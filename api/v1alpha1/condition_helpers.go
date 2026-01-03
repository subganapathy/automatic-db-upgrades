package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/meta"
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

