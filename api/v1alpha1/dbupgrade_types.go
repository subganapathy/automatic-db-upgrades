package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DBUpgradeSpec defines the desired state of DBUpgrade
type DBUpgradeSpec struct {
	// Migrations configuration
	Migrations MigrationsSpec `json:"migrations"`

	// Database configuration
	Database DatabaseSpec `json:"database"`

	// Pre and post upgrade checks
	// +optional
	Checks *ChecksSpec `json:"checks,omitempty"`

	// Runner configuration
	// +optional
	Runner *RunnerSpec `json:"runner,omitempty"`
}

// MigrationsSpec defines the migration configuration
type MigrationsSpec struct {
	// Image is the container image to run migrations
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Dir is the directory containing migration files
	// +kubebuilder:default="/migrations"
	// +optional
	Dir string `json:"dir,omitempty"`
}

// DatabaseSpec defines the database configuration
type DatabaseSpec struct {
	// Type of database
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=selfHosted;awsRds;awsAurora
	Type DatabaseType `json:"type"`

	// Connection configuration
	// +optional
	Connection *ConnectionSpec `json:"connection,omitempty"`

	// AWS-specific configuration (placeholders for future use)
	// +optional
	AWS *AWSSpec `json:"aws,omitempty"`
}

// DatabaseType represents the type of database
// +kubebuilder:validation:Enum=selfHosted;awsRds;awsAurora
type DatabaseType string

const (
	DatabaseTypeSelfHosted DatabaseType = "selfHosted"
	DatabaseTypeAWSRDS     DatabaseType = "awsRds"
	DatabaseTypeAWSAurora  DatabaseType = "awsAurora"
)

// ConnectionSpec defines database connection details
type ConnectionSpec struct {
	// URLSecretRef references a secret containing the database URL
	// +optional
	URLSecretRef *corev1.SecretKeySelector `json:"urlSecretRef,omitempty"`
}

// AWSSpec defines AWS-specific database configuration
// The operator (not the migration job) assumes this role to generate RDS tokens
type AWSSpec struct {
	// RoleArn is the IAM role that the operator will assume to generate RDS auth tokens
	// This role must:
	// - Have trust policy allowing the operator's IAM role (via AssumeRole)
	// - Have rds-db:connect permission for the database
	// The operator has EKS Pod Identity and can assume this role
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^arn:aws:iam::\d{12}:role\/[\w+=,.@-]+$`
	RoleArn string `json:"roleArn"`

	// Region is the AWS region
	// +kubebuilder:validation:Required
	Region string `json:"region"`

	// Host is the database endpoint
	// +kubebuilder:validation:Required
	Host string `json:"host"`

	// Port is the database port
	// +kubebuilder:default=5432
	// +optional
	Port int32 `json:"port,omitempty"`

	// DBName is the database name
	// +kubebuilder:validation:Required
	DBName string `json:"dbName"`

	// Username for database access (must be an RDS IAM user)
	// +kubebuilder:validation:Required
	Username string `json:"username"`
}

// ChecksSpec defines pre and post upgrade checks
type ChecksSpec struct {
	// Pre-upgrade checks
	// +optional
	Pre PreChecksSpec `json:"pre,omitempty"`

	// Post-upgrade checks
	// +optional
	Post PostChecksSpec `json:"post,omitempty"`
}

// PreChecksSpec defines pre-upgrade checks
type PreChecksSpec struct {
	// Minimum pod versions to check
	// +optional
	MinPodVersions []MinPodVersionCheck `json:"minPodVersions,omitempty"`

	// Metrics to check (list-as-map keyed by name for GitOps-friendly edits)
	// +listType=map
	// +listMapKey=name
	// +optional
	Metrics []MetricCheck `json:"metrics,omitempty"`
}

// PostChecksSpec defines post-upgrade checks
type PostChecksSpec struct {
	// Metrics to check (list-as-map keyed by name for GitOps-friendly edits)
	// +listType=map
	// +listMapKey=name
	// +optional
	Metrics []MetricCheck `json:"metrics,omitempty"`
}

// MinPodVersionCheck defines a minimum pod version check
type MinPodVersionCheck struct {
	// Selector to select pods to check
	// +kubebuilder:validation:Required
	Selector metav1.LabelSelector `json:"selector"`

	// MinVersion is the minimum required version (ImageTag-only semver)
	// +kubebuilder:validation:Required
	MinVersion string `json:"minVersion"`

	// ContainerName is the name of the container to check (optional)
	// +optional
	ContainerName string `json:"containerName,omitempty"`

	// StrictMode controls behavior when pods have non-semver image tags.
	// When true (default): non-semver pods cause check failure.
	// When false: non-semver pods are skipped (not counted as pass or fail).
	// +kubebuilder:default=true
	// +optional
	StrictMode *bool `json:"strictMode,omitempty"`

	// DisallowDowngrade prevents downgrades
	// +kubebuilder:default=false
	// +optional
	DisallowDowngrade bool `json:"disallowDowngrade,omitempty"`
}

// MetricCheck defines a metric check
type MetricCheck struct {
	// Name is required and must be unique (list-as-map semantics).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Source of the metric
	// +kubebuilder:validation:Enum=Custom;External
	// +kubebuilder:default=Custom
	// +optional
	Source MetricSource `json:"source,omitempty"`

	// MetricName is the name of the metric
	// +kubebuilder:validation:Required
	MetricName string `json:"metricName"`

	// Target defines what to query for the metric
	// +kubebuilder:validation:Required
	Target MetricTarget `json:"target"`

	// Threshold defines the threshold condition
	// +kubebuilder:validation:Required
	Threshold ThresholdSpec `json:"threshold"`

	// Reduce function to apply to multiple values
	// +kubebuilder:validation:Enum=Max;Avg;Sum;Min
	// +kubebuilder:default=Max
	// +optional
	Reduce ReduceFunction `json:"reduce,omitempty"`

	// BakeSeconds is the time to wait before evaluating
	// +kubebuilder:default=0
	BakeSeconds int32 `json:"bakeSeconds,omitempty"`

	// IntervalSeconds is the interval between metric queries
	// +kubebuilder:default=15
	IntervalSeconds int32 `json:"intervalSeconds,omitempty"`
}

// MetricSource represents the source of a metric
// +kubebuilder:validation:Enum=Custom;External
type MetricSource string

const (
	MetricSourceCustom   MetricSource = "Custom"
	MetricSourceExternal MetricSource = "External"
)

// MetricTarget defines what to query for a metric
// NOTE: Cross-field validation (e.g., type=Pods requires Pods to be set) must be enforced
// in controller validation logic in Phase 1, as CRD schema alone cannot easily enforce this.
type MetricTarget struct {
	// Type of target
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Pods;Object;External
	Type MetricTargetType `json:"type"`

	// Pods target configuration (required when type=Pods)
	// +optional
	Pods *PodsTarget `json:"pods,omitempty"`

	// Object target configuration (required when type=Object)
	// +optional
	Object *ObjectTarget `json:"object,omitempty"`

	// External target configuration (optional when type=External)
	// +optional
	External *ExternalTarget `json:"external,omitempty"`
}

// MetricTargetType represents the type of metric target
// +kubebuilder:validation:Enum=Pods;Object;External
type MetricTargetType string

const (
	MetricTargetTypePods     MetricTargetType = "Pods"
	MetricTargetTypeObject   MetricTargetType = "Object"
	MetricTargetTypeExternal MetricTargetType = "External"
)

// PodsTarget defines pod-based metric target
type PodsTarget struct {
	// Selector to select pods
	// +kubebuilder:validation:Required
	Selector metav1.LabelSelector `json:"selector"`
}

// ObjectTarget defines object-based metric target
type ObjectTarget struct {
	// Reference to the object
	// +kubebuilder:validation:Required
	Ref ObjectReference `json:"ref"`

	// Selector for sub-resources (optional)
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// ObjectReference references a Kubernetes object
type ObjectReference struct {
	// APIVersion of the object
	// +kubebuilder:validation:Required
	APIVersion string `json:"apiVersion"`

	// Kind of the object
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// Name of the object
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// ExternalTarget defines external metric target
type ExternalTarget struct {
	// Selector (optional)
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// ThresholdSpec defines a threshold condition
type ThresholdSpec struct {
	// Operator for comparison
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=">";">=";"<";"<="
	Operator ThresholdOperator `json:"operator"`

	// Value to compare against (resource.Quantity format as decimal string, e.g., "5", "1.5", "250m", "0.05" for 5%).
	// Note: Use decimal fractions for percentages (e.g., "0.05" for 5%), not percentage notation.
	// In Phase 1 controller logic, use Quantity.AsApproximateFloat64() or string parsing consistently
	// for both metric values and threshold comparisons.
	// +kubebuilder:validation:Required
	Value resource.Quantity `json:"value"`
}

// ThresholdOperator represents a comparison operator
// +kubebuilder:validation:Enum=">";">=";"<";"<="
type ThresholdOperator string

const (
	ThresholdOperatorGT  ThresholdOperator = ">"
	ThresholdOperatorGTE ThresholdOperator = ">="
	ThresholdOperatorLT  ThresholdOperator = "<"
	ThresholdOperatorLTE ThresholdOperator = "<="
)

// ReduceFunction represents a reduction function
// +kubebuilder:validation:Enum=Max;Avg;Sum;Min
type ReduceFunction string

const (
	ReduceFunctionMax ReduceFunction = "Max"
	ReduceFunctionAvg ReduceFunction = "Avg"
	ReduceFunctionSum ReduceFunction = "Sum"
	ReduceFunctionMin ReduceFunction = "Min"
)

// RunnerSpec defines runner configuration
type RunnerSpec struct {
	// ActiveDeadlineSeconds for the runner job
	// Can be > 15 minutes even with RDS IAM auth - tokens only expire for
	// establishing connections, not for keeping them open
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`
}

// DBUpgradeStatus defines the observed state of DBUpgrade
type DBUpgradeStatus struct {
	// ObservedGeneration reflects the generation of the most recently observed DBUpgrade
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobCompletedAt records when the migration job completed successfully.
	// Used for baketime calculation in post-checks.
	// +optional
	JobCompletedAt *metav1.Time `json:"jobCompletedAt,omitempty"`

	// Conditions represent the latest available observations of DBUpgrade's state
	// +listType=map
	// +listMapKey=type
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// DBUpgradeConditionType represents a condition type
type DBUpgradeConditionType string

const (
	// ConditionReady indicates the migration has completed successfully for the current spec.
	// Ready=True means the database schema matches the desired state.
	ConditionReady DBUpgradeConditionType = "Ready"

	// ConditionProgressing indicates a migration is currently in progress.
	// When Progressing=False and Ready=False, check the Reason for why:
	// - Initializing: No migration started yet
	// - JobFailed: Migration job failed
	// - PreCheckImageVersionFailed: Image version precheck failed
	// - PreCheckMetricFailed: Metric precheck failed
	// - PostCheckFailed: Post-migration check failed
	// - SecretNotFound: Database connection secret not found
	// - AWSNotSupported: AWS RDS/Aurora not yet implemented
	ConditionProgressing DBUpgradeConditionType = "Progressing"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=dbu
//+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description="Migration completed successfully"
//+kubebuilder:printcolumn:name="Progressing",type=string,JSONPath=".status.conditions[?(@.type==\"Progressing\")].status",description="Migration in progress"
//+kubebuilder:printcolumn:name="Reason",type=string,JSONPath=".status.conditions[?(@.type==\"Progressing\")].reason",description="Current state reason"
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// DBUpgrade is the Schema for the dbupgrades API
type DBUpgrade struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DBUpgradeSpec   `json:"spec"`
	Status DBUpgradeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DBUpgradeList contains a list of DBUpgrade
type DBUpgradeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBUpgrade `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DBUpgrade{}, &DBUpgradeList{})
}
