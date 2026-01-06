# DBUpgrade Operator - Resource Inventory

> Complete inventory of implemented components as of Milestone 2A completion

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
- `image`: Container image with `/migrations` directory (required)
- `dir`: Directory containing migrations (default: `/migrations`)

**Customer Contract**: Provide an image with a `/migrations` directory containing Atlas migration files. No tools/shell required (distroless compatible).

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

### Current Status: Milestone 2A Complete (550+ lines)

### Architecture: Init Container + Atlas CLI Pattern

```
┌─────────────────────────────────────────────────────────────────┐
│                       Migration Job                              │
├─────────────────────────────────────────────────────────────────┤
│  Init Container (crane)          │  Main Container (Atlas CLI)  │
│  ┌─────────────────────────────┐ │  ┌──────────────────────────┐│
│  │ crane export <customer-img> │ │  │ atlas migrate apply      ││
│  │   | tar -xf - /migrations   │ │  │   --dir file:///migrations│
│  │                             │ │  │   --url $DATABASE_URL    ││
│  └──────────────┬──────────────┘ │  └─────────────┬────────────┘│
│                 │                │                │              │
│                 └────────────────┼────────────────┘              │
│                    emptyDir volume (shared)                      │
└─────────────────────────────────────────────────────────────────┘
```

**Key Design Decisions**:
- **crane**: Extracts `/migrations` from customer image without running it (distroless compatible)
- **Atlas CLI**: Standardized migration tooling with linting, versioning, safe upgrades
- **Operator-managed Secret**: Unified codepath for self-hosted and future RDS support

### Container Images Used
```go
const (
    CraneImage = "gcr.io/go-containerregistry/crane:latest"
    AtlasImage = "arigaio/atlas:latest"
)
```

### What's Implemented (Milestone 2A)

#### Secret Validation (`validateSecret`)
- Validates customer's connection Secret exists
- Verifies required key exists in Secret.Data
- Sets Degraded condition if missing
- Emits SecretNotFound event

#### Unified Secret Creation (`ensureMigrationSecret`)
- Creates operator-managed Secret from customer's Secret
- Secret naming: `dbupgrade-{name}-connection`
- OwnerReference for garbage collection
- Prepares for RDS token generation (Phase 2C)

#### Job Creation (`createMigrationJob`)
- Init container: Uses crane to extract migrations
- Main container: Uses Atlas CLI to run migrations
- Shared emptyDir volume for migrations
- DATABASE_URL from operator-managed Secret
- backoffLimit: 0 (no retries - migrations are idempotent)
- Spec hash in Job name for change detection

#### Job Status Monitoring (`syncJobStatus`)
- Maps Job status to DBUpgrade conditions:
  - No Job → Ready=False/Initializing
  - Job Running → Progressing=True/MigrationInProgress
  - Job Succeeded → Ready=True/MigrationComplete
  - Job Failed → Degraded=True/MigrationFailed

#### Event Emission (`recordEvent`)
- SecretNotFound (Warning)
- MigrationStarted (Normal)
- MigrationSucceeded (Normal)
- MigrationFailed (Warning)

#### Helper Functions
- `getJobForDBUpgrade`: Finds Job by owner reference
- `computeSpecHash`: SHA256 hash for change detection
- `isJobRunning`, `isJobSucceeded`, `isJobFailed`: Status helpers

### Reconcile Flow
1. Fetch DBUpgrade resource
2. Initialize status if needed
3. Skip non-selfHosted (AWS blocked until Phase 2C)
4. Validate customer's Secret exists
5. Ensure operator-managed Secret
6. Check if Job already exists
7. Create Job if doesn't exist
8. Sync Job status to conditions
9. Requeue if Job still running (10s)

### RBAC Permissions Configured
```yaml
# DBUpgrade resources
- dbupgrades: get, list, watch, create, update, patch, delete
- dbupgrades/status: get, update, patch
- dbupgrades/finalizers: update

# For Milestone 2A implementation:
- jobs: get, list, watch, create, update, patch, delete
- jobs/status: get
- secrets: get, list, watch, create, update, patch, delete
- events: create, patch

# For future phases:
- leases: get, list, watch, create, update, patch, delete
- pods: get, list, watch (for pre-checks)
- services: get, list, watch (for metric checks)
- custom.metrics.k8s.io/*: get, list
- external.metrics.k8s.io/*: get, list
```

### What's NOT Implemented Yet
- ❌ Pre-check execution (Phase 2B)
- ❌ Post-check execution (Phase 2B)
- ❌ Lease acquisition (Phase 2B)
- ❌ AWS RDS/Aurora support (Phase 2C)
- ❌ RDS IAM token generation (Phase 2C)

---

## 4. Testing Infrastructure

### Test Statistics
- **Total tests**: 36 (all passing ✅)
- **Webhook unit tests**: 12
- **Webhook integration tests (envtest)**: 13
- **Controller unit tests**: 4
- **Controller integration tests (envtest)**: 7

### Controller Unit Tests (`controllers/dbupgrade_controller_test.go`)

**Helper Function Tests (4 tests)**:
- `TestComputeSpecHash`: Hash consistency and uniqueness
- `TestIsJobRunning`: Nil job, active pods, no active pods
- `TestIsJobSucceeded`: Nil job, succeeded, not succeeded, no conditions
- `TestIsJobFailed`: Nil job, failed, not failed, no conditions

### Controller Integration Tests (`controllers/dbupgrade_controller_envtest_test.go`)

**Secret Validation (2 tests)**:
- Should set Degraded when Secret is missing
- Should create Job when Secret exists

**Job Creation (2 tests)**:
- Should create Job with init container + Atlas pattern
- Should create operator-managed Secret

**Job Status Synchronization (2 tests)**:
- Should update Ready condition when Job succeeds
- Should set Degraded when Job fails

**Idempotency (1 test)**:
- Should not create duplicate Jobs for same spec

### Webhook Unit Tests (`api/v1alpha1/dbupgrade_webhook_test.go`)

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

### Webhook Integration Tests (`api/v1alpha1/dbupgrade_webhook_envtest_test.go`)

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

### Test Infrastructure Files
- `api/v1alpha1/suite_test.go` (132 lines): Webhook envtest setup
- `controllers/suite_test.go` (106 lines): Controller envtest setup

### Running Tests
```bash
# Install envtest binaries (one-time)
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
setup-envtest use 1.29.0

# Run all tests
KUBEBUILDER_ASSETS="$(setup-envtest use 1.29.0 -p path)" go test ./... -v

# Results: 36 Passed | 0 Failed
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
    image: "myapp/migrations:v1"  # Just needs /migrations directory
    dir: "/migrations"
  database:
    type: "selfHosted"
    connection:
      urlSecretRef:
        name: "db-connection-secret"
        key: "url"
```

**Example 2: AWS RDS with IAM Authentication** (Phase 2C)
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
- Milestone status
- Building and running instructions
- API version details
- Status and conditions explanation
- Sample CRs
- AWS RDS/Aurora authentication model
- IAM setup requirements
- Validation rules and examples
- Immutability documentation
- Observability section (health checks, metrics, monitoring)

### MILESTONE_2A_PLAN.md (456 lines)
- Detailed implementation plan for Phase 2A
- User story and acceptance criteria
- Implementation tasks with code snippets
- Testing strategy
- Manual testing guide

---

## 9. Milestone Progress

### ✅ Milestone 1 Complete
1. **API fully defined**: All types, validation, immutability
2. **Webhook validated**: 13 integration tests + 12 unit tests
3. **RBAC configured**: All permissions for Jobs, Secrets, Leases, Events
4. **Observability ready**: Metrics, health checks documented
5. **Testing infrastructure**: envtest setup for testing
6. **Documentation complete**: README, validation rules, IAM model

### ✅ Milestone 2A Complete (Self-Hosted DB)
1. **Secret validation**: Validates customer's connection Secret
2. **Unified Secret creation**: Operator-managed Secret for Job
3. **Job creation**: Init container + Atlas CLI pattern
4. **Job status monitoring**: Syncs Job status to conditions
5. **Event emission**: Kubernetes Events for observability
6. **Idempotent reconciliation**: No duplicate Jobs
7. **Controller tests**: 4 unit + 7 envtest integration tests

### 🎯 Milestone 2B (Next): Checks Implementation
- Pre-checks (minPodVersions, metrics)
- Post-checks (metrics)
- Lease acquisition for single-writer guarantee

### 🎯 Milestone 2C: AWS RDS/Aurora Support
- AWS SDK integration
- RDS IAM token generation
- Role assumption

---

## 10. Code Statistics

```
Total Go files: 17
Total lines: ~3,800 (including tests)

Breakdown:
- API types: 456 lines (conditions.go + dbupgrade_types.go)
- Webhook: 271 lines
- Controller: 550+ lines (Milestone 2A complete)
- Metrics: 30 lines
- Main: 131 lines
- Controller tests: 480 lines (unit + envtest + suite)
- Webhook tests: 798 lines (unit + envtest + suite)
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
- **Total tests**: 36 (all passing)
- **Unit tests**: Fast (< 1ms per test)
- **envtest tests**: Moderate (< 100ms per test)
- **All tests**: Pass in ~17 seconds

---

## 11. Git Repository Status

### Recent Commits
```
dcc7124 feat(controller): implement Milestone 2A - self-hosted DB migration support
213dde9 docs: add Milestone 2A implementation plan for self-hosted DB support
ff29038 docs: add comprehensive resource inventory for Milestone 1
```

### Branch: `main`
- Milestone 1 complete
- Milestone 2A complete
- Ready for E2E testing and Phase 2B

---

## Summary: Resource Checklist

### Milestone 1
- ✅ API types complete with validation
- ✅ Webhook validation with immutability enforcement
- ✅ 25 webhook tests (unit + integration) all passing
- ✅ CRD, RBAC, webhook manifests generated
- ✅ Sample CRs for selfHosted and AWS
- ✅ Observability (metrics, health checks)
- ✅ Documentation (README with examples)
- ✅ Testing infrastructure (envtest setup)

### Milestone 2A
- ✅ Secret validation for customer connection Secret
- ✅ Operator-managed Secret creation (unified codepath)
- ✅ Job creation with init container + Atlas CLI pattern
- ✅ Job status monitoring and condition sync
- ✅ Event emission for observability
- ✅ Idempotent reconciliation
- ✅ 11 controller tests (4 unit + 7 envtest)
- ✅ Controller watches Jobs for automatic reconciliation

### Next Steps
- 🎯 E2E testing with kind + PostgreSQL
- 🎯 Phase 2B: Pre-checks and post-checks
- 🎯 Phase 2C: AWS RDS/Aurora support
