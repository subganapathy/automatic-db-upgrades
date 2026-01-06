# Milestone 2A: Self-Hosted DB with Basic Job Creation

> **Goal**: Get database migrations running end-to-end with self-hosted PostgreSQL databases

---

## Scope

### In Scope ✅
- Read and validate connection Secret exists
- Create Kubernetes Job to run migrations
- Mount Secret as environment variable in Job
- Monitor Job status (Pending, Running, Succeeded, Failed)
- Update DBUpgrade status conditions based on Job state
- Status progression: Accepted → Progressing → Ready
- Event emission for observability
- Handle Job failures gracefully
- Idempotent reconciliation (don't recreate completed Jobs)

### Out of Scope ❌
- Pre-checks (minPodVersions, metrics) → Phase 2B
- Post-checks (metrics) → Phase 2B
- Lease acquisition → Phase 2B
- AWS RDS/Aurora support → Phase 2C
- RDS IAM token generation → Phase 2C

---

## User Story

**As a** platform user
**I want to** automatically run database migrations
**So that** my database schema stays in sync with my application

**Acceptance Criteria**:
1. Create DBUpgrade resource with selfHosted type
2. Operator validates connection Secret exists
3. Operator creates Job to run migrations
4. Job connects to database using Secret
5. Status shows Progressing while Job runs
6. Status shows Ready when Job succeeds
7. Status shows Degraded when Job fails
8. Kubernetes Events emitted for observability

---

## Implementation Tasks

### 1. Secret Validation
**File**: `controllers/dbupgrade_controller.go`

**Add function**:
```go
func (r *DBUpgradeReconciler) validateSecret(ctx context.Context, dbUpgrade *DBUpgrade) error
```

**Logic**:
- Check if `database.type == selfHosted`
- Extract Secret name and key from `database.connection.urlSecretRef`
- Use `k8sClient.Get()` to fetch Secret from same namespace
- Verify Secret exists
- Verify key exists in Secret.Data
- If missing: Set Degraded condition with reason "SecretNotFound"
- Return error to requeue

**Tests**:
- Unit test: validateSecret with existing Secret
- Unit test: validateSecret with missing Secret
- envtest: Create DBUpgrade, verify Degraded when Secret missing
- envtest: Create Secret, verify validation passes

---

### 2. Job Creation
**File**: `controllers/dbupgrade_controller.go`

**Add function**:
```go
func (r *DBUpgradeReconciler) createMigrationJob(ctx context.Context, dbUpgrade *DBUpgrade) (*batchv1.Job, error)
```

**Job Specification**:
```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: dbupgrade-{dbupgrade-name}-{hash}
  namespace: {dbupgrade-namespace}
  ownerReferences:
    - apiVersion: dbupgrade.subbug.learning/v1alpha1
      kind: DBUpgrade
      name: {dbupgrade-name}
      uid: {dbupgrade-uid}
      controller: true
      blockOwnerDeletion: true
spec:
  backoffLimit: 0  # No retries (migrations should be idempotent)
  activeDeadlineSeconds: {from runner.activeDeadlineSeconds or default 600}
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: migrate
        image: {migrations.image}
        command: ["sh", "-c"]
        args:
          - |
            # Atlas migrate apply
            atlas migrate apply \
              --dir "file://{migrations.dir}" \
              --url "$DATABASE_URL"
        env:
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: {database.connection.urlSecretRef.name}
              key: {database.connection.urlSecretRef.key}
```

**Key Design Decisions**:
- **backoffLimit: 0**: Migrations should be idempotent, no automatic retries
- **OwnerReference**: Job is owned by DBUpgrade (auto-deletion on cascade)
- **Hash in name**: Use first 8 chars of hash(spec) to detect spec changes
- **Secret as env**: Mount connection URL via environment variable

**Logic**:
- Check if Job already exists for this DBUpgrade
- If exists and succeeded: do nothing (idempotent)
- If exists and failed: check if spec changed (compare hash)
  - If spec changed: delete old Job, create new one
  - If spec same: do nothing, keep Degraded status
- If doesn't exist: create new Job
- Set owner reference for garbage collection

**Tests**:
- Unit test: Job spec generation from DBUpgrade
- envtest: Create DBUpgrade, verify Job created
- envtest: Job has correct owner reference
- envtest: Secret mounted as env var
- envtest: backoffLimit is 0

---

### 3. Job Status Monitoring
**File**: `controllers/dbupgrade_controller.go`

**Add function**:
```go
func (r *DBUpgradeReconciler) syncJobStatus(ctx context.Context, dbUpgrade *DBUpgrade, job *batchv1.Job) error
```

**Job Status Mapping**:
```
Job Status              → DBUpgrade Conditions
================================================================================
No Job exists           → Ready=False/Initializing, Progressing=False/Idle
Job Pending/Running     → Ready=False/JobRunning, Progressing=True/MigrationInProgress
Job Succeeded           → Ready=True/MigrationComplete, Progressing=False/Idle
Job Failed              → Ready=False/JobFailed, Progressing=False/Idle, Degraded=True/MigrationFailed
```

**Logic**:
- Get Job associated with DBUpgrade (by owner reference query)
- Check Job.Status.Conditions
- Map Job status to DBUpgrade conditions
- Update observedGeneration
- Emit Kubernetes Event for state transitions

**Tests**:
- Unit test: syncJobStatus with succeeded Job
- Unit test: syncJobStatus with failed Job
- envtest: Job succeeds → Ready=True
- envtest: Job fails → Degraded=True

---

### 4. Event Emission
**File**: `controllers/dbupgrade_controller.go`

**Add function**:
```go
func (r *DBUpgradeReconciler) recordEvent(ctx context.Context, dbUpgrade *DBUpgrade, eventType, reason, message string)
```

**Events to Emit**:
| Event | Type | Reason | Message | When |
|-------|------|--------|---------|------|
| SecretNotFound | Warning | SecretMissing | "Connection secret {name} not found" | Secret validation fails |
| MigrationStarted | Normal | JobCreated | "Created migration Job {job-name}" | Job created |
| MigrationInProgress | Normal | JobRunning | "Migration Job is running" | Job status = Running |
| MigrationSucceeded | Normal | JobSucceeded | "Migration completed successfully" | Job status = Succeeded |
| MigrationFailed | Warning | JobFailed | "Migration failed: {reason}" | Job status = Failed |

**Logic**:
- Use `k8sClient.Create()` with Event object
- Event should reference DBUpgrade in `involvedObject`
- Include namespace, name, UID

**Tests**:
- envtest: Verify Event created when Job starts
- envtest: Verify Event created when Job succeeds
- envtest: Verify Event created when Job fails

---

### 5. Reconciliation Flow
**File**: `controllers/dbupgrade_controller.go`

**Update Reconcile() function**:
```go
func (r *DBUpgradeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. Fetch DBUpgrade
    dbUpgrade := &DBUpgrade{}
    if err := r.Get(ctx, req.NamespacedName, dbUpgrade); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 2. Initialize status if needed (existing logic)
    if specChanged || missingAccepted {
        r.initializeStatus(ctx, dbUpgrade)
    }

    // 3. Only proceed for selfHosted (skip AWS for now)
    if dbUpgrade.Spec.Database.Type != DatabaseTypeSelfHosted {
        // Set Blocked condition: "AWS support not yet implemented"
        return ctrl.Result{}, nil
    }

    // 4. Validate Secret exists
    if err := r.validateSecret(ctx, dbUpgrade); err != nil {
        // Set Degraded condition
        r.recordEvent(ctx, dbUpgrade, "Warning", "SecretNotFound", err.Error())
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }

    // 5. Check if Job already exists
    job, err := r.getJobForDBUpgrade(ctx, dbUpgrade)
    if err != nil {
        return ctrl.Result{}, err
    }

    // 6. Create Job if doesn't exist
    if job == nil {
        job, err = r.createMigrationJob(ctx, dbUpgrade)
        if err != nil {
            return ctrl.Result{}, err
        }
        r.recordEvent(ctx, dbUpgrade, "Normal", "MigrationStarted", fmt.Sprintf("Created Job %s", job.Name))
    }

    // 7. Sync Job status to DBUpgrade status
    if err := r.syncJobStatus(ctx, dbUpgrade, job); err != nil {
        return ctrl.Result{}, err
    }

    // 8. Requeue if Job is still running
    if isJobRunning(job) {
        return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
    }

    return ctrl.Result{}, nil
}
```

**Requeue Strategy**:
- Secret missing: Requeue after 30s (waiting for Secret)
- Job running: Requeue after 10s (polling Job status)
- Job succeeded: No requeue (done)
- Job failed: No requeue (manual intervention needed)

---

### 6. Helper Functions

**File**: `controllers/dbupgrade_controller.go`

**Add helpers**:
```go
// getJobForDBUpgrade finds the Job owned by this DBUpgrade
func (r *DBUpgradeReconciler) getJobForDBUpgrade(ctx context.Context, dbUpgrade *DBUpgrade) (*batchv1.Job, error)

// computeSpecHash generates a hash of the spec for change detection
func computeSpecHash(spec DBUpgradeSpec) string

// isJobRunning checks if Job is in running state
func isJobRunning(job *batchv1.Job) bool

// isJobSucceeded checks if Job completed successfully
func isJobSucceeded(job *batchv1.Job) bool

// isJobFailed checks if Job failed
func isJobFailed(job *batchv1.Job) bool
```

---

## Testing Strategy

### Unit Tests
**File**: `controllers/dbupgrade_controller_test.go` (new file)

1. Test Secret validation logic
2. Test Job spec generation
3. Test Job status mapping to conditions
4. Test spec hash computation

### Integration Tests (envtest)
**File**: `controllers/dbupgrade_controller_envtest_test.go` (new file)

**Test scenarios**:
1. **Happy path**:
   - Create Secret with connection URL
   - Create DBUpgrade (selfHosted)
   - Verify Job created
   - Simulate Job success (manually set Job status)
   - Verify Ready=True

2. **Secret missing**:
   - Create DBUpgrade without Secret
   - Verify Degraded=True with SecretNotFound reason
   - Create Secret
   - Verify reconciliation retries and creates Job

3. **Job failure**:
   - Create Secret and DBUpgrade
   - Simulate Job failure
   - Verify Degraded=True with MigrationFailed reason

4. **Spec change after success**:
   - Create successful migration
   - Change migrations.image
   - Verify new Job created (spec hash changed)

5. **Idempotency**:
   - Create successful migration
   - Trigger reconciliation again
   - Verify no duplicate Jobs created

### Manual Testing with Docker
**Setup**:
```bash
# 1. Start PostgreSQL in Docker
docker run --name test-postgres \
  -e POSTGRES_PASSWORD=testpass \
  -e POSTGRES_DB=testdb \
  -p 5432:5432 \
  -d postgres:15

# 2. Create connection Secret
kubectl create secret generic db-connection \
  --from-literal=url="postgres://postgres:testpass@host.docker.internal:5432/testdb?sslmode=disable"

# 3. Create sample migrations
# migrations/001_create_users.sql:
CREATE TABLE users (
  id SERIAL PRIMARY KEY,
  email VARCHAR(255) NOT NULL
);

# 4. Build migration image
docker build -t test-migrations:v1 .

# 5. Create DBUpgrade
kubectl apply -f - <<EOF
apiVersion: dbupgrade.subbug.learning/v1alpha1
kind: DBUpgrade
metadata:
  name: test-migration
spec:
  migrations:
    image: test-migrations:v1
    dir: /migrations
  database:
    type: selfHosted
    connection:
      urlSecretRef:
        name: db-connection
        key: url
EOF

# 6. Watch status
kubectl get dbu test-migration -o yaml -w

# 7. Check Job
kubectl get jobs

# 8. Check Events
kubectl describe dbu test-migration
```

---

## Acceptance Criteria

- [ ] Operator validates Secret exists before creating Job
- [ ] Job created with correct spec (image, env, ownerRef)
- [ ] Job mounts Secret as DATABASE_URL env var
- [ ] Status shows Progressing while Job runs
- [ ] Status shows Ready=True when Job succeeds
- [ ] Status shows Degraded=True when Job fails
- [ ] Events emitted for state transitions
- [ ] Idempotent: No duplicate Jobs for same spec
- [ ] Spec changes trigger new Job creation
- [ ] All unit tests pass
- [ ] All envtest integration tests pass
- [ ] Manual testing with Docker PostgreSQL succeeds

---

## Estimated Effort

- Secret validation: 1 hour
- Job creation logic: 2 hours
- Job status monitoring: 2 hours
- Event emission: 1 hour
- Reconciliation flow: 2 hours
- Unit tests: 2 hours
- envtest integration tests: 3 hours
- Manual testing: 1 hour
- Documentation: 1 hour

**Total: ~15 hours** (2-3 work days)

---

## Dependencies

- No new external dependencies needed
- Uses existing controller-runtime client
- Uses batch/v1.Job (standard Kubernetes)
- Uses core/v1.Event (standard Kubernetes)

---

## Success Metrics

After Phase 2A completion:
- Users can run database migrations with selfHosted databases
- Operator handles failures gracefully
- Status accurately reflects migration state
- Events provide visibility into migration progress
- Tests provide confidence for future changes

---

## Next Steps After 2A

**Phase 2B**: Add pre-checks and post-checks
- MinPodVersions validation
- Metrics queries
- Lease acquisition

**Phase 2C**: Add AWS RDS/Aurora support
- AWS SDK integration
- IAM role assumption
- RDS token generation
