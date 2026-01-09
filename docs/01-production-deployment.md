# Production Deployment Guide

This document explains how a Kubernetes operator is deployed to production, using this DBUpgrade operator as a concrete example. We'll cover what pieces exist, how they connect, what's missing for production, and how upgrades work.

---

## Table of Contents

1. [Anatomy of an Operator Deployment](#1-anatomy-of-an-operator-deployment)
2. [Current State: What We Have](#2-current-state-what-we-have)
3. [What's Missing for Production](#3-whats-missing-for-production)
4. [Golden Production Configuration](#4-golden-production-configuration)
5. [Deployment Flow](#5-deployment-flow)
6. [Upgrade Workflow](#6-upgrade-workflow)
7. [Key Takeaways](#7-key-takeaways)

---

## 1. Anatomy of an Operator Deployment

A Kubernetes operator consists of several interconnected pieces:

```
┌─────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                       │
│                                                                   │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                    Custom Resource Definition (CRD)        │   │
│  │                                                            │   │
│  │  Extends Kubernetes API with new resource type:           │   │
│  │  - apiVersion: dbupgrade.subbug.learning/v1alpha1         │   │
│  │  - kind: DBUpgrade                                         │   │
│  └──────────────────────────────────────────────────────────┘   │
│                              │                                    │
│                              │ watches                            │
│                              ▼                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                    Controller (Deployment)                 │   │
│  │                                                            │   │
│  │  - Runs reconciliation loop                                │   │
│  │  - Creates Jobs for migrations                             │   │
│  │  - Updates status conditions                               │   │
│  │  - Needs RBAC permissions                                  │   │
│  └──────────────────────────────────────────────────────────┘   │
│                              │                                    │
│         ┌────────────────────┼────────────────────┐              │
│         │                    │                    │              │
│         ▼                    ▼                    ▼              │
│  ┌────────────┐     ┌────────────────┐    ┌─────────────┐       │
│  │    RBAC    │     │    Webhook     │    │   Secrets   │       │
│  │            │     │                │    │             │       │
│  │ - ClusterRole    │ - Validates CRs│    │ - DB creds  │       │
│  │ - Binding  │     │ - Needs TLS    │    │ - RDS tokens│       │
│  │ - ServiceAccount │ - cert-manager │    │             │       │
│  └────────────┘     └────────────────┘    └─────────────┘       │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

### Component Breakdown

| Component | Purpose | Files in This Repo |
|-----------|---------|-------------------|
| **CRD** | Defines `DBUpgrade` resource schema | `config/crd/bases/dbupgrade.subbug.learning_dbupgrades.yaml` |
| **Controller** | Runs reconciliation logic | `config/manager/manager.yaml`, `controllers/dbupgrade_controller.go` |
| **RBAC** | Grants controller permissions | `config/rbac/role.yaml`, `config/rbac/role_binding.yaml` |
| **Webhook** | Validates CRs before admission | `config/webhook/manifests.yaml`, `api/v1alpha1/dbupgrade_webhook.go` |
| **ServiceAccount** | Identity for controller pod | `config/rbac/service_account.yaml` |

---

## 2. Current State: What We Have

### 2.1 Directory Structure

```
config/
├── crd/
│   └── bases/
│       └── dbupgrade.subbug.learning_dbupgrades.yaml  # Generated CRD
├── rbac/
│   ├── role.yaml              # ClusterRole with permissions
│   ├── role_binding.yaml      # Binds role to service account
│   └── service_account.yaml   # Controller's identity
├── manager/
│   ├── manager.yaml           # Controller Deployment
│   └── kustomization.yaml     # Image configuration
├── webhook/
│   └── manifests.yaml         # ValidatingWebhookConfiguration
├── default/
│   └── kustomization.yaml     # Production kustomization
└── e2e/
    ├── kustomization.yaml     # E2E test overlay
    └── e2e-env-patch.yaml     # Disables webhooks for testing
```

### 2.2 The Controller Deployment

**File:** `config/manager/manager.yaml`

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
  namespace: system
spec:
  replicas: 1                    # Single replica (HA needs more)
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest  # Replaced by kustomize
        args:
        - --leader-elect          # Enables leader election for HA
        ports:
        - containerPort: 9443     # Webhook server
        - containerPort: 8080     # Metrics
        - containerPort: 8081     # Health probes
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8081
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8081
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop: ["ALL"]
```

**Key observations:**
- Single replica with leader election (ready for HA, but not configured)
- Three ports: webhook (9443), metrics (8080), health (8081)
- Security hardened: non-root, no privilege escalation, capabilities dropped

### 2.3 RBAC Permissions

**File:** `config/rbac/role.yaml`

The controller needs permissions to:

```yaml
# Core operations
- apiGroups: ["dbupgrade.subbug.learning"]
  resources: ["dbupgrades", "dbupgrades/status", "dbupgrades/finalizers"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

# Create and monitor migration Jobs
- apiGroups: ["batch"]
  resources: ["jobs", "jobs/status"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

# Read/create secrets (DB credentials, RDS tokens)
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

# Emit events for observability
- apiGroups: [""]
  resources: ["events"]
  verbs: ["create", "patch"]

# Leader election
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

### 2.4 The Webhook

**File:** `config/webhook/manifests.yaml`

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: validating-webhook-configuration
webhooks:
- name: vdbupgrade.kb.io
  clientConfig:
    service:
      name: webhook-service       # Must exist!
      namespace: system
      path: /validate-dbupgrade-subbug-learning-v1alpha1-dbupgrade
  rules:
  - operations: ["CREATE", "UPDATE"]
    resources: ["dbupgrades"]
  failurePolicy: Fail             # Reject if webhook unavailable
  sideEffects: None
```

**What the webhook validates** (in `api/v1alpha1/dbupgrade_webhook.go`):
- Database type matches configuration (e.g., `awsRds` requires `aws` config)
- Required fields are present
- Immutable fields aren't changed after creation

### 2.5 Why E2E Disables Webhooks

Look at `config/e2e/e2e-env-patch.yaml`:

```yaml
env:
- name: DISABLE_WEBHOOKS
  value: "true"
```

**Reason:** Webhooks require TLS certificates. The Kubernetes API server connects to the webhook over HTTPS and validates the certificate. In a kind cluster, we don't have cert-manager installed, so we disable webhooks entirely.

This is handled in `main.go`:

```go
if os.Getenv("DISABLE_WEBHOOKS") != "true" {
    if err = (&dbupgradev1alpha1.DBUpgrade{}).SetupWebhookWithManager(mgr); err != nil {
        // ...
    }
}
```

---

## 3. What's Missing for Production

| Gap | Impact | Solution |
|-----|--------|----------|
| **Webhook TLS certificates** | Webhooks won't work | cert-manager integration |
| **Webhook Service** | API server can't reach webhook | Create Service resource |
| **High Availability** | Single point of failure | Multiple replicas + PDB |
| **Production overlays** | Hard to customize per environment | Kustomize overlays |
| **Monitoring integration** | No alerting/dashboards | ServiceMonitor for Prometheus |
| **Network policies** | No network isolation | NetworkPolicy resources |

### 3.1 Webhook TLS Problem Explained

```
┌─────────────────┐                    ┌─────────────────┐
│                 │   HTTPS (mTLS)     │                 │
│  API Server     │ ──────────────────▶│  Webhook Pod    │
│                 │                    │  (port 9443)    │
└─────────────────┘                    └─────────────────┘
         │                                      │
         │ Needs to trust                       │ Needs valid
         │ webhook's certificate                │ TLS certificate
         ▼                                      ▼
┌─────────────────────────────────────────────────────────┐
│                                                         │
│  ValidatingWebhookConfiguration.webhooks[].clientConfig │
│    .caBundle: <base64-encoded CA certificate>           │
│                                                         │
│  The CA that signed the webhook's TLS certificate       │
│  must be included here so API server trusts it          │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

**Without cert-manager:**
- You'd need to manually generate certificates
- Mount them as secrets into the webhook pod
- Inject the CA bundle into the webhook configuration
- Handle rotation manually

**With cert-manager:**
- Define a `Certificate` resource
- cert-manager generates and rotates certs
- Injects CA bundle into webhook config automatically

---

## 4. Golden Production Configuration

Here's what a production-ready deployment should include:

### 4.1 High Availability

```yaml
# In manager.yaml
spec:
  replicas: 3  # Multiple replicas

---
# PodDisruptionBudget
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: controller-manager-pdb
spec:
  minAvailable: 1
  selector:
    matchLabels:
      control-plane: controller-manager
```

**Leader election** (already enabled) ensures only one controller is active. Others are hot standbys.

### 4.2 cert-manager Integration

```yaml
# Certificate for webhook
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: webhook-server-cert
  namespace: system
spec:
  secretName: webhook-server-cert
  dnsNames:
  - webhook-service.system.svc
  - webhook-service.system.svc.cluster.local
  issuerRef:
    name: selfsigned-issuer
    kind: ClusterIssuer

---
# Annotation on ValidatingWebhookConfiguration
metadata:
  annotations:
    cert-manager.io/inject-ca-from: system/webhook-server-cert
```

### 4.3 Security Hardening

```yaml
# NetworkPolicy - only allow API server to reach webhook
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: controller-manager-policy
spec:
  podSelector:
    matchLabels:
      control-plane: controller-manager
  ingress:
  - ports:
    - port: 9443  # Only webhook port
  policyTypes:
  - Ingress
```

### 4.4 Resource Tuning

Current defaults are conservative:
```yaml
resources:
  limits:
    cpu: 500m
    memory: 128Mi
  requests:
    cpu: 10m
    memory: 64Mi
```

For production, tune based on actual usage patterns.

---

## 5. Deployment Flow

### 5.1 Complete Deployment Sequence

```
Step 1: Prerequisites
├── cert-manager installed
└── Image pushed to registry

Step 2: Install CRDs
└── kubectl apply -f config/crd/bases/

Step 3: Create Namespace
└── kubectl create namespace system

Step 4: Deploy RBAC
├── ServiceAccount
├── ClusterRole
└── ClusterRoleBinding

Step 5: Deploy Certificates (if webhooks enabled)
├── Issuer/ClusterIssuer
└── Certificate

Step 6: Deploy Controller
├── Deployment
├── Service (for webhook)
└── ValidatingWebhookConfiguration

Step 7: Verify
├── kubectl get pods -n system
├── kubectl get crd dbupgrades.dbupgrade.subbug.learning
└── kubectl logs deployment/controller-manager -n system
```

### 5.2 Using Make Targets

```bash
# Install CRDs only
make install

# Deploy everything (uses config/default)
make deploy IMG=myregistry/dbupgrade-operator:v1.0.0

# Verify
kubectl get pods -n automatic-db-upgrades-system
```

### 5.3 Kustomize Build Process

```bash
# What make deploy actually does:
cd config/manager && kustomize edit set image controller=myregistry/dbupgrade-operator:v1.0.0
kustomize build config/default | kubectl apply -f -
```

The kustomize build:
1. Starts with `config/default/kustomization.yaml`
2. Includes CRDs, RBAC, and manager as resources
3. Applies namespace (`system`) and name prefix (`automatic-db-upgrades-`)
4. Substitutes the controller image

---

## 6. Upgrade Workflow

### 6.1 CRD Upgrades

**Safe (additive) changes:**
- Adding new optional fields
- Adding new enum values
- Relaxing validation (e.g., longer string limits)

**Breaking changes (require migration):**
- Removing fields
- Renaming fields
- Changing field types
- Adding new required fields

**Upgrade process for safe changes:**
```bash
# 1. Update CRD
kubectl apply -f config/crd/bases/

# 2. Update controller image
kubectl set image deployment/controller-manager \
  manager=myregistry/dbupgrade-operator:v1.1.0 \
  -n automatic-db-upgrades-system

# 3. Verify
kubectl rollout status deployment/controller-manager -n automatic-db-upgrades-system
```

### 6.2 Controller Image Updates

Kubernetes handles this via **rolling update**:

```
Time 0:  [Pod-v1.0] [Pod-v1.0] [Pod-v1.0]  (3 replicas running v1.0)

Time 1:  [Pod-v1.0] [Pod-v1.0] [Pod-v1.1]  (new pod created with v1.1)
                                    ↑ becomes ready

Time 2:  [Pod-v1.0] [Pod-v1.1] [Pod-v1.1]  (old pod terminated, another new created)

Time 3:  [Pod-v1.1] [Pod-v1.1] [Pod-v1.1]  (all pods running v1.1)
```

**Leader election** ensures:
- Only one controller processes events at a time
- Handoff happens cleanly during pod transitions
- No duplicate work or race conditions

### 6.3 Webhook Certificate Rotation

With cert-manager, certificate rotation is automatic:

1. cert-manager watches certificate expiry
2. Generates new certificate before expiry
3. Updates the Secret
4. Controller pod picks up new cert (may need restart with `--rotate-server-certificates`)
5. Updates CA bundle in webhook configuration

### 6.4 Zero-Downtime Checklist

- [ ] Multiple replicas running
- [ ] PodDisruptionBudget configured
- [ ] Leader election enabled
- [ ] Readiness probes passing before receiving traffic
- [ ] CRD changes are backward compatible
- [ ] Database migrations are idempotent

---

## 7. Key Takeaways

1. **An operator = CRD + Controller + RBAC + (optional) Webhook**

2. **Webhooks need TLS** - use cert-manager in production, disable in local testing

3. **RBAC follows least privilege** - controller only gets permissions it needs

4. **Leader election enables HA** - run multiple replicas, only one is active

5. **CRD upgrades must be additive** - breaking changes require migration strategy

6. **Kustomize overlays** - use different overlays for dev/staging/prod

7. **Rolling updates are safe** - Kubernetes handles pod replacement gracefully

---

---

## 8. Real Production: Complete Artifact Inventory

Let's get concrete. Here's **everything** you need for production deployment:

### 8.1 Container Images (Build Artifacts)

| Image | Purpose | Built From | Pushed To |
|-------|---------|------------|-----------|
| `dbupgrade-operator:v1.0.0` | Controller + webhook binary | `Dockerfile` | Regional ECRs |
| `crane-tar:v1.0.0` | Init container for migration extraction | `images/crane-tar/Dockerfile` | Regional ECRs |
| `arigaio/atlas:latest` | Migration runner (3rd party) | External | Mirror to regional ECRs |

**Why regional registries?**
```
┌─────────────────────────────────────────────────────────────────────┐
│                        Build Pipeline (CI)                          │
│                                                                     │
│  ┌─────────────┐                                                    │
│  │   GitHub    │                                                    │
│  │   Actions   │──build──▶ ghcr.io/yourorg/dbupgrade-operator:v1.0.0│
│  └─────────────┘                    │                               │
│                                     │ replicate                     │
│         ┌───────────────────────────┼───────────────────────┐       │
│         ▼                           ▼                       ▼       │
│  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐ │
│  │ us-east-1 ECR   │    │ eu-west-1 ECR   │    │ ap-south-1 ECR  │ │
│  │ 123.dkr.ecr...  │    │ 123.dkr.ecr...  │    │ 123.dkr.ecr...  │ │
│  └─────────────────┘    └─────────────────┘    └─────────────────┘ │
│         │                       │                       │           │
│         ▼                       ▼                       ▼           │
│  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐ │
│  │ EKS us-east-1   │    │ EKS eu-west-1   │    │ EKS ap-south-1  │ │
│  │ pulls locally   │    │ pulls locally   │    │ pulls locally   │ │
│  └─────────────────┘    └─────────────────┘    └─────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```

**Latency + reliability**: Pods pull images from same-region registry. No cross-region traffic, no single point of failure.

### 8.2 Kubernetes Manifests (Deployment Artifacts)

Here's the complete manifest inventory for production:

```
deploy/
├── base/                              # Shared across all environments
│   ├── kustomization.yaml
│   ├── namespace.yaml                 # Dedicated namespace
│   ├── crds/
│   │   └── dbupgrade.subbug.learning_dbupgrades.yaml
│   ├── rbac/
│   │   ├── service-account.yaml
│   │   ├── cluster-role.yaml
│   │   └── cluster-role-binding.yaml
│   ├── controller/
│   │   ├── deployment.yaml
│   │   └── service.yaml               # For webhook
│   ├── webhook/
│   │   ├── validating-webhook.yaml
│   │   └── certificate.yaml           # cert-manager Certificate
│   ├── security/
│   │   └── network-policy.yaml
│   └── resilience/
│       └── pod-disruption-budget.yaml
│
├── overlays/
│   ├── us-east-1/
│   │   ├── kustomization.yaml
│   │   └── patches/
│   │       └── image-registry.yaml    # Points to us-east-1 ECR
│   ├── eu-west-1/
│   │   ├── kustomization.yaml
│   │   └── patches/
│   │       └── image-registry.yaml    # Points to eu-west-1 ECR
│   └── ap-south-1/
│       └── ...
│
└── cert-manager/                       # Optional: only if not already installed
    ├── kustomization.yaml
    └── cert-manager.yaml               # Or reference Helm chart
```

### 8.3 Each Manifest Explained

#### Namespace (deploy/base/namespace.yaml)
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: dbupgrade-system
  labels:
    # Pod Security Standards - enforce restricted
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: latest
```

#### CRD with Deletion Protection (deploy/base/crds/...)
```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: dbupgrades.dbupgrade.subbug.learning
  annotations:
    # Prevent accidental deletion
    "helm.sh/resource-policy": keep
  finalizers:
    # Extra protection - requires explicit removal
    - customresourcecleanup.apiextensions.k8s.io
spec:
  # ... full CRD spec
```

#### Controller Deployment (deploy/base/controller/deployment.yaml)
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dbupgrade-controller
  namespace: dbupgrade-system
spec:
  replicas: 3                           # HA: multiple replicas
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
      maxSurge: 1
  selector:
    matchLabels:
      app: dbupgrade-controller
  template:
    metadata:
      labels:
        app: dbupgrade-controller
    spec:
      serviceAccountName: dbupgrade-controller
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: manager
        image: PLACEHOLDER_IMAGE        # Replaced by overlay
        args:
        - --leader-elect                # Only one active controller
        - --webhook-cert-dir=/tmp/k8s-webhook-server/serving-certs
        ports:
        - name: webhook
          containerPort: 9443
        - name: metrics
          containerPort: 8080
        - name: health
          containerPort: 8081
        env:
        - name: CRANE_IMAGE
          value: PLACEHOLDER_CRANE      # Replaced by overlay
        - name: ATLAS_IMAGE
          value: PLACEHOLDER_ATLAS      # Replaced by overlay
        volumeMounts:
        - name: webhook-certs
          mountPath: /tmp/k8s-webhook-server/serving-certs
          readOnly: true
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 500m
            memory: 256Mi
        livenessProbe:
          httpGet:
            path: /healthz
            port: health
          initialDelaySeconds: 15
          periodSeconds: 20
        readinessProbe:
          httpGet:
            path: /readyz
            port: health
          initialDelaySeconds: 5
          periodSeconds: 10
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop: ["ALL"]
          readOnlyRootFilesystem: true
      volumes:
      - name: webhook-certs
        secret:
          secretName: webhook-server-cert
      affinity:
        podAntiAffinity:
          # Spread across nodes for HA
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              labelSelector:
                matchLabels:
                  app: dbupgrade-controller
              topologyKey: kubernetes.io/hostname
```

#### Webhook Service (deploy/base/controller/service.yaml)
```yaml
apiVersion: v1
kind: Service
metadata:
  name: dbupgrade-webhook
  namespace: dbupgrade-system
spec:
  ports:
  - port: 443
    targetPort: webhook
  selector:
    app: dbupgrade-controller
```

#### cert-manager Certificate (deploy/base/webhook/certificate.yaml)
```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: webhook-server-cert
  namespace: dbupgrade-system
spec:
  secretName: webhook-server-cert
  duration: 8760h    # 1 year
  renewBefore: 720h  # Renew 30 days before expiry
  dnsNames:
  - dbupgrade-webhook.dbupgrade-system.svc
  - dbupgrade-webhook.dbupgrade-system.svc.cluster.local
  issuerRef:
    name: cluster-issuer    # Assumes ClusterIssuer exists
    kind: ClusterIssuer
```

#### ValidatingWebhook (deploy/base/webhook/validating-webhook.yaml)
```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: dbupgrade-validating-webhook
  annotations:
    # cert-manager injects CA bundle automatically
    cert-manager.io/inject-ca-from: dbupgrade-system/webhook-server-cert
webhooks:
- name: validate.dbupgrade.subbug.learning
  clientConfig:
    service:
      name: dbupgrade-webhook
      namespace: dbupgrade-system
      path: /validate-dbupgrade-subbug-learning-v1alpha1-dbupgrade
  rules:
  - apiGroups: ["dbupgrade.subbug.learning"]
    apiVersions: ["v1alpha1"]
    operations: ["CREATE", "UPDATE"]
    resources: ["dbupgrades"]
  admissionReviewVersions: ["v1"]
  sideEffects: None
  failurePolicy: Fail
  timeoutSeconds: 10
```

#### NetworkPolicy (deploy/base/security/network-policy.yaml)
```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: dbupgrade-controller
  namespace: dbupgrade-system
spec:
  podSelector:
    matchLabels:
      app: dbupgrade-controller
  policyTypes:
  - Ingress
  - Egress
  ingress:
  # Allow webhook calls from API server
  - from: []   # kube-apiserver IPs vary; often allow all for webhooks
    ports:
    - port: 9443
  # Allow metrics scraping from Prometheus
  - from:
    - namespaceSelector:
        matchLabels:
          name: monitoring
    ports:
    - port: 8080
  egress:
  # Allow DNS
  - to: []
    ports:
    - port: 53
      protocol: UDP
  # Allow API server access
  - to: []
    ports:
    - port: 443
  # Allow registry access for Job init containers
  - to: []
    ports:
    - port: 443
```

#### PodDisruptionBudget (deploy/base/resilience/pod-disruption-budget.yaml)
```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: dbupgrade-controller
  namespace: dbupgrade-system
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: dbupgrade-controller
```

#### Regional Overlay (deploy/overlays/us-east-1/kustomization.yaml)
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

namespace: dbupgrade-system

resources:
- ../../base

images:
- name: PLACEHOLDER_IMAGE
  newName: 123456789012.dkr.ecr.us-east-1.amazonaws.com/dbupgrade-operator
  newTag: v1.0.0
- name: PLACEHOLDER_CRANE
  newName: 123456789012.dkr.ecr.us-east-1.amazonaws.com/crane-tar
  newTag: v1.0.0
- name: PLACEHOLDER_ATLAS
  newName: 123456789012.dkr.ecr.us-east-1.amazonaws.com/atlas
  newTag: 0.14.0
```

---

## 9. Multi-Cluster Deployment: GitOps Pattern

### 9.1 Why NOT `make deploy` in Production

You're correct: `make deploy` assumes:
- Laptop has `kubectl` access to cluster
- Human runs command manually
- No audit trail
- No rollback mechanism
- Doesn't scale to multiple clusters

**Production uses GitOps:**
```
┌─────────────────────────────────────────────────────────────────────┐
│                        GitOps Pattern                               │
│                                                                     │
│  ┌─────────────┐         ┌─────────────┐         ┌─────────────┐  │
│  │   GitHub    │ push    │   Argo CD   │ sync    │  Kubernetes │  │
│  │   Repo      │────────▶│  (per cluster)────────▶│   Cluster   │  │
│  └─────────────┘         └─────────────┘         └─────────────┘  │
│        │                        │                                   │
│        │ PR + review            │ auto-sync                        │
│        │                        │ health checks                    │
│        │                        │ rollback on failure              │
│        ▼                        ▼                                   │
│  ┌─────────────┐         ┌─────────────┐                           │
│  │  Manifest   │         │  Audit Log  │                           │
│  │  Changes    │         │  Who/When   │                           │
│  └─────────────┘         └─────────────┘                           │
└─────────────────────────────────────────────────────────────────────┘
```

### 9.2 Repository Structure for GitOps

```
gitops-repo/
├── apps/
│   └── dbupgrade/
│       ├── base/                    # Same as deploy/base above
│       └── overlays/
│           ├── us-east-1-prod/
│           ├── eu-west-1-prod/
│           └── ap-south-1-prod/
│
├── clusters/
│   ├── us-east-1-prod/
│   │   └── dbupgrade.yaml           # Argo CD Application
│   ├── eu-west-1-prod/
│   │   └── dbupgrade.yaml
│   └── ap-south-1-prod/
│       └── dbupgrade.yaml
│
└── infrastructure/
    └── cert-manager/                # Shared infra
        └── ...
```

### 9.3 Argo CD Application (per cluster)

**File:** `clusters/us-east-1-prod/dbupgrade.yaml`
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: dbupgrade
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/yourorg/gitops-repo
    targetRevision: main
    path: apps/dbupgrade/overlays/us-east-1-prod
  destination:
    server: https://kubernetes.default.svc   # In-cluster
    namespace: dbupgrade-system
  syncPolicy:
    automated:
      prune: true          # Remove resources not in Git
      selfHeal: true       # Revert manual changes
    syncOptions:
    - CreateNamespace=true
    - PrunePropagationPolicy=foreground
    - PruneLast=true       # CRDs deleted last
```

### 9.4 Deployment Sequence with Argo CD

```
1. Developer creates PR:
   - Updates image tag in overlay
   - Changes deployment config

2. PR review + merge to main

3. Argo CD (in each cluster) detects change:
   ├── Diffs desired state (Git) vs actual state (cluster)
   └── Applies changes in order:
       a. CRDs first (Sync Wave -1)
       b. RBAC (Sync Wave 0)
       c. cert-manager Certificate (Sync Wave 1)
       d. Deployment + Service (Sync Wave 2)
       e. ValidatingWebhook (Sync Wave 3)

4. Argo CD monitors health:
   ├── Deployment ready?
   ├── All pods healthy?
   └── Webhook responding?

5. If unhealthy → auto-rollback to previous version
```

### 9.5 Sync Waves for Ordered Deployment

Add annotations to control deployment order:

```yaml
# CRD - deploy first
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "-1"

# RBAC - after CRD
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "0"

# Certificate - needs RBAC
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "1"

# Deployment - needs certificate
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "2"

# Webhook - needs deployment running
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "3"
```

---

## 10. Complete Deployment Checklist

### 10.1 Pre-requisites (One-time per cluster)

- [ ] cert-manager installed and ClusterIssuer configured
- [ ] Argo CD installed and connected to Git repo
- [ ] Container registry accessible from cluster
- [ ] Network policies enabled (CNI supports it)

### 10.2 Image Build Pipeline (CI)

- [ ] Build `dbupgrade-operator` image
- [ ] Build `crane-tar` image
- [ ] Security scan images (Trivy, etc.)
- [ ] Push to primary registry (ghcr.io)
- [ ] Replicate to regional registries

### 10.3 Manifest Preparation

- [ ] CRD with deletion protection annotation
- [ ] Namespace with Pod Security Standards
- [ ] RBAC (least privilege)
- [ ] Certificate for webhook TLS
- [ ] Deployment with:
  - [ ] 3+ replicas
  - [ ] Pod anti-affinity
  - [ ] Resource limits
  - [ ] Security context
  - [ ] Readiness/liveness probes
- [ ] Service for webhook
- [ ] ValidatingWebhookConfiguration with cert-manager annotation
- [ ] NetworkPolicy
- [ ] PodDisruptionBudget

### 10.4 GitOps Setup

- [ ] Argo CD Application per cluster
- [ ] Sync waves configured for ordering
- [ ] Auto-sync enabled
- [ ] Rollback policy defined

### 10.5 Verification

```bash
# Per cluster
kubectl get pods -n dbupgrade-system
kubectl get crd dbupgrades.dbupgrade.subbug.learning
kubectl get validatingwebhookconfiguration dbupgrade-validating-webhook

# Test webhook
kubectl apply -f - <<EOF
apiVersion: dbupgrade.subbug.learning/v1alpha1
kind: DBUpgrade
metadata:
  name: test-validation
spec:
  migrations:
    image: test:v1
  database:
    type: awsRds
    # Missing aws config - should be rejected by webhook
EOF
# Expected: Error from server (database.type=awsRds requires either aws or connection)
```

---

## 11. Summary: What `make deploy` vs Production Looks Like

| Aspect | `make deploy` (Dev) | Production (GitOps) |
|--------|---------------------|---------------------|
| **Who runs it** | Developer from laptop | Argo CD in cluster |
| **Cluster access** | kubectl on laptop | Argo CD service account |
| **Audit trail** | None | Git commit history |
| **Rollback** | Manual | Automatic on failure |
| **Multi-cluster** | Run N times manually | Automatic per-cluster sync |
| **Secrets** | Loaded from laptop | External Secrets Operator |
| **Image source** | Local registry | Regional ECR/GCR |

---

## Next Steps

- [02-crd-lifecycle.md](./02-crd-lifecycle.md) - Deep dive into CRD versioning and migrations
- [03-multi-region-packaging.md](./03-multi-region-packaging.md) - Helm vs Kustomize for distribution
