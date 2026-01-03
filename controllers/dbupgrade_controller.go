package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
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

// TODO: Future RBAC for pods access
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// TODO: Future RBAC for services access
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch

// TODO: Future RBAC for jobs access
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// TODO: Future RBAC for custom metrics
//+kubebuilder:rbac:groups=custom.metrics.k8s.io,resources=*,verbs=get;list

// TODO: Future RBAC for external metrics
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

		// Update observed generation
		latest.Status.ObservedGeneration = latest.Generation

		// Set conditions using meta.SetStatusCondition
		// Progressing=False at rest
		dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.DBUpgradeConditionProgressing, false, "Idle", "No migration in progress")

		// Ready=False with Reason "Pending" initially
		dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.DBUpgradeConditionReady, false, "Pending", "Migrations not yet executed")

		// Blocked=False initially
		dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.DBUpgradeConditionBlocked, false, "NoBlock", "No blocking conditions detected")

		// Degraded=False initially
		dbupgradev1alpha1.SetCondition(&latest.Status.Conditions, dbupgradev1alpha1.DBUpgradeConditionDegraded, false, "Healthy", "System is healthy")

		// Update status with retry-on-conflict
		if err := r.Status().Update(ctx, latest); err != nil {
			if errors.IsConflict(err) {
				// Retry on conflict
				continue
			}
			return err
		}
		break
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DBUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbupgradev1alpha1.DBUpgrade{}).
		Complete(r)
}

