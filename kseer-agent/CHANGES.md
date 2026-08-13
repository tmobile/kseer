# Resync persistence across restart (2026-05-07)

P1, P2, and P3 resync schedules now anchor to the most recent SEEDED row in ClickHouse rather than `now + interval` at startup. Restarting an agent pod no longer resets the resync timer; if a kind hasn't been resynced within its interval window at startup, the agent fires immediately.

## Behavior

- **At watcher startup**: query CH for the most recent SEEDED row per kind (within a window of `2 × max-interval`, clamped to `[7d, 30d]`).
- **P1**: `runP1ResyncLoop` schedules the first fire at `lastSeed + interval`. Past timestamps fire immediately on the next loop iteration.
- **P2**: `runWatchEventLoop` arms the first session's resync timer at `lastSeed + interval - now`. Subsequent watch reconnects use the full interval (preserves existing 410-Gone behavior).
- **P3**: `runP3RelistLoop` arms the first relist timer at `lastSeed + interval - now` (or 0 if past).
- **Fallback**: if CH is unavailable, `events_table` is unset, or the kind has no SEEDED row in the lookback window → behave as today (`now + interval`). No data loss; just one missed cycle's worth of staleness.

## CH query

```sql
SELECT kind, max(timestamp)
FROM <events_table>
WHERE timestamp >= now() - INTERVAL ? SECOND
  AND operation = 'SEEDED'
  AND cluster_name = ?
GROUP BY kind
```

Issued once per cluster on watcher startup. Same risk class as the existing `GetSeedCompletionRVsByKind` query.

## Retry policy

All three CH seeder queries (`GetSeedCompletionRVsByKind`, `GetResumeRVsByKind`, `GetLastSeedTimestampsByKind`) are now wrapped in `querySeederWithRetry` — exponential backoff, configurable retry count.

```yaml
agent:
  watchConfig:
    seederQueryRetries: 2     # default → 3 total attempts
    seederQueryBackoff: "2s"  # doubles each attempt → 2s, 4s, 8s
```

Worst-case wait before fallback with defaults: 2s + 4s = 6s.

## Files changed

- `pkg/config/config.go` — `SeederQueryRetries`, `SeederQueryBackoff` on `WatchConfig`.
- `pkg/watcher/seeder_retry.go` — `querySeederWithRetry`, `lastSeedQueryWindow` (new file).
- `pkg/watcher/watcher.go` — `SeedTracker.GetLastSeedTimestampsByKind`, `kindLastSeed` map populated at startup, `gvrFirstResyncDone` flag for P2 first-session detection.
- `pkg/watcher/tier_paths.go` — P1/P3 first-fire scheduling reads `kindLastSeed`.
- `pkg/outputs/clickhouse/clickhouse.go` — implementation of `GetLastSeedTimestampsByKind`.
- `helm-chart/kseer/values.yaml` + `templates/agent-configmap.yaml` — new retry knobs.

---

# Priority Tiers (2026-05-05)

Adds three sync tiers — P1, P2 (default), P3 — that can be assigned per Kubernetes kind to trade off freshness, durability, and cost.

## Tiers

- **P1**: legacy LIST+WATCH PLUS a sequential resync loop. Each P1 kind is re-LISTed at its own interval; the watcher waits for the previous kind's events to flush durably to ClickHouse before listing the next kind. P1 LISTs are non-paginated. Use for low-cardinality, high-importance kinds (Namespace, Node, NetworkPolicy, ResourceQuota, CRDs).
- **P2** (default): legacy behavior. LIST+WATCH with optional `resync_interval`. Any kind not listed in P1/P3 is P2.
- **P3**: no WATCH. Periodic LIST only. Each LIST emits EXISTING events for every current object. The agent does not synthesize DELETED events. Clients determine "live" objects by filtering events to within the most recent `relistInterval` window — anything older than one interval is implicitly stale. Use for slow-moving kinds where event-stream fidelity isn't needed.

## Config

```yaml
default_watch_config:
  tiers:
    p1:
      resync_interval: 5m
      kinds:
        - kind: Namespace
        - kind: Node
        - kind: NetworkPolicy
          resync_interval: 1m   # per-kind override
    p3:
      relist_interval: 1h
      kinds:
        - kind: BigCustomResource
          apiVersion: foo.example.com/v1   # optional disambiguation
          relist_interval: 6h
```

`apiVersion` is optional. When omitted, the entry matches any GVR with that Kind. Set it only when the same Kind exists in multiple API groups.

## Output manager

A second processing pipeline runs in parallel for P1 events (`p1Buffer` + `FlushP1`). The watcher's per-kind sequential resync calls `FlushP1` between kinds; this never blocks on P2/P3 traffic and vice versa. The default `Flush` was also hardened to drain the buffer channel before signaling completion (closes a Send-then-Flush race that existed under load).

## ClickHouse schema

No schema changes. P3 reuses the existing `timestamp` column for client-side staleness filtering: queries restrict to `timestamp >= now() - INTERVAL <relistInterval>` per kind to determine the live set.

## Files changed

- `pkg/config/config.go` — `TierConfig`, `P1TierConfig`, `P3TierConfig`, `KindTierEntry` + validation.
- `pkg/watcher/tiers.go` — `tierResolver` (kind-only and exact-match lookup).
- `pkg/watcher/tier_paths.go` — `runP1ResyncLoop`, `resyncP1Kind`, `startP3RelistLoop`, `runP3RelistOnce`.
- `pkg/watcher/watcher.go` — `discoverAndWatch` dispatches by tier; `handleObject` / SEEDED emit route via `sendByTier`.
- `pkg/outputs/manager.go` — second processLoop for P1; `SendP1`, `FlushP1`; non-blocking buffer drain on flush.

---

# Deployment Modes Implementation - Summary of Changes

## Overview

This document summarizes the changes made to add two installation/deployment modes to kseer-agent:

1. **In-Cluster Mode** - Helm chart deployment inside a cluster
2. **Multi-Cluster Mode** - External binary/container deployment

## Files Added

### Helm Chart (In-Cluster Mode)

```
deploy/helm/kseer-agent/
├── Chart.yaml                          # Helm chart metadata
├── .helmignore                         # Helm ignore patterns
├── values.yaml                         # Default Helm values
├── README.md                           # Helm chart documentation
├── templates/
│   ├── _helpers.tpl                    # Helm template helpers
│   ├── serviceaccount.yaml             # ServiceAccount template
│   ├── clusterrole.yaml                # ClusterRole template
│   ├── clusterrolebinding.yaml         # ClusterRoleBinding template
│   ├── configmap.yaml                  # Configuration ConfigMap
│   ├── deployment.yaml                 # Agent Deployment
│   └── NOTES.txt                       # Installation notes
└── values-examples/
    ├── production.yaml                 # Production config example
    ├── development.yaml                # Development config example
    └── minimal.yaml                    # Minimal testing config
```

### Multi-Cluster Mode

```
deploy/multi-cluster/
├── README.md                           # Multi-cluster documentation
├── config-example.yaml                 # Configuration example
├── docker-compose.yaml                 # Docker Compose setup
└── systemd/
    └── kseer-agent.service             # Systemd service unit
```

### Documentation

```
INSTALL.md                              # Complete installation guide for both modes
DEPLOYMENT-MODES.md                     # Detailed comparison and architecture
CHANGES.md                              # This file
```

## Files Modified

### Code Changes

1. **pkg/config/config.go**
   - Added `DeploymentMode` type with `in-cluster` and `multi-cluster` constants
   - Updated `Config` struct with `Mode` field
   - Enhanced `Load()` function with mode-specific validation
   - Added automatic in-cluster config creation
   - Backward compatible (defaults to `multi-cluster`)

2. **cmd/agent/main.go**
   - Added logging of deployment mode on startup
   - Shows mode and cluster count in startup message

3. **config.yaml**
   - Added `mode` field with documentation
   - Set default to `multi-cluster` for backward compatibility

4. **README.md**
   - Added prominent "Deployment Modes" section at the top
   - Updated configuration examples for both modes
   - Updated installation section with links to detailed guides
   - Updated running/deployment sections
   - Added Quick Start for both modes

5. **Makefile**
   - Enhanced with Helm-related targets
   - Added docker build/push targets
   - Added help documentation
   - Organized targets by category

## Key Features Implemented

### Configuration Validation

- **In-Cluster Mode**:
  - Only allows one cluster configuration
  - Kubeconfig must be empty (uses service account)
  - Auto-creates default cluster config if none provided

- **Multi-Cluster Mode**:
  - Requires at least one cluster configured
  - Each cluster must have kubeconfig path
  - Supports multiple clusters

### Helm Chart Features

- **Security**:
  - Pod Security Context (non-root, read-only filesystem)
  - Security Context with no privilege escalation
  - RBAC with minimal required permissions
  - Optional seccomp profile

- **Reliability**:
  - Liveness and readiness probes
  - Automatic restart on failure
  - Config checksum for automatic rollout on changes

- **Flexibility**:
  - Customizable resource limits
  - Node selector, tolerations, affinity support
  - Configurable RBAC rules
  - Multiple output types

- **Operations**:
  - Structured logging
  - Clear installation notes
  - Easy upgrade path
  - Configuration via values.yaml

### Multi-Cluster Deployment Options

- **Binary**: Direct execution with config file
- **Docker**: Container with mounted configs
- **Docker Compose**: Full stack with dependencies
- **Systemd**: System service with auto-restart

## Architecture Decisions

### Mode Selection

The `mode` field in configuration determines behavior:

```yaml
mode: in-cluster    # Uses service account, single cluster
mode: multi-cluster # Uses kubeconfigs, multiple clusters
```

### Backward Compatibility

- Existing configs without `mode` field default to `multi-cluster`
- All existing functionality preserved
- No breaking changes to APIs or configuration structure

### Authentication

- **In-Cluster**: Uses Kubernetes projected service account tokens
- **Multi-Cluster**: Uses kubeconfig files with various auth methods

### RBAC

- **In-Cluster**: ClusterRole/ClusterRoleBinding created by Helm
- **Multi-Cluster**: Permissions defined in kubeconfig credentials

## Configuration Examples

### In-Cluster Mode (Helm)

```yaml
# values.yaml
mode: in-cluster

cluster:
  name: "production"
  namespaces: []
  resources: []

watchConfig:
  resyncInterval: "5m"

outputs:
  kafka:
    enabled: true
    brokers: ["kafka:9092"]
    topic: "k8s-events"
```

### Multi-Cluster Mode (Config File)

```yaml
# config.yaml
mode: multi-cluster

clusters:
  - name: "prod"
    kubeconfig: "/path/to/prod.yaml"
  - name: "staging"
    kubeconfig: "/path/to/staging.yaml"

outputs:
  - type: kafka
    config:
      brokers: ["kafka:9092"]
      topic: "k8s-events"
```

## Testing Recommendations

### In-Cluster Mode

```bash
# Test with minimal config (stdout only)
helm install kseer-agent ./deploy/helm/kseer-agent \
  -n monitoring --create-namespace \
  -f deploy/helm/kseer-agent/values-examples/minimal.yaml

# Verify
kubectl get pods -n monitoring
kubectl logs -n monitoring -l app.kubernetes.io/name=kseer-agent -f

# Clean up
helm uninstall kseer-agent -n monitoring
```

### Multi-Cluster Mode

```bash
# Build agent
make build

# Create test config
cat > test-config.yaml <<EOF
mode: multi-cluster
clusters:
  - name: "test"
    kubeconfig: "$HOME/.kube/config"
    namespaces: ["default"]
outputs:
  - type: stdout
    config:
      pretty: true
buffer:
  size: 100
  flush_interval: "1s"
EOF

# Run agent
./kseer-agent --config=test-config.yaml

# Should see events streaming to stdout
```

## Migration Path

### From Old Config to In-Cluster Mode

Old config:
```yaml
clusters:
  - name: "cluster-1"
    kubeconfig: ""  # In-cluster
```

New config (Helm values):
```yaml
mode: in-cluster
cluster:
  name: "cluster-1"
```

### From Old Config to Multi-Cluster Mode

Old config:
```yaml
clusters:
  - name: "cluster-1"
    kubeconfig: "~/.kube/config"
```

New config:
```yaml
mode: multi-cluster  # Add this line
clusters:
  - name: "cluster-1"
    kubeconfig: "~/.kube/config"  # No change
```

## Documentation Structure

```
README.md                    # Main documentation with deployment modes
├── Quick Start
├── Deployment Modes
├── Installation (links to INSTALL.md)
├── Configuration
└── Running

INSTALL.md                   # Detailed installation guide
├── In-Cluster Mode
│   ├── Prerequisites
│   ├── Quick Start
│   ├── Configuration
│   ├── RBAC
│   └── Operations
└── Multi-Cluster Mode
    ├── Prerequisites
    ├── Installation Options
    ├── Configuration
    ├── Running Methods
    └── Security

DEPLOYMENT-MODES.md          # Architecture and comparison
├── Mode Comparison Table
├── Architecture Diagrams
├── Configuration Reference
├── Choosing a Mode
├── Migration Guide
└── Implementation Details

deploy/helm/kseer-agent/README.md    # Helm chart docs
deploy/multi-cluster/README.md       # Multi-cluster docs
```

## Security Considerations

### In-Cluster Mode

- Service account tokens automatically mounted
- RBAC rules explicitly defined in ClusterRole
- Pod runs as non-root user (65534)
- Read-only root filesystem
- No privilege escalation
- All capabilities dropped

### Multi-Cluster Mode

- Kubeconfig files must be protected (chmod 600)
- Credentials should use read-only service accounts
- Consider using temporary credentials
- Implement credential rotation
- Use VPN for cluster access when possible

## Future Enhancements

Potential future additions:

1. **Namespace-scoped mode**: Watch only specific namespace (Role instead of ClusterRole)
2. **Agent clustering**: Multiple agents with leader election
3. **Dynamic cluster discovery**: Auto-detect clusters from cloud APIs
4. **Credential rotation**: Automatic kubeconfig refresh
5. **Multi-region support**: Agent replication across regions
6. **Metrics endpoint**: Prometheus metrics for monitoring
7. **Health checks**: HTTP health/ready endpoints

## Breaking Changes

**None** - All changes are backward compatible.

Existing configurations will:
- Default to `multi-cluster` mode
- Continue working without modification
- Not require any changes to deployment

## Validation Checklist

- [x] Code compiles successfully
- [x] No linter errors in Go code
- [x] Backward compatibility maintained
- [x] Configuration validation works
- [x] Documentation is comprehensive
- [x] Examples provided for both modes
- [ ] Helm chart validates (requires helm installation)
- [ ] Integration tests pass (requires test clusters)
- [ ] End-to-end testing (requires Kafka + clusters)

## Quick Reference

### Install In-Cluster

```bash
helm install kseer-agent ./deploy/helm/kseer-agent -n monitoring --create-namespace
```

### Install Multi-Cluster

```bash
make build
./kseer-agent --config=config.yaml
```

### Build and Test

```bash
# Build
make build

# Test
make test

# Build Docker image
make docker-build

# Lint Helm chart (requires helm)
make helm-lint
```

## Support

For questions or issues:

1. Check the documentation: `README.md`, `INSTALL.md`, `DEPLOYMENT-MODES.md`
2. Review examples: `deploy/helm/kseer-agent/values-examples/`, `deploy/multi-cluster/`
3. Check logs: `kubectl logs` (in-cluster) or `journalctl` (multi-cluster)
4. Validate config: Ensure `mode` field is set correctly
5. Test connectivity: Verify cluster access and output destinations

