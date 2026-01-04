# Automatic DB Upgrades Operator

Kubernetes operator for automated database upgrades using kubebuilder.

## Phase 0 Status ✅

**All steps completed successfully!**

✅ Project scaffolded with kubebuilder  
✅ Complete DBUpgrade CRD API with all required fields  
✅ Minimal reconciler that updates status/conditions  
✅ RBAC configured with TODO comments for future permissions  
✅ Sample manifests created  
✅ DeepCopy code generated  
✅ CRD manifest generated  
✅ Go 1.23.3 downloaded and available  
✅ `make generate` and `make manifests` working  

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

See `config/samples/dbupgrade_v1alpha1_dbupgrade.yaml` for a complete example.
