package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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

// RBAC for Leases - controller acquires leases for single-writer guarantee
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// RBAC for Events - controller emits events for observability
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// TODO: Future RBAC for pods access (pre-check: pod version validation)
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// TODO: Future RBAC for services access
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

// TODO: Future RBAC for custom metrics (pre/post checks)
//+kubebuilder:rbac:groups=custom.metrics.k8s.io,resources=*,verbs=get;list

// TODO: Future RBAC for external metrics (pre/post checks)
//+kubebuilder:rbac:groups=external.metrics.k8s.io,resources=*,verbs=get;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.2/pkg/reconcile
func (r *DBUpgradeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the DBUpgrade instance
	dbUpgrade := &dbupgradev1alpha1.DBUpgrade{}
	if err := r.Get(ctx, req.NamespacedName, dbUpgrade); err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "unable to fetch DBUpgrade")
		return ctrl.Result{}, err
	}

	// 2. Initialize status if needed
	if err := r.updateStatus(ctx, dbUpgrade); err != nil {
		logger.Error(err, "unable to update DBUpgrade status")
		return ctrl.Result{}, err
	}

	// Re-fetch after status update to get latest version
	if err := r.Get(ctx, req.NamespacedName, dbUpgrade); err != nil {
		return ctrl.Result{}, err
	}

	// 3. Only proceed for selfHosted (skip AWS for now - Phase 2C)
	if dbUpgrade.Spec.Database.Type != dbupgradev1alpha1.DatabaseTypeSelfHosted {
		logger.Info("Skipping non-selfHosted database type (AWS support coming in Phase 2C)", "type", dbUpgrade.Spec.Database.Type)
		// Set Blocked condition for AWS types
		observedGen := dbUpgrade.Generation
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionBlocked, true, "AWSNotImplemented", "AWS RDS/Aurora support not yet implemented", observedGen)
		if err := r.Status().Update(ctx, dbUpgrade); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 4. Validate customer's Secret exists
	if err := r.validateSecret(ctx, dbUpgrade); err != nil {
		logger.Error(err, "Secret validation failed")
		// Set Degraded condition
		observedGen := dbUpgrade.Generation
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionDegraded, true, "SecretNotFound", err.Error(), observedGen)
		if updateErr := r.Status().Update(ctx, dbUpgrade); updateErr != nil {
			logger.Error(updateErr, "Failed to update status")
		}
		// Emit event
		r.recordEvent(ctx, dbUpgrade, corev1.EventTypeWarning, "SecretNotFound", err.Error())
		// Requeue after 30 seconds to allow Secret to be created
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 5. Ensure operator-managed Secret for the Job (unified codepath)
	migrationSecret, err := r.ensureMigrationSecret(ctx, dbUpgrade)
	if err != nil {
		logger.Error(err, "Failed to ensure migration secret")
		return ctrl.Result{}, err
	}

	// 6. Check if Job already exists
	job, err := r.getJobForDBUpgrade(ctx, dbUpgrade)
	if err != nil {
		logger.Error(err, "Failed to get Job for DBUpgrade")
		return ctrl.Result{}, err
	}

	// 7. Create Job if doesn't exist
	if job == nil {
		logger.Info("No Job found, creating migration Job")
		job, err = r.createMigrationJob(ctx, dbUpgrade, migrationSecret)
		if err != nil {
			logger.Error(err, "Failed to create migration Job")
			return ctrl.Result{}, err
		}
		// Emit event for Job creation
		r.recordEvent(ctx, dbUpgrade, corev1.EventTypeNormal, "MigrationStarted", fmt.Sprintf("Created migration Job %s", job.Name))
	}

	// 8. Sync Job status to DBUpgrade status
	if err := r.syncJobStatus(ctx, dbUpgrade, job); err != nil {
		logger.Error(err, "Failed to sync Job status")
		return ctrl.Result{}, err
	}

	// Update DBUpgrade status
	if err := r.Status().Update(ctx, dbUpgrade); err != nil {
		logger.Error(err, "Failed to update DBUpgrade status")
		return ctrl.Result{}, err
	}

	// 8. Requeue if Job is still running
	if isJobRunning(job) {
		logger.Info("Job is still running, requeuing", "jobName", job.Name)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Emit success/failure events
	if isJobSucceeded(job) {
		r.recordEvent(ctx, dbUpgrade, corev1.EventTypeNormal, "MigrationSucceeded", "Database migration completed successfully")
	} else if isJobFailed(job) {
		r.recordEvent(ctx, dbUpgrade, corev1.EventTypeWarning, "MigrationFailed", "Database migration failed")
	}

	return ctrl.Result{}, nil
}

// updateStatus updates the status of the DBUpgrade resource
// It initializes baseline conditions and observedGeneration when the spec changes.
func (r *DBUpgradeReconciler) updateStatus(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade) error {
	key := types.NamespacedName{
		Name:      dbUpgrade.Name,
		Namespace: dbUpgrade.Namespace,
	}

	// Retry on conflict
	for {
		// Fetch latest version
		latest := &dbupgradev1alpha1.DBUpgrade{}
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}

		// Create a deep copy to compare against
		orig := latest.DeepCopy()

		// Check if spec changed (new generation)
		specChanged := latest.Status.ObservedGeneration != latest.Generation

		// Check if Accepted condition exists
		acceptedCondition := findCondition(latest.Status.Conditions, string(dbupgradev1alpha1.ConditionAccepted))
		missingAccepted := acceptedCondition == nil

		// Update status if spec changed or Accepted condition is missing
		if specChanged || missingAccepted {
			// Update observed generation
			observedGen := latest.Generation
			latest.Status.ObservedGeneration = observedGen

			// Always set Accepted=True when spec changes or is missing
			dbupgradev1alpha1.SetAcceptedTrue(&latest.Status.Conditions, "Spec validated", observedGen)

			if specChanged {
				// Spec changed: reset baseline conditions deterministically for new spec
				// This prevents stale Ready=True from previous successful upgrade
				// Check if Blocked condition exists and is True (before reset)
				blockedCondition := findCondition(latest.Status.Conditions, string(dbupgradev1alpha1.ConditionBlocked))
				isBlocked := blockedCondition != nil && blockedCondition.Status == metav1.ConditionTrue

				if isBlocked {
					// Blocked=True: enforce Progressing=False and Ready=False
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionProgressing, false, dbupgradev1alpha1.ReasonIdle, "Blocked - migration cannot progress", observedGen)
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionReady, false, dbupgradev1alpha1.ReasonIdle, "Blocked - precheck gate failing", observedGen)
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionBlocked, true, blockedCondition.Reason, blockedCondition.Message, observedGen)
				} else {
					// Reset baseline conditions for new spec
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionReady, false, dbupgradev1alpha1.ReasonInitializing, "No upgrade run started yet", observedGen)
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionProgressing, false, dbupgradev1alpha1.ReasonIdle, "No migration in progress", observedGen)
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionBlocked, false, dbupgradev1alpha1.ReasonIdle, "No blocking conditions detected", observedGen)
				}

				// Degraded=False reason=Idle (always reset on spec change)
				dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionDegraded, false, dbupgradev1alpha1.ReasonIdle, "System is healthy", observedGen)
			} else {
				// Spec hasn't changed, but Accepted was missing: only fill in missing conditions
				// Don't stomp existing conditions
				readyCondition := findCondition(latest.Status.Conditions, string(dbupgradev1alpha1.ConditionReady))
				if readyCondition == nil {
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionReady, false, dbupgradev1alpha1.ReasonInitializing, "No upgrade run started yet", observedGen)
				}

				progressingCondition := findCondition(latest.Status.Conditions, string(dbupgradev1alpha1.ConditionProgressing))
				if progressingCondition == nil {
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionProgressing, false, dbupgradev1alpha1.ReasonIdle, "No migration in progress", observedGen)
				}

				blockedCondition := findCondition(latest.Status.Conditions, string(dbupgradev1alpha1.ConditionBlocked))
				if blockedCondition == nil {
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionBlocked, false, dbupgradev1alpha1.ReasonIdle, "No blocking conditions detected", observedGen)
				}

				degradedCondition := findCondition(latest.Status.Conditions, string(dbupgradev1alpha1.ConditionDegraded))
				if degradedCondition == nil {
					dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.ConditionDegraded, false, dbupgradev1alpha1.ReasonIdle, "System is healthy", observedGen)
				}
			}
		}

		// Only patch if status actually changed
		if !equality.Semantic.DeepEqual(orig.Status, latest.Status) {
			if err := r.Status().Patch(ctx, latest, client.MergeFrom(orig)); err != nil {
				if errors.IsConflict(err) {
					// Retry on conflict
					continue
				}
				return err
			}
		}
		break
	}

	return nil
}

// findCondition finds a condition by type in the conditions slice
func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// validateSecret validates that the connection Secret exists and has the required key
func (r *DBUpgradeReconciler) validateSecret(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade) error {
	logger := log.FromContext(ctx)

	// Only validate for selfHosted databases
	if dbUpgrade.Spec.Database.Type != dbupgradev1alpha1.DatabaseTypeSelfHosted {
		return nil
	}

	// Check that connection is configured
	if dbUpgrade.Spec.Database.Connection == nil || dbUpgrade.Spec.Database.Connection.URLSecretRef == nil {
		return fmt.Errorf("database.connection.urlSecretRef is required for selfHosted database")
	}

	secretRef := dbUpgrade.Spec.Database.Connection.URLSecretRef
	secretName := secretRef.Name
	secretKey := secretRef.Key

	// Fetch the Secret
	secret := &corev1.Secret{}
	secretNamespacedName := types.NamespacedName{
		Name:      secretName,
		Namespace: dbUpgrade.Namespace,
	}

	if err := r.Get(ctx, secretNamespacedName, secret); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Connection secret not found", "secret", secretName)
			return fmt.Errorf("connection secret %q not found in namespace %q", secretName, dbUpgrade.Namespace)
		}
		return fmt.Errorf("failed to get connection secret: %w", err)
	}

	// Verify the key exists in the Secret
	if _, exists := secret.Data[secretKey]; !exists {
		logger.Info("Secret key not found", "secret", secretName, "key", secretKey)
		return fmt.Errorf("key %q not found in secret %q", secretKey, secretName)
	}

	logger.Info("Secret validation passed", "secret", secretName, "key", secretKey)
	return nil
}

// ensureMigrationSecret creates or updates the operator-managed Secret for the migration Job
// This provides a unified codepath for both self-hosted and RDS databases
func (r *DBUpgradeReconciler) ensureMigrationSecret(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade) (*corev1.Secret, error) {
	logger := log.FromContext(ctx)

	secretName := fmt.Sprintf("dbupgrade-%s-connection", dbUpgrade.Name)

	// For selfHosted, read the customer's Secret and copy the connection URL
	if dbUpgrade.Spec.Database.Type == dbupgradev1alpha1.DatabaseTypeSelfHosted {
		customerSecretRef := dbUpgrade.Spec.Database.Connection.URLSecretRef
		customerSecret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      customerSecretRef.Name,
			Namespace: dbUpgrade.Namespace,
		}, customerSecret); err != nil {
			return nil, fmt.Errorf("failed to get customer secret: %w", err)
		}

		connectionURL := customerSecret.Data[customerSecretRef.Key]

		// Check if operator Secret already exists
		existingSecret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: dbUpgrade.Namespace}, existingSecret)
		if err == nil {
			// Secret exists, update if connection URL changed
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
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         "dbupgrade.subbug.learning/v1alpha1",
						Kind:               "DBUpgrade",
						Name:               dbUpgrade.Name,
						UID:                dbUpgrade.UID,
						Controller:         boolPtr(true),
						BlockOwnerDeletion: boolPtr(true),
					},
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"url": connectionURL,
			},
		}

		if err := r.Create(ctx, operatorSecret); err != nil {
			return nil, fmt.Errorf("failed to create migration secret: %w", err)
		}
		logger.Info("Created migration secret", "secret", secretName)
		return operatorSecret, nil
	}

	// TODO: For AWS RDS/Aurora (Phase 2C), generate IAM token and create Secret
	return nil, fmt.Errorf("AWS database types not yet supported")
}

// getJobForDBUpgrade finds the Job owned by this DBUpgrade
func (r *DBUpgradeReconciler) getJobForDBUpgrade(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade) (*batchv1.Job, error) {
	// List all Jobs in the namespace
	jobList := &batchv1.JobList{}
	if err := r.List(ctx, jobList, client.InNamespace(dbUpgrade.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list Jobs: %w", err)
	}

	// Find Job with matching owner reference
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
	// Marshal spec to JSON
	specJSON, err := json.Marshal(spec)
	if err != nil {
		// Fallback to empty string if marshaling fails
		return ""
	}

	// Compute SHA256 hash
	hash := sha256.Sum256(specJSON)
	// Return first 8 characters of hex representation
	return fmt.Sprintf("%x", hash)[:8]
}

// isJobRunning checks if Job is in running state
func isJobRunning(job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	// Job is running if it has active pods and hasn't completed or failed
	return job.Status.Active > 0
}

// isJobSucceeded checks if Job completed successfully
func isJobSucceeded(job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// isJobFailed checks if Job failed
func isJobFailed(job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// Container images used for migration Jobs
const (
	// CraneImage is used to extract migrations from customer images
	// crane is a tool for interacting with container registries
	// We use the :debug tag which includes busybox (has sh, tar) instead of distroless
	CraneImage = "gcr.io/go-containerregistry/crane:debug"

	// AtlasImage is the official Atlas CLI image for running migrations
	AtlasImage = "arigaio/atlas:latest"
)

// createMigrationJob creates a Kubernetes Job to run database migrations
// Architecture:
// - Init container: Uses crane to extract /migrations from customer's image (works with distroless)
// - Main container: Uses official Atlas CLI to run migrations
// - Shared emptyDir volume for migrations
// - Database URL from operator-managed Secret
func (r *DBUpgradeReconciler) createMigrationJob(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade, migrationSecret *corev1.Secret) (*batchv1.Job, error) {
	logger := log.FromContext(ctx)

	// Compute spec hash for the Job name
	specHash := computeSpecHash(dbUpgrade.Spec)
	jobName := fmt.Sprintf("dbupgrade-%s-%s", dbUpgrade.Name, specHash)

	// Default active deadline to 600 seconds (10 minutes)
	activeDeadlineSeconds := int64(600)
	if dbUpgrade.Spec.Runner != nil && dbUpgrade.Spec.Runner.ActiveDeadlineSeconds != nil {
		activeDeadlineSeconds = *dbUpgrade.Spec.Runner.ActiveDeadlineSeconds
	}

	// Get migrations directory in customer's image (default to /migrations)
	migrationsDir := "/migrations"
	if dbUpgrade.Spec.Migrations.Dir != "" {
		migrationsDir = dbUpgrade.Spec.Migrations.Dir
	}

	// Init container command: extract migrations from customer image using crane
	// crane export exports image filesystem as tarball, tar extracts the migrations directory
	// This works with distroless/scratch images since we don't run the customer image
	// --insecure allows pulling from HTTP registries (like local dev registries)
	initCommand := fmt.Sprintf(`crane export --insecure %s - | tar -xf - -C /shared %s`,
		dbUpgrade.Spec.Migrations.Image,
		migrationsDir[1:]) // Remove leading slash for tar

	// Build Job spec
	backoffLimit := int32(0) // No retries - migrations should be idempotent
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: dbUpgrade.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "dbupgrade.subbug.learning/v1alpha1",
					Kind:               "DBUpgrade",
					Name:               dbUpgrade.Name,
					UID:                dbUpgrade.UID,
					Controller:         boolPtr(true),
					BlockOwnerDeletion: boolPtr(true),
				},
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					// Shared volume for migrations between init and main container
					Volumes: []corev1.Volume{
						{
							Name: "migrations",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
					// Init container: extract migrations from customer image
					InitContainers: []corev1.Container{
						{
							Name:    "fetch-migrations",
							Image:   CraneImage,
							Command: []string{"sh", "-c"},
							Args:    []string{initCommand},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "migrations",
									MountPath: "/shared",
								},
							},
						},
					},
					// Main container: run Atlas CLI migrations
					// Note: Atlas image ENTRYPOINT is /atlas, so we use it directly
					Containers: []corev1.Container{
						{
							Name:    "migrate",
							Image:   AtlasImage,
							Command: []string{"/atlas", "migrate", "apply"},
							Args: []string{
								"--dir", fmt.Sprintf("file:///migrations%s", migrationsDir),
								"--url", "$(DATABASE_URL)",
							},
							Env: []corev1.EnvVar{
								{
									Name: "DATABASE_URL",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: migrationSecret.Name,
											},
											Key: "url",
										},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "migrations",
									MountPath: "/migrations",
								},
							},
						},
					},
				},
			},
		},
	}

	// Create the Job
	if err := r.Create(ctx, job); err != nil {
		logger.Error(err, "Failed to create migration Job", "jobName", jobName)
		return nil, fmt.Errorf("failed to create migration Job: %w", err)
	}

	logger.Info("Created migration Job", "jobName", jobName, "initImage", CraneImage, "atlasImage", AtlasImage)
	return job, nil
}

// syncJobStatus synchronizes the Job status to DBUpgrade status conditions
func (r *DBUpgradeReconciler) syncJobStatus(ctx context.Context, dbUpgrade *dbupgradev1alpha1.DBUpgrade, job *batchv1.Job) error {
	logger := log.FromContext(ctx)
	observedGen := dbUpgrade.Generation

	// If no Job exists yet
	if job == nil {
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionReady, false, dbupgradev1alpha1.ReasonInitializing, "No migration Job exists yet", observedGen)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionProgressing, false, dbupgradev1alpha1.ReasonIdle, "No migration in progress", observedGen)
		return nil
	}

	// Check Job status
	if isJobSucceeded(job) {
		logger.Info("Migration Job succeeded", "jobName", job.Name)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionReady, true, "MigrationComplete", "Database migration completed successfully", observedGen)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionProgressing, false, dbupgradev1alpha1.ReasonIdle, "Migration completed", observedGen)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionDegraded, false, dbupgradev1alpha1.ReasonIdle, "System is healthy", observedGen)
		return nil
	}

	if isJobFailed(job) {
		logger.Info("Migration Job failed", "jobName", job.Name)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionReady, false, "JobFailed", "Migration Job failed", observedGen)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionProgressing, false, dbupgradev1alpha1.ReasonIdle, "Migration failed", observedGen)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionDegraded, true, "MigrationFailed", "Database migration failed", observedGen)
		return nil
	}

	if isJobRunning(job) {
		logger.Info("Migration Job is running", "jobName", job.Name)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionReady, false, "JobRunning", "Migration Job is running", observedGen)
		dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionProgressing, true, "MigrationInProgress", "Database migration is in progress", observedGen)
		return nil
	}

	// Job exists but hasn't started yet (Pending)
	logger.Info("Migration Job is pending", "jobName", job.Name)
	dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionReady, false, "JobPending", "Migration Job is pending", observedGen)
	dbupgradev1alpha1.SetCondition(&dbUpgrade.Status.Conditions, dbupgradev1alpha1.ConditionProgressing, true, "MigrationInProgress", "Migration Job is starting", observedGen)
	return nil
}

// recordEvent emits a Kubernetes Event for observability
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
		Type:    eventType,
		Reason:  reason,
		Message: message,
		Source: corev1.EventSource{
			Component: "dbupgrade-controller",
		},
		FirstTimestamp: metav1.NewTime(time.Now()),
		LastTimestamp:  metav1.NewTime(time.Now()),
		Count:          1,
	}

	if err := r.Create(ctx, event); err != nil {
		logger.Error(err, "Failed to create event", "reason", reason)
	} else {
		logger.Info("Emitted event", "type", eventType, "reason", reason)
	}
}

// boolPtr returns a pointer to a bool value
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
