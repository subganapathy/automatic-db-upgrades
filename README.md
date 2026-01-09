# DBUpgrade Operator

A production-ready Kubernetes operator for automated database schema migrations using [Atlas](https://atlasgo.io/).

## Features

- **Automated Schema Migrations**: Extract migrations from container images and apply using Atlas
- **AWS RDS/Aurora Support**: Built-in IAM authentication for AWS managed databases
- **Pre/Post Checks**: Validate pod versions and metrics before/after migrations
- **Safety Guards**: Blocks spec changes during active migrations
- **Observability**: Prometheus metrics, events, and detailed status conditions

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

Block migrations until all pods are running the required version:

```yaml
spec:
  checks:
    pre:
      minPodVersions:
        - selector:
            matchLabels:
              app: myapp
          minVersion: "2.0.0"
          containerName: myapp  # optional
```

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
```

### 3. Verify Metrics Are Available

```bash
# Check custom metrics API
kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta2/namespaces/default/pods/*/http_errors_per_second"
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

2. **Trust Policy** allowing the operator:
```json
{
  "Effect": "Allow",
  "Principal": {
    "AWS": "arn:aws:iam::PLATFORM_ACCOUNT:role/dbupgrade-operator"
  },
  "Action": "sts:AssumeRole"
}
```

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
