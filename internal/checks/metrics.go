package checks

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	custommetricsclient "k8s.io/metrics/pkg/client/custom_metrics"
	externalmetricsclient "k8s.io/metrics/pkg/client/external_metrics"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbupgradev1alpha1 "github.com/subganapathy/automatic-db-upgrades/api/v1alpha1"
)

// MetricCheckResult contains the result of a metric check
type MetricCheckResult struct {
	Passed  bool
	Message string
	// Values contains the metric values that were checked
	Values []float64
	// ReducedValue is the final value after applying the reduce function
	ReducedValue float64
	// ThresholdValue is the threshold being compared against
	ThresholdValue float64
}

// MetricsChecker provides methods for checking metrics
type MetricsChecker struct {
	customMetricsClient   custommetricsclient.CustomMetricsClient
	externalMetricsClient externalmetricsclient.ExternalMetricsClient
}

// NewMetricsChecker creates a new MetricsChecker from a rest.Config
func NewMetricsChecker(config *rest.Config) (*MetricsChecker, error) {
	// Create discovery client to get available APIs
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	// Create REST mapper
	groupResources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get API group resources: %w", err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	// Create available APIs getter
	apiGetter := custommetricsclient.NewAvailableAPIsGetter(discoveryClient)

	// Create custom metrics client
	customClient := custommetricsclient.NewForConfig(config, mapper, apiGetter)

	// Create external metrics client
	externalClient, err := externalmetricsclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create external metrics client: %w", err)
	}

	return &MetricsChecker{
		customMetricsClient:   customClient,
		externalMetricsClient: externalClient,
	}, nil
}

// CheckMetrics validates metrics against their thresholds
// Note: BakeSeconds is handled at the controller level using status timestamps,
// not via blocking sleep here. This ensures baketime survives operator restarts.
func (m *MetricsChecker) CheckMetrics(ctx context.Context, namespace string, checks []dbupgradev1alpha1.MetricCheck) (*MetricCheckResult, error) {
	for _, check := range checks {
		result, err := m.checkSingleMetric(ctx, namespace, check)
		if err != nil {
			return nil, fmt.Errorf("failed to check metric %s: %w", check.Name, err)
		}

		if !result.Passed {
			return result, nil
		}
	}

	return &MetricCheckResult{
		Passed:  true,
		Message: fmt.Sprintf("All %d metric check(s) passed", len(checks)),
	}, nil
}

func (m *MetricsChecker) checkSingleMetric(ctx context.Context, namespace string, check dbupgradev1alpha1.MetricCheck) (*MetricCheckResult, error) {
	logger := log.FromContext(ctx)

	var values []float64
	var err error

	switch check.Source {
	case dbupgradev1alpha1.MetricSourceCustom, "":
		values, err = m.getCustomMetricValues(ctx, namespace, check)
	case dbupgradev1alpha1.MetricSourceExternal:
		values, err = m.getExternalMetricValues(ctx, namespace, check)
	default:
		return nil, fmt.Errorf("unsupported metric source: %s", check.Source)
	}

	if err != nil {
		return nil, err
	}

	if len(values) == 0 {
		return &MetricCheckResult{
			Passed:  false,
			Message: fmt.Sprintf("No metric values found for %s", check.MetricName),
		}, nil
	}

	// Apply reduce function
	reducedValue := reduceValues(values, check.Reduce)

	// Compare against threshold
	thresholdValue := check.Threshold.Value.AsApproximateFloat64()
	passed := compareThreshold(reducedValue, thresholdValue, check.Threshold.Operator)

	logger.Info("Metric check result",
		"check", check.Name,
		"metric", check.MetricName,
		"values", values,
		"reduced", reducedValue,
		"threshold", thresholdValue,
		"operator", check.Threshold.Operator,
		"passed", passed)

	if !passed {
		return &MetricCheckResult{
			Passed:         false,
			Message:        fmt.Sprintf("Metric %s value %.4f does not satisfy %s %.4f", check.MetricName, reducedValue, check.Threshold.Operator, thresholdValue),
			Values:         values,
			ReducedValue:   reducedValue,
			ThresholdValue: thresholdValue,
		}, nil
	}

	return &MetricCheckResult{
		Passed:         true,
		Message:        fmt.Sprintf("Metric %s value %.4f satisfies %s %.4f", check.MetricName, reducedValue, check.Threshold.Operator, thresholdValue),
		Values:         values,
		ReducedValue:   reducedValue,
		ThresholdValue: thresholdValue,
	}, nil
}

func (m *MetricsChecker) getCustomMetricValues(ctx context.Context, namespace string, check dbupgradev1alpha1.MetricCheck) ([]float64, error) {
	var values []float64

	switch check.Target.Type {
	case dbupgradev1alpha1.MetricTargetTypePods:
		if check.Target.Pods == nil {
			return nil, fmt.Errorf("pods target configuration required for Pods type")
		}

		selector, err := metav1.LabelSelectorAsSelector(&check.Target.Pods.Selector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %w", err)
		}

		// Query custom metrics for pods
		metricList, err := m.customMetricsClient.NamespacedMetrics(namespace).GetForObjects(
			schema.GroupKind{Group: "", Kind: "Pod"},
			selector,
			check.MetricName,
			labels.Everything(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to get custom metrics: %w", err)
		}

		for _, item := range metricList.Items {
			values = append(values, float64(item.Value.MilliValue())/1000.0)
		}

	case dbupgradev1alpha1.MetricTargetTypeObject:
		if check.Target.Object == nil {
			return nil, fmt.Errorf("object target configuration required for Object type")
		}

		ref := check.Target.Object.Ref
		metricValue, err := m.customMetricsClient.NamespacedMetrics(namespace).GetForObject(
			schema.GroupKind{
				Group: extractGroup(ref.APIVersion),
				Kind:  ref.Kind,
			},
			ref.Name,
			check.MetricName,
			labels.Everything(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to get custom metric for object: %w", err)
		}

		values = append(values, float64(metricValue.Value.MilliValue())/1000.0)

	default:
		return nil, fmt.Errorf("unsupported target type %s for custom metrics", check.Target.Type)
	}

	return values, nil
}

func (m *MetricsChecker) getExternalMetricValues(ctx context.Context, namespace string, check dbupgradev1alpha1.MetricCheck) ([]float64, error) {
	var selector labels.Selector
	var err error

	if check.Target.External != nil && check.Target.External.Selector != nil {
		selector, err = metav1.LabelSelectorAsSelector(check.Target.External.Selector)
		if err != nil {
			return nil, fmt.Errorf("invalid external metric selector: %w", err)
		}
	} else {
		selector = labels.Everything()
	}

	metricList, err := m.externalMetricsClient.NamespacedMetrics(namespace).List(
		check.MetricName,
		selector,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get external metrics: %w", err)
	}

	var values []float64
	for _, item := range metricList.Items {
		values = append(values, float64(item.Value.MilliValue())/1000.0)
	}

	return values, nil
}

func reduceValues(values []float64, reduce dbupgradev1alpha1.ReduceFunction) float64 {
	if len(values) == 0 {
		return 0
	}

	switch reduce {
	case dbupgradev1alpha1.ReduceFunctionMin:
		min := values[0]
		for _, v := range values[1:] {
			if v < min {
				min = v
			}
		}
		return min

	case dbupgradev1alpha1.ReduceFunctionSum:
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		return sum

	case dbupgradev1alpha1.ReduceFunctionAvg:
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		return sum / float64(len(values))

	case dbupgradev1alpha1.ReduceFunctionMax, "":
		max := values[0]
		for _, v := range values[1:] {
			if v > max {
				max = v
			}
		}
		return max

	default:
		// Default to max
		max := values[0]
		for _, v := range values[1:] {
			if v > max {
				max = v
			}
		}
		return max
	}
}

func compareThreshold(value, threshold float64, operator dbupgradev1alpha1.ThresholdOperator) bool {
	switch operator {
	case dbupgradev1alpha1.ThresholdOperatorGT:
		return value > threshold
	case dbupgradev1alpha1.ThresholdOperatorGTE:
		return value >= threshold
	case dbupgradev1alpha1.ThresholdOperatorLT:
		return value < threshold
	case dbupgradev1alpha1.ThresholdOperatorLTE:
		return value <= threshold
	default:
		return false
	}
}

// Helper functions
func extractGroup(apiVersion string) string {
	// apiVersion can be "v1" or "group/version"
	if !containsSlash(apiVersion) {
		return ""
	}
	idx := 0
	for i, c := range apiVersion {
		if c == '/' {
			idx = i
			break
		}
	}
	return apiVersion[:idx]
}

func containsSlash(s string) bool {
	for _, c := range s {
		if c == '/' {
			return true
		}
	}
	return false
}

func pluralize(kind string) string {
	// Simple pluralization - in production you'd use proper k8s resource mapping
	lower := toLower(kind)
	if len(lower) > 0 && lower[len(lower)-1] == 's' {
		return lower + "es"
	}
	if len(lower) > 0 && lower[len(lower)-1] == 'y' {
		return lower[:len(lower)-1] + "ies"
	}
	return lower + "s"
}

func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}

// Ensure interfaces are available
var _ meta.RESTMapper = (meta.RESTMapper)(nil)
