package controllers

import (
	"context"

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

	// Fetch the DBUpgrade instance
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

	// Update status with retry-on-conflict
	err := r.updateStatus(ctx, dbUpgrade)
	if err != nil {
		logger.Error(err, "unable to update DBUpgrade status")
		return ctrl.Result{}, err
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

// SetupWithManager sets up the controller with the Manager.
func (r *DBUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbupgradev1alpha1.DBUpgrade{}).
		Complete(r)
}
