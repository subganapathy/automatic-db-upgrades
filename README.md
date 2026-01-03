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

# Check status
kubectl get dbupgrade dbupgrade-sample -o yaml
```

## API Version

- Group: `dbupgrade.subbug.learning`
- Version: `v1alpha1`
- Kind: `DBUpgrade`
- CRD: `dbupgrades.dbupgrade.subbug.learning`

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

## Phase 0 Controller Behavior

The controller currently only updates the status with conditions:
- Sets `status.observedGeneration = metadata.generation`
- Updates conditions: Ready, Progressing, Blocked, Degraded
- No Job creation or metrics queries yet (to be implemented in later phases)
