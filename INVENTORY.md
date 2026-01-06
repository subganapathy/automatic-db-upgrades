# DBUpgrade Operator - Resource Inventory

> Complete inventory of implemented components as of Milestone 1 completion

---

## 1. API Types (`api/v1alpha1/`)

### Files Overview
- **conditions.go** (50 lines): Condition types and helper functions
- **dbupgrade_types.go** (386 lines): Core CRD types and spec/status
- **dbupgrade_webhook.go** (271 lines): Validation webhook implementation
- **groupversion_info.go** (20 lines): API group registration
- **zz_generated.deepcopy.go** (449 lines): Auto-generated DeepCopy methods

### API Version
- **Group**: `dbupgrade.subbug.learning`
- **Version**: `v1alpha1`
- **Kind**: `DBUpgrade`
- **Plural**: `dbupgrades`
- **Short name**: `dbu`

### DBUpgradeSpec Structure

```go
type DBUpgradeSpec struct {
    Migrations MigrationsSpec  // Container image and directory
    Database   DatabaseSpec    // Connection and type config
    Checks     *ChecksSpec     // Pre/post upgrade validation (optional)
    Runner     *RunnerSpec     // Job configuration (optional)
}
```

#### Migrations Configuration
- `image`: Container image with migration tools (required)
- `dir`: Directory containing migrations (default: `/migrations`)

#### Database Configuration
- **Type**: `selfHosted`, `awsRds`, or `awsAurora`
- **For selfHosted**:
  - `connection.urlSecretRef`: K8s Secret with connection URL
- **For awsRds/awsAurora**:
  - Option 1: AWS config (roleArn, region, host, port, dbName, username)
  - Option 2: connection.urlSecretRef (for custom connection strings)

#### Checks Configuration (Optional)
- **Pre-checks**:
  - `minPodVersions`: Ensure pods are at minimum version before upgrade
  - `metrics`: Custom metric checks (Pods, Object, or External)
- **Post-checks**:
  - `metrics`: Custom metric checks to verify upgrade success

#### Runner Configuration (Optional)
- `activeDeadlineSeconds`: Job timeout (can be > 15min even with RDS tokens)

### DBUpgradeStatus Structure

```go
type DBUpgradeStatus struct {
    ObservedGeneration int64              // Tracks spec changes
    Conditions         []metav1.Condition // Standard K8s conditions
}
```

#### Conditions (Kubernetes-standard)
- **Accepted** (True/False): Spec is valid
- **Ready** (True/False): Upgrade complete and ready
- **Progressing** (True/False): Upgrade in progress
- **Degraded** (True/False): System degraded
- **Blocked** (True/False): Upgrade blocked (e.g., pre-check failing)

### Field-Level Validation (OpenAPI)
- AWS roleArn pattern validation (IAM ARN format)
- Required field markers on all critical fields
- Default values (e.g., port=5432, dir="/migrations")
- Enum validation for database types and metric target types

### Immutability Rules
**Enforced by webhook ValidateUpdate:**

Immutable fields (cannot change after creation):
- `database.type`
- `database.connection.urlSecretRef`
- `database.aws.*` (all AWS config fields)

Mutable fields (can update):
- `migrations.image` - Deploy new migration versions
- `migrations.dir` - Change migration location
- `checks.*` - Modify validation checks
- `runner.activeDeadlineSeconds` - Adjust timeouts

---

## 2. Validation Webhook (`api/v1alpha1/dbupgrade_webhook.go`)

### Implementation: 271 lines

### Webhook Methods
```go
ValidateCreate() (admission.Warnings, error)
ValidateUpdate(old runtime.Object) (admission.Warnings, error)
ValidateDelete() (admission.Warnings, error)
```

### Validation Logic

#### Database Validation (`validateDatabase`)
- **selfHosted**: Requires `connection.urlSecretRef`
- **awsRds/awsAurora**: Requires either `aws` config OR `connection.urlSecretRef`
- **AWS config validation**: All required fields (roleArn, region, host, dbName, username)
- **Cross-field validation**: Type must match configuration

#### Metric Validation (`validateMetrics`)
- **Pods target**: Requires `target.pods` with selector
- **Object target**: Requires `target.object` with object reference
- **External target**: `target.external` is optional
- **Threshold validation**: Ensures value is not zero/empty

#### Immutability Validation (`validateImmutableFields`)
- Checks all database.* fields
- Prevents type changes
- Prevents connection/AWS config changes
- Allows migrations.* and checks.* changes

### Design Decision
- **Secret existence NOT validated**: Deferred to controller runtime validation
- **Rationale**: Avoids webhook latency and additional RBAC requirements

---

## 3. Controller (`controllers/dbupgrade_controller.go`)

### Current Status: Phase 0 Implementation (196 lines)

### What's Implemented
- Skeleton reconciler structure
- Status initialization on spec changes
- Baseline condition setting (Accepted, Ready, Progressing, Degraded, Blocked)
- ObservedGeneration tracking
- Patch-based status updates (avoids unnecessary writes)

### RBAC Permissions Configured
```yaml
# DBUpgrade resources
- dbupgrades: get, list, watch, create, update, patch, delete
- dbupgrades/status: get, update, patch
- dbupgrades/finalizers: update

# For Milestone 2 implementation:
- jobs: get, list, watch, create, update, patch, delete
- jobs/status: get
- secrets: get, list, watch, create, update, patch, delete
- leases: get, list, watch, create, update, patch, delete
- events: create, patch
- pods: get, list, watch (for pre-checks)
- services: get, list, watch (for metric checks)
- custom.metrics.k8s.io/*: get, list
- external.metrics.k8s.io/*: get, list
```

### Reconcile Logic (Current)
1. Fetch DBUpgrade resource
2. Check if spec changed (generation vs observedGeneration)
3. Initialize/reset conditions
4. Update status via patch

### What's NOT Implemented Yet (Milestone 2)
- ❌ Job creation for migrations
- ❌ Secret management (RDS tokens, connection strings)
- ❌ Pre-check execution
- ❌ Post-check execution
- ❌ Lease acquisition
- ❌ Event emission
- ❌ Status progression (Ready → Progressing → Ready)

---

## 4. Testing Infrastructure

### Test Statistics
- **Total tests**: 25 (all passing ✅)
- **Unit tests**: 12
- **Integration tests (envtest)**: 13

### Unit Tests (`dbupgrade_webhook_test.go` - 315 lines)

**Database Validation (4 tests)**:
- Accept selfHosted with connection
- Reject selfHosted without connection
- Accept awsRds with AWS config
- Reject awsRds without config

**Metric Validation (3 tests)**:
- Accept Pod metric with pods target
- Reject Pod metric without pods target
- Reject invalid threshold quantity

**Immutability Validation (5 tests)**:
- Reject changing database.type
- Reject changing database.connection.urlSecretRef
- Reject changing database.aws.roleArn
- Allow changing migrations.image
- Allow changing migrations.dir

### Integration Tests (`dbupgrade_webhook_envtest_test.go` - 351 lines)

**Create Validation via API (5 tests)**:
- Accept valid selfHosted DBUpgrade
- Reject selfHosted without connection
- Accept valid awsRds DBUpgrade
- Reject awsRds without AWS config or connection
- Reject awsRds with incomplete AWS config

**Update Validation - Immutability (4 tests)**:
- Reject changing database.type
- Reject changing database.connection.urlSecretRef
- Allow changing migrations.image
- Allow changing migrations.dir

**Update Validation - AWS Immutability (4 tests)**:
- Reject changing database.aws.roleArn
- Reject changing database.aws.region
- Reject changing database.aws.host
- Reject changing database.aws.dbName

### Test Infrastructure (`suite_test.go` - 132 lines)
- envtest environment setup
- Kubernetes API server + etcd bootstrapping
- Webhook server with TLS certificates
- Manager startup and graceful shutdown
- BeforeSuite/AfterSuite hooks
- Timeout handling for webhook readiness

### Running Tests
```bash
# Install envtest binaries (one-time)
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
setup-envtest use 1.29.0

# Run all tests
KUBEBUILDER_ASSETS="$(setup-envtest use 1.29.0 -p path)" go test ./api/v1alpha1 -v

# Results: 25 Passed | 0 Failed
```

---

## 5. Generated Kubernetes Manifests (`config/`)

### CRD (`config/crd/bases/dbupgrade.subbug.learning_dbupgrades.yaml`)
- Full OpenAPI schema with validation
- Print columns: NAME, READY, PROGRESSING, DEGRADED, OBSERVEDGEN
- Short name: `dbu`
- Subresources: status

### RBAC (`config/rbac/`)
- **role.yaml**: ClusterRole with all permissions
- **role_binding.yaml**: ClusterRoleBinding
- **service_account.yaml**: ServiceAccount for operator

### Webhook (`config/webhook/manifests.yaml`)
- ValidatingWebhookConfiguration
- Webhook path: `/validate-dbupgrade-subbug-learning-v1alpha1-dbupgrade`
- Operations: CREATE, UPDATE
- Failure policy: Fail
- Side effects: None

### Samples (`config/samples/dbupgrade_v1alpha1_dbupgrade.yaml`)

**Example 1: Self-Hosted Database**
```yaml
apiVersion: dbupgrade.subbug.learning/v1alpha1
kind: DBUpgrade
metadata:
  name: dbupgrade-sample
spec:
  migrations:
    image: "postgres:15-migrations"
    dir: "/migrations"
  database:
    type: "selfHosted"
    connection:
      urlSecretRef:
        name: "db-connection-secret"
        key: "url"
```

**Example 2: AWS RDS with IAM Authentication**
```yaml
apiVersion: dbupgrade.subbug.learning/v1alpha1
kind: DBUpgrade
metadata:
  name: dbupgrade-aws-example
spec:
  migrations:
    image: myapp/migrations:v2.0.0
    dir: /migrations
  database:
    type: awsRds
    aws:
      roleArn: arn:aws:iam::123456789012:role/myapp-db-migrator
      region: us-east-1
      host: mydb.abc123.us-east-1.rds.amazonaws.com
      port: 5432
      dbName: myapp
      username: migrator
  checks:
    pre:
      minPodVersions:
      - selector:
          matchLabels:
            app: myapp
        minVersion: "v1.5.0"
  runner:
    activeDeadlineSeconds: 900
```

---

## 6. Observability & Metrics (`internal/metrics/`)

### Heartbeat Metric (`metrics.go` - 30 lines)

```go
dbupgrade_operator_up{} = 1
```

**Purpose**: Indicates operator process is alive
**Type**: Gauge (always 1 when running)
**Exported at**: `http://localhost:8080/metrics`
**Alert on**: `absent(dbupgrade_operator_up)` = process is dead

### Health Probes (`main.go`)
- **Liveness**: `http://localhost:8081/healthz`
- **Readiness**: `http://localhost:8081/readyz`

### Standard controller-runtime Metrics
- Reconciliation duration
- Reconciliation errors
- Queue depth
- Work queue latency

---

## 7. Main Entry Point (`main.go`)

### Responsibilities (131 lines)
1. Initialize scheme with DBUpgrade types
2. Parse command-line flags
3. Create controller manager with:
   - Metrics server (port 8080)
   - Webhook server (port 9443)
   - Health probes (port 8081)
   - Leader election (optional)
4. Register DBUpgradeReconciler
5. Register webhook with manager
6. Set operator heartbeat metric
7. Start manager with signal handling

### Configuration
- **Metrics bind address**: `:8080` (Prometheus)
- **Webhook bind address**: `:9443` (HTTPS)
- **Health probe bind address**: `:8081` (K8s liveness/readiness)
- **Leader election ID**: `dbupgrade-controller-manager.subbug.learning`

---

## 8. Documentation

### README.md (318 lines)
- Project overview
- Milestone 1 status
- Building and running instructions
- API version details
- Status and conditions explanation
- Phase 0 controller behavior
- Sample CRs
- AWS RDS/Aurora authentication model
- IAM setup requirements
- Validation rules and examples
- Immutability documentation
- Observability section (health checks, metrics, monitoring)

---

## 9. What's Ready for Milestone 2

### ✅ Complete Foundation
1. **API fully defined**: All types, validation, immutability
2. **Webhook validated**: 13 integration tests + 12 unit tests
3. **RBAC configured**: All permissions for Jobs, Secrets, Leases, Events
4. **Observability ready**: Metrics, health checks documented
5. **Testing infrastructure**: envtest setup for controller testing
6. **Documentation complete**: README, validation rules, IAM model

### 🎯 Milestone 2 Scope

**Controller Implementation** will add:

1. **Job Management**:
   - Create migration Job based on spec
   - Monitor Job status
   - Handle Job failures
   - Clean up completed Jobs

2. **Secret Management**:
   - Generate RDS IAM tokens (AWS SDK)
   - Create K8s Secrets with connection info
   - Handle Secret rotation (15-min token expiry)
   - Validate Secret existence for selfHosted

3. **Pre-Check Execution**:
   - Validate min pod versions
   - Query custom metrics (Pods, Object, External)
   - Set Blocked condition if checks fail
   - Prevent migration if blocked

4. **Post-Check Execution**:
   - Query metrics after migration
   - Set Degraded if checks fail
   - Allow rollback detection

5. **Status Management**:
   - Progress through states: Accepted → Progressing → Ready
   - Update conditions based on Job status
   - Emit Kubernetes Events for observability
   - Handle error states gracefully

6. **Lease Management**:
   - Acquire lease before creating Job
   - Ensure single-writer guarantee
   - Release lease after Job completes

### 📊 Current Code Statistics

```
Total Go files: 13
Total lines: ~2,400 (including tests)

Breakdown:
- API types: 456 lines (conditions.go + dbupgrade_types.go)
- Webhook: 271 lines
- Controller: 196 lines (Phase 0 skeleton)
- Metrics: 30 lines
- Main: 131 lines
- Tests: 798 lines (unit + envtest + suite)
- Generated: 449 lines (DeepCopy)
```

### 🔧 Tools & Dependencies

**Required**:
- Go 1.23.x
- kubectl
- Kubernetes cluster (or envtest for testing)

**Development**:
- controller-runtime v0.17.2
- kubebuilder markers (for code generation)
- setup-envtest (for integration tests)
- Ginkgo/Gomega (testing framework)

### 📈 Test Coverage
- **api/v1alpha1**: 26.5% (webhook validation focused)
- **Unit tests**: Fast (< 1ms per test)
- **envtest tests**: Moderate (< 100ms per test)
- **All tests**: Pass in ~7 seconds

---

## 10. Git Repository Status

### Recent Commits (Last 5)
```
13d9e4c test(webhook): add envtest integration tests
c63385b feat(webhook): add immutability validation and heartbeat metric
dbc9fb3 refactor(api): simplify to operator-managed IAM, add validation and RBAC
a82b5e6 fix(controller): reset Ready condition when spec changes
3caee24 fix(api): clarify MetricCheck.Name requirements
```

### Branch: `main`
- All Milestone 1 work merged
- Clean working tree
- Ready for Milestone 2 branch

---

## Summary: Resource Checklist

- ✅ API types complete with validation
- ✅ Webhook validation with immutability enforcement
- ✅ 25 tests (unit + integration) all passing
- ✅ CRD, RBAC, webhook manifests generated
- ✅ Sample CRs for selfHosted and AWS
- ✅ Observability (metrics, health checks)
- ✅ Documentation (README with examples)
- ✅ Testing infrastructure (envtest setup)
- ✅ Controller skeleton (Phase 0)
- 🎯 Ready for Milestone 2: Controller implementation

**Next**: Implement reconciliation logic to create Jobs, manage Secrets, execute checks, and update status.
