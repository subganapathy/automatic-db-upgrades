# Automatic DB Upgrades Operator

Kubernetes operator for automated database upgrades using kubebuilder.

## Milestone 1 Status ✅

**API refinement and validation completed!**

✅ API updated - ServiceAccount configuration removed (operator-managed)
✅ AWS IAM authentication model implemented (operator assumes customer roles)
✅ Validation webhook added with comprehensive validation tests
✅ RBAC permissions configured for Jobs, Secrets, Leases, Events
✅ Sample CRs updated with both self-hosted and AWS examples
✅ Documentation complete for IAM setup and validation rules  

## Project Structure

- `api/v1alpha1/` - DBUpgrade CRD API definitions
- `controllers/` - Controller implementation
- `config/` - Kubernetes manifests (CRD, RBAC, samples)
- `main.go` - Operator entry point

## Building and Running

### Prerequisites

- Go 1.23.x (available locally in `.go-versions/go/` or system installation)
- kubectl
- Access to a Kubernetes cluster

### Using the Local Go 1.23.3

```bash
export PATH="$(pwd)/.go-versions/go/bin:$PATH"
```

### Generate Code and Manifests

```bash
make generate    # Generate DeepCopy code
make manifests   # Generate CRD and RBAC manifests
```

### Build

```bash
make build       # Build the operator binary
```

### Running the Operator

```bash
# Install CRDs
make install

# Run controller locally
make run

# In another terminal, apply the sample
kubectl apply -f config/samples/dbupgrade_v1alpha1_dbupgrade.yaml

# Check status (shows print columns)
kubectl get dbupgrades
# Output columns: NAME, READY, PROGRESSING, DEGRADED, OBSERVEDGEN

# View detailed status
kubectl get dbupgrade dbupgrade-sample -o yaml

# Check conditions (primary status surface)
kubectl get dbupgrade dbupgrade-sample -o jsonpath='{.status.conditions[*]}'
```

## API Version

- Group: `dbupgrade.subbug.learning`
- Version: `v1alpha1`
- Kind: `DBUpgrade`
- CRD: `dbupgrades.dbupgrade.subbug.learning`
- Short name: `dbu` (use `kubectl get dbu` instead of `kubectl get dbupgrades`)

## Generated Files

- CRD: `config/crd/bases/dbupgrade.subbug.learning_dbupgrades.yaml`
- DeepCopy: `api/v1alpha1/zz_generated.deepcopy.go`
- RBAC: `config/rbac/role.yaml`

## Testing

Note: `make test` requires Go 1.25+ for envtest. The core functionality can be verified with:

```bash
go fmt ./...
go vet ./...
make build
```

## Status and Conditions

The `DBUpgrade` status uses Kubernetes-standard conditions to report state:

- **Accepted**: Indicates the spec is valid (`True` with reason `ValidSpec`)
- **Ready**: Indicates the upgrade is complete and ready (`False` initially with reason `Initializing`)
- **Progressing**: Indicates an upgrade is in progress (`False` at rest with reason `Idle`)
- **Degraded**: Indicates the system is in a degraded state (`False` when healthy)
- **Blocked**: Indicates the upgrade is blocked by some condition

Conditions are the primary way to check the status of a `DBUpgrade` resource:

```bash
# View all conditions
kubectl get dbupgrade dbupgrade-sample -o jsonpath='{.status.conditions[*]}'

# Check Ready condition
kubectl get dbupgrade dbupgrade-sample -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
```

The `kubectl get dbupgrades` command shows key conditions in columns for quick status checks.

## Phase 0 Controller Behavior

The controller initializes baseline status:
- Sets `status.observedGeneration = metadata.generation` when spec changes
- Initializes conditions: Accepted=True, Ready=False, Progressing=False, Degraded=False
- Uses patch-based status updates to avoid unnecessary updates
- No Job creation or metrics queries yet (to be implemented in later phases)

### Sample CR

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

See `config/samples/dbupgrade_v1alpha1_dbupgrade.yaml` for complete examples.

## AWS RDS/Aurora Authentication

The DBUpgrade operator handles all AWS IAM authentication.
Migration jobs run as plain pods without IAM roles.

### How it works

1. You specify an IAM `roleArn` in the DBUpgrade spec
2. The operator (which has EKS Pod Identity) assumes your role
3. The operator generates a short-lived RDS IAM auth token (valid 15 minutes)
4. The operator stores the token in a Kubernetes Secret
5. The migration Job reads the Secret to connect to the database

### IAM Setup Required

**Your IAM Role** (`roleArn` in spec):
- Must have `rds-db:connect` permission for your database
- Must have trust policy allowing the operator's IAM role to assume it:
  ```json
  {
    "Effect": "Allow",
    "Principal": {
      "AWS": "arn:aws:iam::PLATFORM_ACCOUNT:role/dbupgrade-operator"
    },
    "Action": "sts:AssumeRole"
  }
  ```

**Operator's IAM Role** (platform-managed):
- Has EKS Pod Identity
- Has permission to assume customer roles:
  ```json
  {
    "Effect": "Allow",
    "Action": "sts:AssumeRole",
    "Resource": "arn:aws:iam::*:role/*-db-migrator"
  }
  ```

### AWS Example

```yaml
apiVersion: dbupgrade.subbug.learning/v1alpha1
kind: DBUpgrade
metadata:
  name: myapp-upgrade
spec:
  migrations:
    image: "myapp/migrations:v2.0.0"
  database:
    type: "awsRds"
    aws:
      roleArn: "arn:aws:iam::123456789012:role/myapp-db-migrator"
      region: "us-east-1"
      host: "mydb.abc123.us-east-1.rds.amazonaws.com"
      port: 5432
      dbName: "myapp"
      username: "migrator"
  runner:
    activeDeadlineSeconds: 900
```

## Validation

The DBUpgrade webhook validates:
- Database type matches configuration (e.g., `type=awsRds` requires `aws` or `connection`)
- AWS configuration has all required fields when specified
- Metric checks have matching target types (e.g., `type=Pods` requires `target.pods`)
- Threshold values are valid Kubernetes Quantities

### Validation Examples

```yaml
# ❌ Invalid - awsRds without AWS config or connection secret
spec:
  database:
    type: awsRds

# ✅ Valid - awsRds with AWS config
spec:
  database:
    type: awsRds
    aws:
      roleArn: arn:aws:iam::123:role/migrator
      region: us-east-1
      host: db.amazonaws.com
      dbName: mydb
      username: migrator

# ❌ Invalid - selfHosted without connection secret
spec:
  database:
    type: selfHosted

# ✅ Valid - selfHosted with connection secret
spec:
  database:
    type: selfHosted
    connection:
      urlSecretRef:
        name: db-secret
        key: url
```
