/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Masterminds/semver/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func (r *DBUpgrade) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-dbupgrade-subbug-learning-v1alpha1-dbupgrade,mutating=false,failurePolicy=fail,sideEffects=None,groups=dbupgrade.subbug.learning,resources=dbupgrades,verbs=create;update,versions=v1alpha1,name=vdbupgrade.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &DBUpgrade{}

// ValidateCreate implements webhook.Validator
func (r *DBUpgrade) ValidateCreate() (admission.Warnings, error) {
	return nil, r.validateDBUpgrade()
}

// ValidateUpdate implements webhook.Validator
func (r *DBUpgrade) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	oldDBUpgrade := old.(*DBUpgrade)

	// Block spec changes while migration is running
	if err := r.validateNotProgressing(oldDBUpgrade); err != nil {
		return nil, err
	}

	// Validate immutable fields
	if err := r.validateImmutableFields(oldDBUpgrade); err != nil {
		return nil, err
	}

	// Validate current state
	return nil, r.validateDBUpgrade()
}

// ValidateDelete implements webhook.Validator
func (r *DBUpgrade) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

// validateDBUpgrade contains validation logic
func (r *DBUpgrade) validateDBUpgrade() error {
	var allErrs []error

	// Validate database configuration
	if err := r.validateDatabase(); err != nil {
		allErrs = append(allErrs, err)
	}

	// Validate checks configuration
	if r.Spec.Checks != nil {
		// Validate minPodVersions
		if err := r.validateMinPodVersions(); err != nil {
			allErrs = append(allErrs, err)
		}

		// Validate cross-field constraints for metrics
		if err := r.validateMetrics(); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	if len(allErrs) > 0 {
		return fmt.Errorf("validation failed: %v", allErrs)
	}

	return nil
}

// validateMinPodVersions validates minPodVersion checks
func (r *DBUpgrade) validateMinPodVersions() error {
	for i, check := range r.Spec.Checks.Pre.MinPodVersions {
		// Validate minVersion is valid semver
		_, err := semver.NewVersion(strings.TrimPrefix(check.MinVersion, "v"))
		if err != nil {
			return fmt.Errorf("checks.pre.minPodVersions[%d].minVersion %q is not valid semver: %w", i, check.MinVersion, err)
		}
	}
	return nil
}

// validateDatabase ensures database configuration is valid
// Note: We do NOT validate Secret existence here (would add latency and require
// webhook to have Secret RBAC). The controller validates Secret existence and
// reports issues via Degraded condition.
func (r *DBUpgrade) validateDatabase() error {
	dbType := r.Spec.Database.Type
	hasAWS := r.Spec.Database.AWS != nil
	hasConnection := r.Spec.Database.Connection != nil

	switch dbType {
	case DatabaseTypeAWSRDS, DatabaseTypeAWSAurora:
		// For AWS types, must have AWS config OR connection secret
		if !hasAWS && !hasConnection {
			return fmt.Errorf("database.type=%s requires either aws or connection to be set", dbType)
		}

		// If AWS config provided, validate required fields
		if hasAWS {
			if r.Spec.Database.AWS.RoleArn == "" {
				return fmt.Errorf("database.aws.roleArn is required when aws is specified")
			}
			if r.Spec.Database.AWS.Region == "" {
				return fmt.Errorf("database.aws.region is required when aws is specified")
			}
			if r.Spec.Database.AWS.Host == "" {
				return fmt.Errorf("database.aws.host is required when aws is specified")
			}
			if r.Spec.Database.AWS.DBName == "" {
				return fmt.Errorf("database.aws.dbName is required when aws is specified")
			}
			if r.Spec.Database.AWS.Username == "" {
				return fmt.Errorf("database.aws.username is required when aws is specified")
			}
		}

	case DatabaseTypeSelfHosted:
		// Self-hosted must have connection secret
		if !hasConnection {
			return fmt.Errorf("database.type=selfHosted requires connection to be set")
		}

		// Warn if AWS config is set for self-hosted
		if hasAWS {
			return fmt.Errorf("database.aws should not be set for selfHosted type")
		}
	}

	// Validate connection secret if provided
	if hasConnection {
		if r.Spec.Database.Connection.URLSecretRef == nil {
			return fmt.Errorf("database.connection.urlSecretRef is required when connection is specified")
		}
		if r.Spec.Database.Connection.URLSecretRef.Name == "" {
			return fmt.Errorf("database.connection.urlSecretRef.name cannot be empty")
		}
		if r.Spec.Database.Connection.URLSecretRef.Key == "" {
			return fmt.Errorf("database.connection.urlSecretRef.key cannot be empty")
		}
	}

	return nil
}

// validateMetrics validates metric check configurations
func (r *DBUpgrade) validateMetrics() error {
	// Validate pre-check metrics
	for _, metric := range r.Spec.Checks.Pre.Metrics {
		if err := validateMetricCheck(metric); err != nil {
			return fmt.Errorf("pre-check metric %q: %w", metric.Name, err)
		}
	}

	// Validate post-check metrics
	for _, metric := range r.Spec.Checks.Post.Metrics {
		if err := validateMetricCheck(metric); err != nil {
			return fmt.Errorf("post-check metric %q: %w", metric.Name, err)
		}
	}

	return nil
}

// validateMetricCheck validates a single metric check
func validateMetricCheck(m MetricCheck) error {
	// Validate that target type matches target configuration
	switch m.Target.Type {
	case MetricTargetTypePods:
		if m.Target.Pods == nil {
			return fmt.Errorf("target.type=Pods requires target.pods to be set")
		}
		if m.Target.Object != nil {
			return fmt.Errorf("target.type=Pods should not have target.object set")
		}
		if m.Target.External != nil {
			return fmt.Errorf("target.type=Pods should not have target.external set")
		}

	case MetricTargetTypeObject:
		if m.Target.Object == nil {
			return fmt.Errorf("target.type=Object requires target.object to be set")
		}
		if m.Target.Pods != nil {
			return fmt.Errorf("target.type=Object should not have target.pods set")
		}
		if m.Target.External != nil {
			return fmt.Errorf("target.type=Object should not have target.external set")
		}

	case MetricTargetTypeExternal:
		// External can have selector (optional)
		if m.Target.Pods != nil {
			return fmt.Errorf("target.type=External should not have target.pods set")
		}
		if m.Target.Object != nil {
			return fmt.Errorf("target.type=External should not have target.object set")
		}
	}

	// Validate threshold value is not empty
	if m.Threshold.Value.IsZero() {
		return fmt.Errorf("threshold.value cannot be empty")
	}

	return nil
}

// validateNotProgressing blocks spec changes while a migration is running.
// This prevents partial migration state where a migration is interrupted.
// Note: The controller also has this guard for defense in depth.
func (r *DBUpgrade) validateNotProgressing(old *DBUpgrade) error {
	// Only block if spec actually changed (allow metadata/status-only updates)
	if reflect.DeepEqual(old.Spec, r.Spec) {
		return nil
	}

	// Check if migration is in progress
	for _, cond := range old.Status.Conditions {
		if cond.Type == string(ConditionProgressing) && cond.Status == metav1.ConditionTrue {
			return fmt.Errorf("cannot update spec while migration is in progress (Progressing=True); wait for current migration to complete")
		}
	}

	return nil
}

// validateImmutableFields ensures immutable fields haven't changed
// Mutable fields: migrations.image, migrations.dir, checks, runner
// Immutable fields: database.* (type, connection, aws)
func (r *DBUpgrade) validateImmutableFields(old *DBUpgrade) error {
	// Database type is immutable
	if r.Spec.Database.Type != old.Spec.Database.Type {
		return fmt.Errorf("database.type is immutable (cannot change from %s to %s)",
			old.Spec.Database.Type, r.Spec.Database.Type)
	}

	// Connection secret reference is immutable
	oldHasConnection := old.Spec.Database.Connection != nil && old.Spec.Database.Connection.URLSecretRef != nil
	newHasConnection := r.Spec.Database.Connection != nil && r.Spec.Database.Connection.URLSecretRef != nil

	if oldHasConnection != newHasConnection {
		return fmt.Errorf("database.connection cannot be added or removed after creation")
	}

	if oldHasConnection && newHasConnection {
		oldRef := old.Spec.Database.Connection.URLSecretRef
		newRef := r.Spec.Database.Connection.URLSecretRef

		if oldRef.Name != newRef.Name || oldRef.Key != newRef.Key {
			return fmt.Errorf("database.connection.urlSecretRef is immutable (cannot change from %s/%s to %s/%s)",
				oldRef.Name, oldRef.Key, newRef.Name, newRef.Key)
		}
	}

	// AWS configuration is immutable
	oldHasAWS := old.Spec.Database.AWS != nil
	newHasAWS := r.Spec.Database.AWS != nil

	if oldHasAWS != newHasAWS {
		return fmt.Errorf("database.aws cannot be added or removed after creation")
	}

	if oldHasAWS && newHasAWS {
		oldAWS := old.Spec.Database.AWS
		newAWS := r.Spec.Database.AWS

		if oldAWS.RoleArn != newAWS.RoleArn {
			return fmt.Errorf("database.aws.roleArn is immutable")
		}
		if oldAWS.Region != newAWS.Region {
			return fmt.Errorf("database.aws.region is immutable")
		}
		if oldAWS.Host != newAWS.Host {
			return fmt.Errorf("database.aws.host is immutable")
		}
		if oldAWS.Port != newAWS.Port {
			return fmt.Errorf("database.aws.port is immutable")
		}
		if oldAWS.DBName != newAWS.DBName {
			return fmt.Errorf("database.aws.dbName is immutable")
		}
		if oldAWS.Username != newAWS.Username {
			return fmt.Errorf("database.aws.username is immutable")
		}
	}

	return nil
}
