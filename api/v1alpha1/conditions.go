package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reason constants for Progressing condition
// When Progressing=False and Ready=False, these explain why
const (
	// ReasonInitializing - no migration has started yet
	ReasonInitializing = "Initializing"

	// ReasonMigrationInProgress - migration job is running
	ReasonMigrationInProgress = "MigrationInProgress"

	// ReasonMigrationComplete - migration succeeded (used with Ready=True)
	ReasonMigrationComplete = "MigrationComplete"

	// ReasonJobFailed - migration job failed
	ReasonJobFailed = "JobFailed"

	// ReasonJobPending - migration job is pending (waiting for pod scheduling)
	ReasonJobPending = "JobPending"

	// ReasonSecretNotFound - database connection secret not found
	ReasonSecretNotFound = "SecretNotFound"

	// ReasonAWSNotSupported - AWS RDS/Aurora not yet implemented
	ReasonAWSNotSupported = "AWSNotSupported"

	// ReasonPreCheckImageVersionFailed - image version precheck failed
	ReasonPreCheckImageVersionFailed = "PreCheckImageVersionFailed"

	// ReasonPreCheckMetricFailed - metric precheck failed
	ReasonPreCheckMetricFailed = "PreCheckMetricFailed"

	// ReasonPostCheckFailed - post-migration check failed
	ReasonPostCheckFailed = "PostCheckFailed"
)

// SetCondition sets or updates a condition in the conditions slice
func SetCondition(conditions *[]metav1.Condition, conditionType DBUpgradeConditionType, status bool, reason, message string, observedGeneration int64) {
	conditionStatus := metav1.ConditionFalse
	if status {
		conditionStatus = metav1.ConditionTrue
	}

	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}

// SetReady sets the Ready condition
func SetReady(conditions *[]metav1.Condition, status bool, reason, message string, observedGeneration int64) {
	SetCondition(conditions, ConditionReady, status, reason, message, observedGeneration)
}

// SetProgressing sets the Progressing condition
func SetProgressing(conditions *[]metav1.Condition, status bool, reason, message string, observedGeneration int64) {
	SetCondition(conditions, ConditionProgressing, status, reason, message, observedGeneration)
}
