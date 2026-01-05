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

	// Validate cross-field constraints for metrics
	if r.Spec.Checks != nil {
		if err := r.validateMetrics(); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	if len(allErrs) > 0 {
		return fmt.Errorf("validation failed: %v", allErrs)
	}

	return nil
}

// validateDatabase ensures database configuration is valid
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
