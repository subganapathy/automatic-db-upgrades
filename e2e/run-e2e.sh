#!/bin/bash
# E2E test script for DBUpgrade operator
# Tests self-hosted PostgreSQL migration flow on a kind cluster

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Cluster name
CLUSTER_NAME="dbupgrade-e2e"

# Registry settings
REGISTRY_NAME="kind-registry"
REGISTRY_PORT="5001"

# Image names (using local registry)
OPERATOR_IMG="localhost:${REGISTRY_PORT}/dbupgrade-operator:e2e"
MIGRATIONS_IMG="localhost:${REGISTRY_PORT}/sample-migrations:e2e"
CRANE_TAR_IMG="localhost:${REGISTRY_PORT}/crane-tar:e2e"

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

cleanup() {
    log_info "Cleaning up..."
    kind delete cluster --name "${CLUSTER_NAME}" 2>/dev/null || true
    docker rm -f "${REGISTRY_NAME}" 2>/dev/null || true
}

# Create local registry for kind
create_registry() {
    log_info "Creating local registry..."

    # Check if registry is already running
    if docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null | grep -q 'true'; then
        log_info "Registry already running"
        return
    fi

    # Start local registry
    docker run -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" --name "${REGISTRY_NAME}" registry:2

    # Connect registry to kind network
    if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${REGISTRY_NAME}")" = 'null' ]; then
        docker network connect "kind" "${REGISTRY_NAME}" 2>/dev/null || true
    fi

    log_info "Local registry created at localhost:${REGISTRY_PORT}"
}

# Trap for cleanup on exit
trap cleanup EXIT

# Check required tools
check_prerequisites() {
    log_info "Checking prerequisites..."

    local missing=()

    command -v kind >/dev/null 2>&1 || missing+=("kind")
    command -v kubectl >/dev/null 2>&1 || missing+=("kubectl")
    command -v docker >/dev/null 2>&1 || missing+=("docker")

    if [ ${#missing[@]} -ne 0 ]; then
        log_error "Missing required tools: ${missing[*]}"
        log_error "Please install them before running E2E tests"
        exit 1
    fi

    log_info "All prerequisites found"
}

# Create kind cluster
create_cluster() {
    log_info "Creating kind cluster '${CLUSTER_NAME}'..."

    # Delete existing cluster if present
    kind delete cluster --name "${CLUSTER_NAME}" 2>/dev/null || true

    kind create cluster --name "${CLUSTER_NAME}" --config "${SCRIPT_DIR}/kind-config.yaml"

    log_info "Waiting for cluster to be ready..."
    kubectl wait --for=condition=Ready nodes --all --timeout=60s

    # Connect registry to kind network (if registry exists)
    if docker inspect "${REGISTRY_NAME}" >/dev/null 2>&1; then
        docker network connect "kind" "${REGISTRY_NAME}" 2>/dev/null || true
    fi

    # Document the local registry
    # https://github.com/kubernetes/enhancements/tree/master/keps/sig-cluster-lifecycle/generic/1755-communicating-a-local-registry
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${REGISTRY_PORT}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF

    log_info "Kind cluster created successfully"
}

# Deploy PostgreSQL
deploy_postgres() {
    log_info "Deploying PostgreSQL..."

    kubectl apply -f "${SCRIPT_DIR}/postgres/deployment.yaml"

    log_info "Waiting for PostgreSQL to be ready..."
    kubectl wait --for=condition=Available deployment/postgres -n postgres --timeout=120s

    # Wait for pod to be ready
    kubectl wait --for=condition=Ready pod -l app=postgres -n postgres --timeout=60s

    log_info "PostgreSQL deployed successfully"
}

# Build and push custom crane-tar image (pinned version, minimal)
build_crane_tar_image() {
    log_info "Building crane-tar image (pinned crane version)..."

    local INTERNAL_IMG="kind-registry:5000/crane-tar:e2e"
    docker build --provenance=false -t "${INTERNAL_IMG}" "${PROJECT_ROOT}/images/crane-tar"

    # Tag for localhost access (for pushing)
    docker tag "${INTERNAL_IMG}" "${CRANE_TAR_IMG}"

    log_info "Pushing crane-tar image to local registry..."
    docker push "${CRANE_TAR_IMG}"

    log_info "crane-tar image pushed successfully"
}

# Build and push sample migrations image to local registry
build_migrations_image() {
    log_info "Building sample migrations image..."

    # Docker buildx may add provenance/attestation manifests which create multi-manifest images
    # The operator handles this via --platform flag in crane export (dynamic arch detection)
    local INTERNAL_IMG="kind-registry:5000/sample-migrations:e2e"
    docker build -t "${INTERNAL_IMG}" "${SCRIPT_DIR}/sample-migrations"

    # Tag for localhost access (for pushing)
    docker tag "${INTERNAL_IMG}" "${MIGRATIONS_IMG}"

    log_info "Pushing migrations image to local registry..."
    docker push "${MIGRATIONS_IMG}"

    log_info "Migrations image pushed successfully"
}

# Build and deploy the operator
deploy_operator() {
    log_info "Building operator image..."

    cd "${PROJECT_ROOT}"

    # Install kustomize if not present
    make kustomize

    # Build the operator image
    make docker-build IMG="${OPERATOR_IMG}"

    log_info "Pushing operator image to local registry..."
    docker push "${OPERATOR_IMG}"

    log_info "Installing CRDs..."
    make install

    log_info "Deploying operator (E2E mode - webhooks disabled)..."
    # Use E2E kustomization which disables webhooks
    cd config/manager && ${PROJECT_ROOT}/bin/kustomize edit set image controller=${OPERATOR_IMG}
    ${PROJECT_ROOT}/bin/kustomize build ${PROJECT_ROOT}/config/e2e | kubectl apply -f -

    log_info "Waiting for operator to be ready..."
    kubectl wait --for=condition=Available deployment/automatic-db-upgrades-controller-manager -n system --timeout=120s

    log_info "Operator deployed successfully"
}

# Create connection secret and DBUpgrade CR
create_test_resources() {
    log_info "Creating test resources..."

    # Create connection secret
    kubectl apply -f "${SCRIPT_DIR}/postgres/connection-secret.yaml"

    # Create DBUpgrade CR
    kubectl apply -f "${SCRIPT_DIR}/dbupgrade-test.yaml"

    log_info "Test resources created"
}

# Wait for migration job to complete
wait_for_migration() {
    log_info "Waiting for migration to complete..."

    local timeout=120
    local interval=5
    local elapsed=0

    while [ $elapsed -lt $timeout ]; do
        # Get the DBUpgrade Ready condition status
        # The status uses conditions pattern: Ready=True means success, Ready=False with Degraded=True means failure
        local ready=$(kubectl get dbupgrade e2e-test-migration -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "Unknown")
        local degraded=$(kubectl get dbupgrade e2e-test-migration -o jsonpath='{.status.conditions[?(@.type=="Degraded")].status}' 2>/dev/null || echo "Unknown")

        log_info "Current status: Ready=${ready}, Degraded=${degraded}"

        if [ "$ready" == "True" ]; then
            log_info "Migration completed successfully!"
            return 0
        fi

        if [ "$degraded" == "True" ]; then
            log_error "Migration failed!"
            kubectl get dbupgrade e2e-test-migration -o yaml
            kubectl logs -l job-name -n default --tail=50 || true
            return 1
        fi

        sleep $interval
        elapsed=$((elapsed + interval))
    done

    log_error "Timeout waiting for migration to complete"
    kubectl get dbupgrade e2e-test-migration -o yaml
    kubectl get jobs -n default
    kubectl get pods -n default
    return 1
}

# Verify the database schema was applied
verify_schema() {
    log_info "Verifying database schema..."

    # Get a postgres pod to run psql
    local pg_pod=$(kubectl get pods -n postgres -l app=postgres -o jsonpath='{.items[0].metadata.name}')

    # Check if users table exists
    log_info "Checking for 'users' table..."
    local users_exists=$(kubectl exec -n postgres "${pg_pod}" -- psql -U dbupgrade -d testdb -t -c "SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'users');" | tr -d ' ')

    if [ "$users_exists" != "t" ]; then
        log_error "Table 'users' not found!"
        return 1
    fi
    log_info "Table 'users' exists"

    # Check if posts table exists
    log_info "Checking for 'posts' table..."
    local posts_exists=$(kubectl exec -n postgres "${pg_pod}" -- psql -U dbupgrade -d testdb -t -c "SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'posts');" | tr -d ' ')

    if [ "$posts_exists" != "t" ]; then
        log_error "Table 'posts' not found!"
        return 1
    fi
    log_info "Table 'posts' exists"

    # List all tables for verification
    log_info "Listing all tables in database:"
    kubectl exec -n postgres "${pg_pod}" -- psql -U dbupgrade -d testdb -c "\\dt"

    # Check columns in users table
    log_info "Checking 'users' table structure:"
    kubectl exec -n postgres "${pg_pod}" -- psql -U dbupgrade -d testdb -c "\\d users"

    log_info "Schema verification passed!"
    return 0
}

# Print test summary
print_summary() {
    local status=$1

    echo ""
    echo "=============================================="
    if [ $status -eq 0 ]; then
        echo -e "${GREEN}E2E TEST PASSED${NC}"
    else
        echo -e "${RED}E2E TEST FAILED${NC}"
    fi
    echo "=============================================="
    echo ""
}

# Debug function to show resources
debug_resources() {
    log_warn "Debugging: Current resources"
    echo ""
    echo "--- DBUpgrade ---"
    kubectl get dbupgrade -o wide || true
    echo ""
    echo "--- Jobs ---"
    kubectl get jobs -n default || true
    echo ""
    echo "--- Pods ---"
    kubectl get pods -n default || true
    echo ""
    echo "--- Operator logs ---"
    kubectl logs deployment/automatic-db-upgrades-controller-manager -n system --tail=30 || true
}

# Main test flow
main() {
    log_info "Starting E2E tests for DBUpgrade operator"
    echo ""

    check_prerequisites
    create_registry
    create_cluster
    deploy_postgres
    build_crane_tar_image
    build_migrations_image
    deploy_operator
    create_test_resources

    if wait_for_migration; then
        if verify_schema; then
            print_summary 0
            exit 0
        fi
    fi

    debug_resources
    print_summary 1
    exit 1
}

# Parse arguments
case "${1:-}" in
    --no-cleanup)
        trap - EXIT
        main
        ;;
    --debug)
        set -x
        main
        ;;
    *)
        main
        ;;
esac
