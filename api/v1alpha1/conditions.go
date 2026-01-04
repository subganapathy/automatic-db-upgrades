package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition type constants
const (
	ConditionAccepted    DBUpgradeConditionType = "Accepted"
	ConditionReady       DBUpgradeConditionType = "Ready"
	ConditionProgressing DBUpgradeConditionType = "Progressing"
	ConditionBlocked     DBUpgradeConditionType = "Blocked"
	ConditionDegraded    DBUpgradeConditionType = "Degraded"
)

// Stable reason constants
const (
	ReasonValidSpec    = "ValidSpec"
	ReasonInvalidSpec  = "InvalidSpec"
	ReasonInitializing = "Initializing"
	ReasonIdle         = "Idle"
)

// SetCondition sets or updates a condition in the conditions slice
func SetCondition(conditions *[]metav1.Condition, conditionType DBUpgradeConditionType, status bool, reason, message string) {
	conditionStatus := metav1.ConditionFalse
	if status {
		conditionStatus = metav1.ConditionTrue
	}

	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:    string(conditionType),
		Status:  conditionStatus,
		Reason:  reason,
		Message: message,
	})
}

// SetAcceptedTrue sets the Accepted condition to True
func SetAcceptedTrue(conditions *[]metav1.Condition, message string) {
	SetCondition(conditions, ConditionAccepted, true, ReasonValidSpec, message)
}

// SetAcceptedFalse sets the Accepted condition to False
func SetAcceptedFalse(conditions *[]metav1.Condition, reason, message string) {
	SetCondition(conditions, ConditionAccepted, false, reason, message)
}
