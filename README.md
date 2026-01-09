# DBUpgrade Operator

A production-ready Kubernetes operator for automated database schema migrations using [Atlas](https://atlasgo.io/).

## Features

- **Automated Schema Migrations**: Extract migrations from container images and apply using Atlas
- **AWS RDS/Aurora Support**: Built-in IAM authentication for AWS managed databases
- **Pre/Post Checks**: Validate pod versions and metrics before/after migrations
- **Safety Guards**: Blocks spec changes during active migrations
- **Observability**: Prometheus metrics, events, and detailed status conditions

## Design Decisions

### Architecture: Controller Monitors, Job Executes

The operator follows a clear separation of concerns:

```
┌─────────────────────────────────────────────────────────────────┐
│                         Controller                               │
│  • Watches DBUpgrade CRs                                        │
│  • Runs pre-checks (pod versions, metrics)                      │
│  • Creates/monitors migration Job                               │
│  • Runs post-checks after Job completion                        │
│  • Updates status conditions                                    │
└─────────────────────────────────────────────────────────────────┘
                              │ creates
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                        Migration Job                             │
│  ┌─────────────────┐    ┌─────────────────────────────────────┐ │
│  │  Init Container │    │          Main Container             │ │
│  │  (crane)        │───▶│          (atlas)                    │ │
│  │                 │    │                                     │ │
│  │  Extracts       │    │  Runs migrations against database   │ │
│  │  /migrations    │    │  using extracted SQL files          │ │
│  │  from app image │    │                                     │ │
│  └─────────────────┘    └─────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

**Why this matters:**
- **Blast radius containment**: If a migration fails or hangs, only the Job is affected. The controller continues monitoring other DBUpgrades.
- **Resource isolation**: Migration Jobs can have different resource limits, timeouts (`activeDeadlineSeconds`), and node affinity than the controller.
- **Restart safety**: Controller can restart without affecting running migrations. It rediscovers Job state on startup.
- **Debuggability**: Migration logs are in the Job's pod logs, separate from controller logs. Failed Jobs are preserved for inspection.

### Reliability Patterns

| Decision | Why It Matters |
|----------|----------------|
| **Namespace-scoped resources** | Migration jobs, secrets, and init containers run in the same namespace as the DBUpgrade CR. Simplifies RBAC, keeps resources colocated, and enables namespace-level isolation. |
| **Non-blocking baketime** | Post-migration `bakeSeconds` uses timestamp comparison + requeue, not `time.Sleep()`. The controller remains responsive and can handle other reconciles during the bake period. |
| **Webhook blocks spec changes** | A ValidatingWebhook rejects spec modifications while a migration is running (`Progressing=True`). Prevents mid-flight changes that could cause undefined behavior. |
| **Owner references for cleanup** | Jobs and secrets have `ownerReferences` pointing to the DBUpgrade CR. Kubernetes garbage collection automatically cleans up resources when the CR is deleted. |
| **Idempotent reconciliation** | The controller can be restarted at any point. State is reconstructed from the Job status and CR conditions, not in-memory variables. |
| **Semver strictMode** | Configurable behavior for non-semver image tags. `strictMode: true` (default) fails fast; `strictMode: false` skips non-semver pods gracefully. |

### Security Design

| Decision | Why It Matters |
|----------|----------------|
| **No database credentials in controller** | The controller never sees database passwords. For self-hosted DBs, it references secrets by name. For AWS RDS, it generates short-lived IAM tokens directly into Job secrets. |
| **Minimal RBAC** | Controller only needs: Jobs (create/watch), Secrets (create for migration), Pods (list for version checks), custom metrics API (for metric checks). No cluster-admin required. |
| **Pod security hardening** | Migration Jobs run as non-root with read-only root filesystem. Only the `/migrations` volume is writable. |
| **Secret isolation** | Migration secrets are created in the user's namespace, not the operator namespace. Users can apply NetworkPolicies to restrict access. |
| **Admission validation** | Webhook validates specs at admission time—invalid semver in `minVersion`, missing required fields, and spec changes during migration are rejected before persisting. |
| **No privilege escalation** | The operator cannot grant more database access than the referenced IAM role or secret provides. It's a pass-through, not a privilege boundary. |

### AWS-Specific Security

| Decision | Why It Matters |
|----------|----------------|
| **Shared HTTP connection pool** | A single `ClientManager` with pooled connections (100 max idle, 20 per host) is created at startup. Prevents linear scaling of TCP connections as DBUpgrade count grows. |
| **ExternalID for tenant isolation** | Every `AssumeRole` call includes `ExternalID={namespace}/{name}`. Customer IAM trust policies **must** require this ExternalID, preventing Tenant A from assuming Tenant B's role (confused deputy prevention). |
| **Short-lived IAM tokens** | RDS IAM auth tokens are generated per-migration (15 min validity). No long-lived credentials stored; tokens are created just-in-time in ephemeral secrets. |
| **Role session naming** | STS sessions are named `dbupgrade-operator` for CloudTrail audit trails. Combined with ExternalID, provides full traceability of which DBUpgrade assumed which role. |

## Quick Start

### Installation via Helm

```bash
# Add the Helm repository (OCI)
helm install dbupgrade-operator oci://ghcr.io/subganapathy/automatic-db-upgrades/dbupgrade-operator \
  --namespace dbupgrade-system --create-namespace

# Or with custom values
helm install dbupgrade-operator oci://ghcr.io/subganapathy/automatic-db-upgrades/dbupgrade-operator \
  --namespace dbupgrade-system --create-namespace \
  -f values.yaml
```

### Kustomize Installation

```bash
# Install CRDs and operator
kubectl apply -k config/default
```

## Basic Usage

### Self-Hosted Database

```yaml
apiVersion: dbupgrade.subbug.learning/v1alpha1
kind: DBUpgrade
metadata:
  name: myapp-migration
spec:
  migrations:
    image: myapp:v2.0.0
    dir: /migrations
  database:
    type: selfHosted
    connection:
      urlSecretRef:
        name: db-credentials
        key: url
```

### AWS RDS with IAM Authentication

```yaml
apiVersion: dbupgrade.subbug.learning/v1alpha1
kind: DBUpgrade
metadata:
  name: myapp-rds-migration
spec:
  migrations:
    image: myapp:v2.0.0
    dir: /migrations
  database:
    type: awsRds
    aws:
      roleArn: arn:aws:iam::123456789012:role/myapp-db-migrator
      region: us-east-1
      host: mydb.cluster-abc123.us-east-1.rds.amazonaws.com
      port: 5432
      dbName: myapp
      username: migrator
  runner:
    activeDeadlineSeconds: 900  # 15 min timeout
```

## Pre/Post Migration Checks

### Pod Version Validation

Block migrations until all pods are running the required version.

**Important**: Both `minVersion` and your container image tags must follow [Semantic Versioning](https://semver.org/) (e.g., `1.2.3`, `v2.0.0`, `1.0.0-rc1`). Non-semver tags like `latest`, `alpine`, or `sha256:...` will cause the check to fail by default (see `strictMode`).

```yaml
spec:
  checks:
    pre:
      minPodVersions:
        - selector:
            matchLabels:
              app: myapp
          minVersion: "2.0.0"      # Must be valid semver
          containerName: myapp     # optional, checks all containers if omitted
          strictMode: true         # default: true, fails on non-semver tags
```

| Field | Description |
|-------|-------------|
| `minVersion` | Required semver version (e.g., `1.2.3`, `v2.0.0`) |
| `strictMode` | `true` (default): non-semver pods fail the check. `false`: non-semver pods are skipped |

### Metric Validation

Validate metrics before/after migration (requires prometheus-adapter):

```yaml
spec:
  checks:
    pre:
      metrics:
        - name: error-rate-check
          metricName: http_errors_per_second
          source: Custom  # or External
          target:
            type: Pods
            pods:
              selector:
                matchLabels:
                  app: myapp
          threshold:
            operator: "<"
            value: "0.05"  # error rate < 5%
          reduce: Max
    post:
      metrics:
        - name: latency-check
          metricName: http_request_latency_p99
          source: Custom
          target:
            type: Pods
            pods:
              selector:
                matchLabels:
                  app: myapp
          threshold:
            operator: "<"
            value: "500"  # p99 latency < 500ms
          reduce: Max
          bakeSeconds: 60  # wait 60s before checking
```

## Setting Up prometheus-adapter

For metric-based pre/post checks, you need [prometheus-adapter](https://github.com/kubernetes-sigs/prometheus-adapter) installed:

### 1. Install prometheus-adapter

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm install prometheus-adapter prometheus-community/prometheus-adapter \
  --namespace monitoring \
  -f prometheus-adapter-values.yaml
```

### 2. Configure Custom Metrics

Example `prometheus-adapter-values.yaml`:

```yaml
prometheus:
  url: http://prometheus.monitoring.svc
  port: 9090

rules:
  custom:
    - seriesQuery: 'http_requests_total{namespace!="",pod!=""}'
      resources:
        overrides:
          namespace: {resource: "namespace"}
          pod: {resource: "pod"}
      name:
        matches: "^(.*)_total$"
        as: "${1}_per_second"
      metricsQuery: 'sum(rate(<<.Series>>{<<.LabelMatchers>>}[2m])) by (<<.GroupBy>>)'

    - seriesQuery: 'http_request_duration_seconds_bucket{namespace!="",pod!=""}'
      resources:
        overrides:
          namespace: {resource: "namespace"}
          pod: {resource: "pod"}
      name:
        matches: ".*"
        as: "http_request_latency_p99"
      metricsQuery: 'histogram_quantile(0.99, sum(rate(<<.Series>>{<<.LabelMatchers>>}[5m])) by (le, <<.GroupBy>>))'

  # External metrics (for RDS CloudWatch metrics via YACE or cloudwatch-exporter)
  external:
    - seriesQuery: 'aws_rds_database_connections_average'
      resources: {}
      name:
        matches: "^aws_rds_(.*)$"
        as: "rds_$1"
      metricsQuery: 'avg(<<.Series>>{<<.LabelMatchers>>})'

    - seriesQuery: 'aws_rds_cpuutilization_average'
      resources: {}
      name:
        matches: "^aws_rds_(.*)$"
        as: "rds_$1"
      metricsQuery: 'avg(<<.Series>>{<<.LabelMatchers>>})'
```

### Using RDS CloudWatch Metrics

To use RDS metrics in pre/post checks, you need to export CloudWatch metrics to Prometheus using [YACE (Yet Another CloudWatch Exporter)](https://github.com/nerdswords/yet-another-cloudwatch-exporter) or [cloudwatch-exporter](https://github.com/prometheus/cloudwatch_exporter).

Example YACE configuration for RDS:

```yaml
# yace-config.yaml
discovery:
  jobs:
    - type: rds
      regions: [us-east-1]
      metrics:
        - name: DatabaseConnections
          statistics: [Average, Maximum]
        - name: CPUUtilization
          statistics: [Average, Maximum]
        - name: FreeableMemory
          statistics: [Average, Minimum]
        - name: ReadLatency
          statistics: [Average, Maximum]
        - name: WriteLatency
          statistics: [Average, Maximum]
```

Then use external metrics in your DBUpgrade:

```yaml
spec:
  checks:
    pre:
      metrics:
        - name: rds-cpu-check
          metricName: rds_cpuutilization_average
          source: External
          target:
            type: External
            external:
              selector:
                matchLabels:
                  dbinstance_identifier: mydb
          threshold:
            operator: "<"
            value: "80"  # CPU < 80% before migration
          reduce: Max
    post:
      metrics:
        - name: rds-connections-check
          metricName: rds_database_connections_average
          source: External
          target:
            type: External
            external:
              selector:
                matchLabels:
                  dbinstance_identifier: mydb
          threshold:
            operator: ">"
            value: "0"  # Ensure connections are restored
          reduce: Min
          bakeSeconds: 120  # Wait 2 min after migration
```

### 3. Verify Metrics Are Available

```bash
# Check custom metrics API (pod metrics)
kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta2/namespaces/default/pods/*/http_errors_per_second"

# Check external metrics API (RDS metrics)
kubectl get --raw "/apis/external.metrics.k8s.io/v1beta1/namespaces/default/rds_cpuutilization_average?labelSelector=dbinstance_identifier=mydb"
```

## Status and Conditions

The operator uses two conditions to report state:

| Condition | Meaning |
|-----------|---------|
| `Ready=True` | Migration completed successfully |
| `Ready=False, Progressing=True` | Migration in progress |
| `Ready=False, Progressing=False` | Migration blocked (see Reason) |

### Reason Codes

| Reason | Description |
|--------|-------------|
| `Initializing` | Setting up for migration |
| `MigrationInProgress` | Atlas migration running |
| `MigrationComplete` | Migration succeeded |
| `JobFailed` | Migration job failed |
| `JobPending` | Waiting for job to start |
| `SecretNotFound` | Database credentials not found |
| `PreCheckImageVersionFailed` | Pod version too low |
| `PreCheckMetricFailed` | Metric threshold not met |
| `PostCheckFailed` | Post-migration check failed |

```bash
# Quick status check
kubectl get dbu myapp-migration

# Detailed conditions
kubectl get dbu myapp-migration -o jsonpath='{.status.conditions}'
```

## AWS IAM Setup

### Operator IAM Role

The operator needs permission to assume customer roles:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "sts:AssumeRole",
    "Resource": "arn:aws:iam::*:role/*-db-migrator"
  }]
}
```

Configure via Helm:

```yaml
aws:
  enabled: true
  region: us-east-1
  serviceAccountAnnotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::PLATFORM_ACCOUNT:role/dbupgrade-operator
```

### Customer IAM Role

Your database access role needs:

1. **RDS IAM Authentication**:
```json
{
  "Effect": "Allow",
  "Action": "rds-db:connect",
  "Resource": "arn:aws:rds-db:us-east-1:123456789012:dbuser:cluster-id/migrator"
}
```

2. **Trust Policy with ExternalId** (required for tenant isolation):

The operator passes an `ExternalId` of `{namespace}/{name}` when assuming roles. Your trust policy **must** require this ExternalId to prevent cross-tenant role assumption attacks.

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {
      "AWS": "arn:aws:iam::PLATFORM_ACCOUNT:role/dbupgrade-operator"
    },
    "Action": "sts:AssumeRole",
    "Condition": {
      "StringEquals": {
        "sts:ExternalId": "production/myapp-migration"
      }
    }
  }]
}
```

**Important**: The `ExternalId` is automatically set to `{namespace}/{name}` of the DBUpgrade resource:
- DBUpgrade `production/myapp-migration` → ExternalId `production/myapp-migration`
- DBUpgrade `staging/api-db-upgrade` → ExternalId `staging/api-db-upgrade`

This ensures:
- Tenant A cannot assume Tenant B's role (different ExternalId)
- Roles are scoped to specific DBUpgrade resources
- CloudTrail logs show which DBUpgrade assumed which role

### Connection Pooling

The operator uses a shared HTTP client with connection pooling for AWS API calls:
- HTTP connections are reused across reconciles
- Connection pool: 100 max idle, 20 per host
- No linear scaling of connections with DBUpgrade count

## Atlas Migration Best Practices

### Directory Structure

```
migrations/
  20240101000000_initial.sql
  20240115000000_add_users.sql
  20240201000000_add_orders.sql
  atlas.sum
```

### Linting Rules

Add `.atlas.hcl` to your migrations directory:

```hcl
lint {
  # Prevent destructive changes
  destructive {
    error = true
  }

  # Require explicit naming for constraints
  naming {
    index {
      match = "^idx_[a-z]+_[a-z]+$"
    }
  }

  # Detect data-dependent changes
  data_depend {
    error = true
  }

  # Prevent dropping columns without safety period
  modify_check {
    error = true
  }
}
```

### Dangerous Operations

Atlas detects these dangerous operations:
- `DROP TABLE` / `DROP COLUMN` - Data loss
- `ALTER COLUMN TYPE` - May fail with existing data
- `NOT NULL` without default - Fails on existing rows
- Large table modifications - Potential locks

## Observability

### Prometheus Metrics

```yaml
# Operator heartbeat (absence = operator down)
dbupgrade_operator_up

# controller-runtime standard metrics
controller_runtime_reconcile_total
controller_runtime_reconcile_errors_total
controller_runtime_reconcile_time_seconds_bucket
```

### Example Alerts

```yaml
groups:
- name: dbupgrade
  rules:
  - alert: DBUpgradeOperatorDown
    expr: absent(dbupgrade_operator_up)
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: DBUpgrade operator is down

  - alert: DBUpgradeFailed
    expr: |
      kube_customresource_dbupgrade_status_condition{
        condition="Ready",
        status="false"
      } == 1
      and
      kube_customresource_dbupgrade_status_condition{
        condition="Progressing",
        status="false"
      } == 1
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "DBUpgrade {{ $labels.name }} is stuck"
```

## Configuration

### Helm Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of operator replicas | `1` |
| `image.repository` | Operator image | `ghcr.io/subganapathy/automatic-db-upgrades` |
| `image.tag` | Image tag | Chart appVersion |
| `craneImage` | Crane image for extracting migrations | `gcr.io/go-containerregistry/crane:v0.20.2` |
| `atlasImage` | Atlas CLI image | `arigaio/atlas:latest` |
| `webhook.enabled` | Enable validation webhook | `true` |
| `webhook.certManager.enabled` | Use cert-manager for TLS | `true` |
| `aws.enabled` | Enable AWS IAM authentication | `false` |
| `aws.region` | AWS region | `""` |

### Optional Dependencies

The Helm chart includes optional dependencies that can be enabled as needed:

| Dependency | Purpose | Enable With |
|------------|---------|-------------|
| **cert-manager** | TLS certificates for webhook | `certManager.install=true` (default) |
| **prometheus-adapter** | Metric-based pre/post checks | `prometheusAdapter.enabled=true` |
| **YACE** | Export AWS RDS metrics to Prometheus | `yace.enabled=true` |

#### cert-manager

Installed by default. If you already have cert-manager in your cluster:

```bash
helm install dbupgrade-operator oci://ghcr.io/subganapathy/automatic-db-upgrades/dbupgrade-operator \
  --set certManager.install=false
```

#### prometheus-adapter (for metric checks)

Enable if using `spec.checks.pre.metrics` or `spec.checks.post.metrics`:

```bash
helm install dbupgrade-operator oci://ghcr.io/subganapathy/automatic-db-upgrades/dbupgrade-operator \
  --set prometheusAdapter.enabled=true \
  --set prometheusAdapter.prometheus.url=http://prometheus.monitoring.svc
```

#### YACE (for AWS RDS metrics)

Enable if using RDS CloudWatch metrics in checks:

```bash
helm install dbupgrade-operator oci://ghcr.io/subganapathy/automatic-db-upgrades/dbupgrade-operator \
  --set yace.enabled=true \
  --set yace.aws.role=arn:aws:iam::123456789012:role/cloudwatch-reader
```

#### Full AWS Setup Example

For AWS RDS with IAM auth and CloudWatch metrics:

```bash
helm install dbupgrade-operator oci://ghcr.io/subganapathy/automatic-db-upgrades/dbupgrade-operator \
  --namespace dbupgrade-system --create-namespace \
  --set aws.enabled=true \
  --set aws.region=us-east-1 \
  --set yace.enabled=true \
  --set prometheusAdapter.enabled=true
```

## Development

### Prerequisites

- Go 1.21+
- kubectl
- kind (for local testing)
- Docker

### Local Development

```bash
# Start kind cluster with local registry
./e2e/setup-kind.sh

# Build and deploy
make docker-build IMG=localhost:5000/dbupgrade-operator:dev
docker push localhost:5000/dbupgrade-operator:dev
make deploy IMG=localhost:5000/dbupgrade-operator:dev

# Run E2E tests
./e2e/run-e2e.sh
```

### Running Tests

```bash
# Unit tests
make test

# E2E tests
./e2e/run-e2e.sh
```

## License

Apache License 2.0
