# LinkedIn Post Draft

---

**Building a Production-Ready Kubernetes Operator: DBUpgrade**

I just open-sourced DBUpgrade Operator - a Kubernetes operator for automated database schema migrations. Here's what I learned building it:

**The Problem**
Database migrations in Kubernetes are often manual, error-prone, and lack proper safety checks. Teams struggle with:
- Coordinating migrations with application deployments
- Ensuring applications are ready before/after schema changes
- Managing AWS RDS IAM authentication complexity

**The Solution**
DBUpgrade Operator automates the entire workflow:

1. **Extract migrations from your app images** - No separate migration images needed. Crane extracts migration files directly from your application containers.

2. **Pre-flight checks** - Block migrations until:
   - All pods meet minimum version requirements (semver validation)
   - Custom metrics are within acceptable thresholds

3. **Post-migration validation** - Verify success by:
   - Checking application health metrics
   - Supporting "bake time" before declaring victory

4. **AWS RDS IAM Auth** - Built-in support for RDS/Aurora with IAM authentication. The operator generates short-lived tokens - your migrations never touch long-lived credentials.

5. **Safety by design** - Can't modify spec while migration is running. The validating webhook blocks changes to prevent inconsistent states.

**Technical Highlights**
- Built with kubebuilder and controller-runtime
- Two-condition status model (Ready + Progressing) with meaningful Reason codes
- Helm chart published to ghcr.io
- Full GitHub Actions CI/CD pipeline
- Prometheus metrics for observability

**What I Learned**
- Status conditions should be simple. Started with 4, refined to 2 + Reasons.
- Optimistic concurrency is elegant - let controller-runtime handle conflicts.
- Webhooks are critical for safety - reject bad state before it exists.
- E2E tests in kind are invaluable for catching integration issues.

Check it out: github.com/subganapathy/automatic-db-upgrades

Would love feedback from folks running database migrations in Kubernetes!

#kubernetes #golang #devops #databases #opensource

---

## Shorter Version (for character limits)

---

Just open-sourced DBUpgrade Operator - automates database migrations in Kubernetes with:

- Pre/post migration health checks
- Pod version validation (semver)
- AWS RDS IAM authentication
- Safety guards (can't change spec during migration)
- Helm chart + GitHub Actions CI

Built with kubebuilder, learned a lot about controller patterns and status design.

github.com/subganapathy/automatic-db-upgrades

#kubernetes #golang #devops #opensource

---
