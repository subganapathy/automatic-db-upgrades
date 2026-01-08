package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbupgradev1alpha1 "github.com/subganapathy/automatic-db-upgrades/api/v1alpha1"
)

// DBUpgradeReconciler reconciles a DBUpgrade object
type DBUpgradeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=dbupgrade.subbug.learning,resources=dbupgrades,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbupgrade.subbug.learning,resources=dbupgrades/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbupgrade.subbug.learning,resources=dbupgrades/finalizers,verbs=update

// RBAC for migration Jobs - controller creates, monitors, and cleans up Jobs
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get

// RBAC for Secrets - controller creates RDS tokens or reads user-provided secrets
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// RBAC for Events - controller emits events for observability
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// RBAC for Leases - required for leader election
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// TODO: Future RBAC for pods access (pre-check: pod version validation)
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// TODO: Future RBAC for custom metrics (pre/post checks)
//+kubebuilder:rbac:groups=custom.metrics.k8s.io,resources=*,verbs=get;list

// TODO: Future RBAC for external metrics (pre/post checks)
//+kubebuilder:rbac:groups=external.metrics.k8s.io,resources=*,verbs=get;list

// Container images used for migration Jobs
var (
	// CraneImage extracts migrations from customer images
	// Default uses :debug tag (includes shell/tar). Override via CRANE_IMAGE env var.
	CraneImage = getEnvOrDefault("CRANE_IMAGE", "gcr.io/go-containerregistry/crane:debug")

	// AtlasImage is the official Atlas CLI image for running migrations
	AtlasImage = getEnvOrDefault("ATLAS_IMAGE", "arigaio/atlas:latest")

	// AllowInsecureRegistries enables --insecure flag for crane (local dev only)
	AllowInsecureRegistries = os.Getenv("ALLOW_INSECURE_REGISTRIES") == "true"
)

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// reconcileResult holds the state to be written to status at the end of reconcile
type reconcileResult struct {
	ready           bool
	readyReason     string
	readyMessage    string
	progressing     bool
	progressReason  string
	progressMessage string
	requeueAfter    time.Duration
	event           *eventInfo
}

type eventInfo struct {
	eventType string
	reason    string
	message   string
}

// Reconcile is the main reconciliation loop
func (r *DBUpgradeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the DBUpgrade instance
	dbUpgrade := &dbupgradev1alpha1.DBUpgrade{}
	if err := r.Get(ctx, req.NamespacedName, dbUpgrade); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch DBUpgrade")
		return ctrl.Result{}, err
	}

	// 2. Run reconciliation logic and collect result
	result := r.reconcileDBUpgrade(ctx, dbUpgrade)

	// 3. Single status update at the end
	if err := r.updateStatus(ctx, dbUpgrade, result); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	// 4. Emit event if needed
	if result.event != nil {
		r.recordEvent(ctx, dbUpgrade, result.event.eventType, result.event.reason, result.event.message)
	}

	// 5. Return requeue result
	if result.requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: result.requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileDBUpgrade contains the main reconciliation logic
// Returns a reconcileResult that will be written to status
func (r *DBUpgradeReconciler) reconcileDBUpgrade(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade) reconcileResult {
	logger := log.FromContext(ctx)

	// Check database type - only selfHosted supported for now
	if dbUpgrade.Spec.Database.Type != dbupgradev1alpha1.DatabaseTypeSelfHosted {
		logger.Info("AWS database types not yet supported", "type", dbUpgrade.Spec.Database.Type)
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonAWSNotSupported,
			readyMessage:    "AWS RDS/Aurora support not yet implemented",
			progressing:     false,
			progressReason:  dbupgradev1alpha1.ReasonAWSNotSupported,
			progressMessage: fmt.Sprintf("Database type %s not yet supported", dbUpgrade.Spec.Database.Type),
		}
	}

	// Validate customer's Secret exists
	if err := r.validateSecret(ctx, dbUpgrade); err != nil {
		logger.Info("Secret validation failed", "error", err)
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonSecretNotFound,
			readyMessage:    err.Error(),
			progressing:     false,
			progressReason:  dbupgradev1alpha1.ReasonSecretNotFound,
			progressMessage: err.Error(),
			requeueAfter:    30 * time.Second,
			event:           &eventInfo{corev1.EventTypeWarning, "SecretNotFound", err.Error()},
		}
	}

	// Ensure operator-managed Secret for the Job
	migrationSecret, err := r.ensureMigrationSecret(ctx, dbUpgrade)
	if err != nil {
		logger.Error(err, "Failed to ensure migration secret")
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonSecretNotFound,
			readyMessage:    "Failed to create migration secret",
			progressing:     false,
			progressReason:  dbupgradev1alpha1.ReasonSecretNotFound,
			progressMessage: err.Error(),
			requeueAfter:    10 * time.Second,
		}
	}

	// Get current spec hash
	currentHash := computeSpecHash(dbUpgrade.Spec)

	// Find existing Job for this DBUpgrade
	existingJob, err := r.getJobForDBUpgrade(ctx, dbUpgrade)
	if err != nil {
		logger.Error(err, "Failed to get Job")
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonInitializing,
			readyMessage:    "Error checking for existing Job",
			progressing:     false,
			progressReason:  dbupgradev1alpha1.ReasonInitializing,
			progressMessage: err.Error(),
			requeueAfter:    5 * time.Second,
		}
	}

	// Check if existing Job is for current spec (by hash in name)
	expectedJobName := fmt.Sprintf("dbupgrade-%s-%s", dbUpgrade.Name, currentHash)

	if existingJob != nil && existingJob.Name != expectedJobName {
		// Spec changed while Job exists from previous spec

		// Don't delete running Jobs - wait for completion to avoid partial migration state
		if isJobRunning(existingJob) {
			logger.Info("Spec changed but migration is running, waiting for completion",
				"oldJob", existingJob.Name, "expectedJob", expectedJobName)
			return reconcileResult{
				ready:           false,
				readyReason:     dbupgradev1alpha1.ReasonInitializing,
				readyMessage:    "Waiting for current migration to complete",
				progressing:     true,
				progressReason:  dbupgradev1alpha1.ReasonMigrationInProgress,
				progressMessage: "Cannot apply new spec while migration is running",
				requeueAfter:    10 * time.Second,
			}
		}

		// Job is not running (completed or failed) - safe to delete
		logger.Info("Spec changed, deleting completed Job", "oldJob", existingJob.Name, "expectedJob", expectedJobName)

		// Delete the old Job (propagation policy deletes pods too)
		propagation := metav1.DeletePropagationBackground
		if err := r.Delete(ctx, existingJob, &client.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Failed to delete stale Job")
			return reconcileResult{
				ready:           false,
				readyReason:     dbupgradev1alpha1.ReasonInitializing,
				readyMessage:    "Cleaning up stale Job",
				progressing:     false,
				progressReason:  dbupgradev1alpha1.ReasonInitializing,
				progressMessage: "Deleting Job from previous spec",
				requeueAfter:    5 * time.Second,
			}
		}

		// Requeue to create new Job after deletion
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonInitializing,
			readyMessage:    "Spec changed, preparing new migration",
			progressing:     false,
			progressReason:  dbupgradev1alpha1.ReasonInitializing,
			progressMessage: "Deleted old Job, will create new one",
			requeueAfter:    2 * time.Second,
			event:           &eventInfo{corev1.EventTypeNormal, "SpecChanged", "Spec changed, starting new migration"},
		}
	}

	// Create Job if doesn't exist
	if existingJob == nil {
		logger.Info("Creating migration Job", "jobName", expectedJobName)
		job, err := r.createMigrationJob(ctx, dbUpgrade, migrationSecret, currentHash)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				// Race condition - Job was just created, requeue
				return reconcileResult{
					ready:           false,
					readyReason:     dbupgradev1alpha1.ReasonInitializing,
					readyMessage:    "Job creation in progress",
					progressing:     true,
					progressReason:  dbupgradev1alpha1.ReasonJobPending,
					progressMessage: "Migration Job being created",
					requeueAfter:    2 * time.Second,
				}
			}
			logger.Error(err, "Failed to create migration Job")
			return reconcileResult{
				ready:           false,
				readyReason:     dbupgradev1alpha1.ReasonJobFailed,
				readyMessage:    "Failed to create migration Job",
				progressing:     false,
				progressReason:  dbupgradev1alpha1.ReasonJobFailed,
				progressMessage: err.Error(),
				requeueAfter:    30 * time.Second,
			}
		}
		existingJob = job
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonInitializing,
			readyMessage:    "Migration Job created",
			progressing:     true,
			progressReason:  dbupgradev1alpha1.ReasonJobPending,
			progressMessage: fmt.Sprintf("Created Job %s", job.Name),
			requeueAfter:    5 * time.Second,
			event:           &eventInfo{corev1.EventTypeNormal, "MigrationStarted", fmt.Sprintf("Created migration Job %s", job.Name)},
		}
	}

	// Sync Job status to conditions
	return r.syncJobStatus(existingJob)
}

// updateStatus writes the reconcile result to the DBUpgrade status
func (r *DBUpgradeReconciler) updateStatus(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade, result reconcileResult) error {
	// Update observed generation
	dbUpgrade.Status.ObservedGeneration = dbUpgrade.Generation

	// Set conditions
	gen := dbUpgrade.Generation
	dbupgradev1alpha1.SetReady(&dbUpgrade.Status.Conditions, result.ready, result.readyReason, result.readyMessage, gen)
	dbupgradev1alpha1.SetProgressing(&dbUpgrade.Status.Conditions, result.progressing, result.progressReason, result.progressMessage, gen)

	// Update status - if conflict, let controller-runtime requeue
	return r.Status().Update(ctx, dbUpgrade)
}

// validateSecret validates that the connection Secret exists and has the required key
func (r *DBUpgradeReconciler) validateSecret(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade) error {
	if dbUpgrade.Spec.Database.Type != dbupgradev1alpha1.DatabaseTypeSelfHosted {
		return nil
	}

	if dbUpgrade.Spec.Database.Connection == nil || dbUpgrade.Spec.Database.Connection.URLSecretRef == nil {
		return fmt.Errorf("database.connection.urlSecretRef is required for selfHosted database")
	}

	secretRef := dbUpgrade.Spec.Database.Connection.URLSecretRef
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretRef.Name, Namespace: dbUpgrade.Namespace}, secret); err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("secret %q not found in namespace %q", secretRef.Name, dbUpgrade.Namespace)
		}
		return fmt.Errorf("failed to get secret: %w", err)
	}

	if _, exists := secret.Data[secretRef.Key]; !exists {
		return fmt.Errorf("key %q not found in secret %q", secretRef.Key, secretRef.Name)
	}

	return nil
}

// ensureMigrationSecret creates or updates the operator-managed Secret for the migration Job
func (r *DBUpgradeReconciler) ensureMigrationSecret(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade) (*corev1.Secret, error) {
	logger := log.FromContext(ctx)
	secretName := fmt.Sprintf("dbupgrade-%s-connection", dbUpgrade.Name)

	if dbUpgrade.Spec.Database.Type != dbupgradev1alpha1.DatabaseTypeSelfHosted {
		return nil, fmt.Errorf("AWS database types not yet supported")
	}

	// Read customer's Secret
	customerSecretRef := dbUpgrade.Spec.Database.Connection.URLSecretRef
	customerSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: customerSecretRef.Name, Namespace: dbUpgrade.Namespace}, customerSecret); err != nil {
		return nil, fmt.Errorf("failed to get customer secret: %w", err)
	}
	connectionURL := customerSecret.Data[customerSecretRef.Key]

	// Check if operator Secret already exists
	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: dbUpgrade.Namespace}, existingSecret)
	if err == nil {
		// Update if connection URL changed
		if string(existingSecret.Data["url"]) != string(connectionURL) {
			existingSecret.Data["url"] = connectionURL
			if err := r.Update(ctx, existingSecret); err != nil {
				return nil, fmt.Errorf("failed to update migration secret: %w", err)
			}
			logger.Info("Updated migration secret", "secret", secretName)
		}
		return existingSecret, nil
	}
	if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to check migration secret: %w", err)
	}

	// Create new operator-managed Secret
	operatorSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: dbUpgrade.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "dbupgrade.subbug.learning/v1alpha1",
				Kind:               "DBUpgrade",
				Name:               dbUpgrade.Name,
				UID:                dbUpgrade.UID,
				Controller:         boolPtr(true),
				BlockOwnerDeletion: boolPtr(true),
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"url": connectionURL},
	}

	if err := r.Create(ctx, operatorSecret); err != nil {
		return nil, fmt.Errorf("failed to create migration secret: %w", err)
	}
	logger.Info("Created migration secret", "secret", secretName)
	return operatorSecret, nil
}

// getJobForDBUpgrade finds the Job owned by this DBUpgrade
func (r *DBUpgradeReconciler) getJobForDBUpgrade(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade) (*batchv1.Job, error) {
	jobList := &batchv1.JobList{}
	if err := r.List(ctx, jobList, client.InNamespace(dbUpgrade.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list Jobs: %w", err)
	}

	for i := range jobList.Items {
		job := &jobList.Items[i]
		for _, owner := range job.OwnerReferences {
			if owner.UID == dbUpgrade.UID {
				return job, nil
			}
		}
	}
	return nil, nil
}

// computeSpecHash generates a hash of the spec for change detection
func computeSpecHash(spec dbupgradev1alpha1.DBUpgradeSpec) string {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(specJSON)
	return fmt.Sprintf("%x", hash)[:8]
}

// createMigrationJob creates a Kubernetes Job to run database migrations
func (r *DBUpgradeReconciler) createMigrationJob(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade, migrationSecret *corev1.Secret, specHash string) (*batchv1.Job, error) {
	logger := log.FromContext(ctx)
	jobName := fmt.Sprintf("dbupgrade-%s-%s", dbUpgrade.Name, specHash)

	// Default timeout
	activeDeadlineSeconds := int64(600)
	if dbUpgrade.Spec.Runner != nil && dbUpgrade.Spec.Runner.ActiveDeadlineSeconds != nil {
		activeDeadlineSeconds = *dbUpgrade.Spec.Runner.ActiveDeadlineSeconds
	}

	// Migrations directory
	migrationsDir := "/migrations"
	if dbUpgrade.Spec.Migrations.Dir != "" {
		migrationsDir = dbUpgrade.Spec.Migrations.Dir
	}

	// Init container command
	insecureFlag := ""
	if AllowInsecureRegistries {
		insecureFlag = "--insecure "
	}
	initCommand := fmt.Sprintf(`crane export %s--platform linux/$(uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/') %s - | tar -xf - -C /shared %s`,
		insecureFlag,
		dbUpgrade.Spec.Migrations.Image,
		migrationsDir[1:])

	backoffLimit := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: dbUpgrade.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "dbupgrade.subbug.learning/v1alpha1",
				Kind:               "DBUpgrade",
				Name:               dbUpgrade.Name,
				UID:                dbUpgrade.UID,
				Controller:         boolPtr(true),
				BlockOwnerDeletion: boolPtr(true),
			}},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Volumes: []corev1.Volume{{
						Name: "migrations",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
					InitContainers: []corev1.Container{{
						Name:    "fetch-migrations",
						Image:   CraneImage,
						Command: []string{"sh", "-c"},
						Args:    []string{initCommand},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "migrations",
							MountPath: "/shared",
						}},
					}},
					Containers: []corev1.Container{{
						Name:    "migrate",
						Image:   AtlasImage,
						Command: []string{"/atlas", "migrate", "apply"},
						Args: []string{
							"--dir", fmt.Sprintf("file:///migrations%s", migrationsDir),
							"--url", "$(DATABASE_URL)",
						},
						Env: []corev1.EnvVar{{
							Name: "DATABASE_URL",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: migrationSecret.Name},
									Key:                  "url",
								},
							},
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "migrations",
							MountPath: "/migrations",
						}},
					}},
				},
			},
		},
	}

	if err := r.Create(ctx, job); err != nil {
		return nil, err
	}

	logger.Info("Created migration Job", "jobName", jobName)
	return job, nil
}

// syncJobStatus maps Job status to reconcileResult
func (r *DBUpgradeReconciler) syncJobStatus(job *batchv1.Job) reconcileResult {
	if job == nil {
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonInitializing,
			readyMessage:    "No migration Job exists",
			progressing:     false,
			progressReason:  dbupgradev1alpha1.ReasonInitializing,
			progressMessage: "Waiting for Job creation",
		}
	}

	// Job succeeded
	if isJobSucceeded(job) {
		return reconcileResult{
			ready:           true,
			readyReason:     dbupgradev1alpha1.ReasonMigrationComplete,
			readyMessage:    "Database migration completed successfully",
			progressing:     false,
			progressReason:  dbupgradev1alpha1.ReasonMigrationComplete,
			progressMessage: fmt.Sprintf("Job %s completed", job.Name),
			event:           &eventInfo{corev1.EventTypeNormal, "MigrationSucceeded", "Database migration completed successfully"},
		}
	}

	// Job failed
	if isJobFailed(job) {
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonJobFailed,
			readyMessage:    "Migration Job failed",
			progressing:     false,
			progressReason:  dbupgradev1alpha1.ReasonJobFailed,
			progressMessage: fmt.Sprintf("Job %s failed", job.Name),
			event:           &eventInfo{corev1.EventTypeWarning, "MigrationFailed", "Database migration failed"},
		}
	}

	// Job running
	if isJobRunning(job) {
		return reconcileResult{
			ready:           false,
			readyReason:     dbupgradev1alpha1.ReasonInitializing,
			readyMessage:    "Migration in progress",
			progressing:     true,
			progressReason:  dbupgradev1alpha1.ReasonMigrationInProgress,
			progressMessage: fmt.Sprintf("Job %s is running", job.Name),
			requeueAfter:    10 * time.Second,
		}
	}

	// Job pending (created but not yet running)
	return reconcileResult{
		ready:           false,
		readyReason:     dbupgradev1alpha1.ReasonInitializing,
		readyMessage:    "Migration Job pending",
		progressing:     true,
		progressReason:  dbupgradev1alpha1.ReasonJobPending,
		progressMessage: fmt.Sprintf("Job %s is pending", job.Name),
		requeueAfter:    5 * time.Second,
	}
}

func isJobRunning(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	return job.Status.Active > 0
}

func isJobSucceeded(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func isJobFailed(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// recordEvent emits a Kubernetes Event
func (r *DBUpgradeReconciler) recordEvent(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade, eventType, reason, message string) {
	logger := log.FromContext(ctx)

	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.%x", dbUpgrade.Name, time.Now().UnixNano()),
			Namespace: dbUpgrade.Namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "dbupgrade.subbug.learning/v1alpha1",
			Kind:       "DBUpgrade",
			Name:       dbUpgrade.Name,
			Namespace:  dbUpgrade.Namespace,
			UID:        dbUpgrade.UID,
		},
		Type:           eventType,
		Reason:         reason,
		Message:        message,
		Source:         corev1.EventSource{Component: "dbupgrade-controller"},
		FirstTimestamp: metav1.NewTime(time.Now()),
		LastTimestamp:  metav1.NewTime(time.Now()),
		Count:          1,
	}

	if err := r.Create(ctx, event); err != nil {
		logger.Error(err, "Failed to create event", "reason", reason)
	}
}

func boolPtr(b bool) *bool {
	return &b
}

// SetupWithManager sets up the controller with the Manager.
func (r *DBUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbupgradev1alpha1.DBUpgrade{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
