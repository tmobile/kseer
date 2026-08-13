# Kseer Agent Helm Chart

Deploys the kseer-agent in **in-cluster mode** to watch Kubernetes resources and stream events to configured outputs.

## TL;DR

```bash
# In-cluster mode (agent runs in the cluster it watches)
helm install kseer-agent ./kseer-agent -n kseer-agent --create-namespace \
  --set namespace.create=false \
  --set outputs.clickhouse.credentialsSecret.name=kseer-clickhouse-creds

# Multi-cluster mode (chart emits RBAC + SA-token only; external readers pull
# the token from Vault)
helm install kseer-agent ./kseer-agent -n monitoring --create-namespace \
  -f values-examples/external-vault.yaml
```

### Namespace behavior

You have two patterns for where kseer-agent resources land. Pick based on what you want `helm uninstall` to do:

#### Pattern A — Helm's `--create-namespace` manages the namespace (recommended default)

Pre-create via Helm's flag; chart skips its own Namespace template.

```bash
helm install kseer-agent ./kseer-agent -n kseer-agent --create-namespace \
  --set namespace.create=false \
  -f my-values.yaml
```

- `helm uninstall` deletes tracked resources but **leaves the namespace** — remove manually with `kubectl delete ns kseer-agent` when done
- Works on any Helm version
- One-liner install

#### Pattern B — Chart manages the namespace (cascading cleanup on uninstall)

Chart renders a `Namespace` resource. To avoid the "already exists" conflict with `--create-namespace`, put release metadata in a *different* existing namespace:

```bash
helm install kseer-agent ./kseer-agent -n default \
  --set namespace.create=true \
  --set namespace.name=kseer-agent \
  -f my-values.yaml
```

- `helm uninstall kseer-agent -n default` deletes the namespace (cascading → everything in it)
- `-n` is for Helm's release-tracking secret, not the workload namespace — slightly unusual
- Alternative: if you have Helm ≥ 3.17, use `-n kseer-agent --create-namespace --take-ownership` with `namespace.create=true`

#### Multi-cluster mode

The `namespace:` block is ignored. All namespaced resources live in `.Release.Namespace` (whatever you pass via `-n`).

## Introduction

This chart bootstraps a kseer-agent deployment on a Kubernetes cluster using the Helm package manager. The agent will:

- Watch all resources in the cluster (configurable)
- Stream events to ClickHouse, Kafka, or other configured outputs
- Use projected service account tokens for authentication
- Automatically discover new CRDs

## Prerequisites

- Kubernetes 1.19+ (1.29+ for the default FlowSchema API version; set `flowSchema.legacy: true` below that)
- Helm 3.0+
- For the ClickHouse output: a reachable ClickHouse instance and credentials supplied by one of the three paths in [ClickHouse Credentials Setup](#clickhouse-credentials-setup)

## Installing the Chart

```bash
# Try it out: stdout only, no external dependencies
helm install kseer-agent ./kseer-agent \
  -n monitoring \
  --create-namespace \
  -f values-examples/minimal.yaml \
  --set cluster.name=my-cluster

# With your own values file
helm install kseer-agent ./kseer-agent -n monitoring -f my-values.yaml
```

Always set `cluster.name` — the chart's default is empty, which makes the agent
fall back to `local-cluster` and mislabel every event it stores.

### ClickHouse Credentials Setup

Choose exactly one of these paths. The chart fails at render time if the
combination is ambiguous. Whichever you pick, the Secret must have `username`
and `password` keys.

**A — External Secrets Operator pulls from Vault (recommended where ESO exists):**

```yaml
externalSecret:
  enabled: true
  vaultServer: "https://vault.example.com"
  vaultMount: "kv"
  vaultPath: "kseer/clickhouse"
  vaultRole: "kseer-agent"
  vaultAuthMountPath: "kubernetes-prd-aws-k8s-foo-001"
outputs:
  clickhouse:
    credentialsSecret:
      create: false
```

**B — Helm creates the Secret:**

```yaml
outputs:
  clickhouse:
    username: "<ch-username>"
    password: "<ch-password>"
    credentialsSecret:
      create: true
```

**C — Reference a Secret you created out of band:**

```bash
kubectl create secret generic kseer-clickhouse-creds \
  --from-literal=username=<ch-username> \
  --from-literal=password=<ch-password> \
  -n monitoring
```

```yaml
outputs:
  clickhouse:
    enabled: true
    credentialsSecret:
      create: false
      name: "kseer-clickhouse-creds"
```

## Uninstalling the Chart

```bash
helm uninstall kseer-agent -n monitoring
```

## Configuration

The following table lists the configurable parameters of the kseer-agent chart and their default values.

### Global Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `mode` | Deployment mode (`in-cluster` or `multi-cluster`) | `in-cluster` |
| `namespace.create` | In-cluster only: chart renders a Namespace resource (Pattern B) | `false` |
| `namespace.name` | Namespace name (empty = chart fullname, e.g. `kseer-agent`) | `""` |
| `nameOverride` | Override the name of the chart | `""` |
| `fullnameOverride` | Override the fullname of the chart | `""` |

### Image Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Image repository | `registry.example.com/kseer/kseer-agent` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Image tag (defaults to chart appVersion) | `""` |
| `imagePullSecrets` | Image pull secrets | `[]` |

### Service Account Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.annotations` | Service account annotations | `{}` |
| `serviceAccount.name` | Service account name | `""` |

### RBAC Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rbac.create` | Create RBAC resources | `true` |
| `rbac.clusterRoleName` | Override the generated ClusterRole name | `""` |
| `rbac.clusterRoleBindingName` | Override the generated ClusterRoleBinding name | `""` |
| `rbac.rules` | ClusterRole rules | `get`/`list`/`watch` on `*` |

### API Priority and Fairness

| Parameter | Description | Default |
|-----------|-------------|---------|
| `flowSchema.enabled` | Render a FlowSchema so agent reads get their own priority level | `true` |
| `flowSchema.name` | Override the generated FlowSchema name | `""` |
| `flowSchema.legacy` | Use `flowcontrol.apiserver.k8s.io/v1beta2` for Kubernetes < 1.29 | `false` |
| `flowSchema.matchingPrecedence` | FlowSchema matching precedence | `2000` |
| `flowSchema.priorityLevel` | Priority level configuration to bind to | `workload-low` |

### External Secrets (ClickHouse credentials path A)

| Parameter | Description | Default |
|-----------|-------------|---------|
| `externalSecret.enabled` | Render a SecretStore and ExternalSecret backed by Vault | `false` |
| `externalSecret.vaultServer` | Vault URL (required when enabled) | `""` |
| `externalSecret.vaultMount` | KV v2 mount name | `""` |
| `externalSecret.vaultPath` | Path holding `username` and `password` | `""` |
| `externalSecret.vaultRole` | Vault Kubernetes auth role for the agent ServiceAccount | `""` |
| `externalSecret.vaultAuthMountPath` | This cluster's Kubernetes auth mount path in Vault | `""` |
| `externalSecret.secretStoreName` | Override the generated SecretStore name | `""` |
| `externalSecret.refreshInterval` | How often ESO re-syncs from Vault | `1h` |

### Multi-Cluster Chart Mode

| Parameter | Description | Default |
|-----------|-------------|---------|
| `tokenSecret.suffix` | Suffix on the service account token Secret; alternate `a`/`b` to rotate | `a` |

### Cluster Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `cluster.name` | Name of the cluster, stored on every event. Set this — empty makes the agent fall back to `local-cluster` | `""` |
| `cluster.namespaces` | Namespaces to watch (empty = all) | `[]` |
| `cluster.resources` | Resources to watch (empty = all) | `[]` |

### ClickHouse Output Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `outputs.clickhouse.enabled` | Enable ClickHouse output | `true` |
| `outputs.clickhouse.host` | ClickHouse host | `""` |
| `outputs.clickhouse.port` | ClickHouse port | `9000` |
| `outputs.clickhouse.database` | ClickHouse database | `""` |
| `outputs.clickhouse.table` | Target table for events | `""` |
| `outputs.clickhouse.eventsTable` | Table for resume RV queries | `""` |
| `outputs.clickhouse.tls` | Enable TLS | `false` |
| `outputs.clickhouse.tlsSkipVerify` | Skip TLS verification | `false` |
| `outputs.clickhouse.batchSize` | Rendered into the output config but **never read by the agent** — batching comes from `buffer.batchSize` | `1000` |
| `outputs.clickhouse.tableVersion` | Table format (`object_json`, `object_string`, `object_normalized`) | `object_json` |
| `outputs.clickhouse.username` | Username (only used when credentialsSecret.create=true) | `""` |
| `outputs.clickhouse.password` | Password (only used when credentialsSecret.create=true) | `""` |
| `outputs.clickhouse.credentialsSecret.create` | Create credentials Secret | `false` |
| `outputs.clickhouse.credentialsSecret.name` | Name of credentials Secret | `""` |

### Kafka Output Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `outputs.kafka.enabled` | Enable Kafka output | `false` |
| `outputs.kafka.brokers` | Kafka broker addresses | `["kafka:9092"]` |
| `outputs.kafka.topic` | Kafka topic name | `kubernetes-events` |
| `outputs.kafka.compression` | Kafka compression | `snappy` |
| `outputs.kafka.config` | Additional Kafka config | `{}` |

### Stdout Output Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `outputs.stdout.enabled` | Enable stdout output | `false` |
| `outputs.stdout.pretty` | Pretty print JSON | `false` |

### Buffer Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `buffer.size` | In-memory event queue capacity. **Events are dropped with a warning when full**, never blocked | `500000` |
| `buffer.flushInterval` | Maximum time a partial batch waits before being sent | `5s` |
| `buffer.retryInterval` | Parsed but **currently inert** — failed sends are logged and dropped, not retried | `30s` |
| `buffer.batchSize` | Events per batch to outputs | `25000` |

### Concurrency Control

| Parameter | Description | Default |
|-----------|-------------|---------|
| `concurrencyControl.enabled` | Enable concurrency limits | `true` |
| `concurrencyControl.maxGlobalLists` | Max concurrent LIST ops globally | `30` |
| `concurrencyControl.maxListsPerCluster` | Max concurrent LIST ops per cluster | `10` |
| `concurrencyControl.maxClusterInits` | Max concurrent cluster initializations | `5` |

### Watch Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `watchConfig.initialBackoff` | Initial reconnect backoff | `1s` |
| `watchConfig.maxBackoff` | Maximum reconnect backoff | `5m` |
| `watchConfig.timeoutSeconds` | API server watch timeout | `432000` (5 days) |
| `watchConfig.allowBookmarks` | Request watch bookmarks. **Cannot currently be disabled** — the agent forces this back to `true` | `true` |
| `watchConfig.listPageSize` | LIST pagination size (`-1` disables paging) | `500` |
| `watchConfig.resyncInterval` | Periodic P2 re-LIST interval (`0` = disabled). Every resync is a full LIST | `"0"` |
| `watchConfig.seederQueryRetries` | Extra attempts for the ClickHouse startup queries | `2` |
| `watchConfig.seederQueryBackoff` | Initial retry delay, doubling each attempt | `2s` |

### Priority Tiers

Kinds not listed in a tier are P2 (LIST+WATCH). See the
[agent README](../../../README.md#priority-tiers-and-periodic-resync) for what
each tier does.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `watchConfig.tiers.p1.resyncInterval` | Default P1 re-LIST cadence. Required and must be > 0 when `p1.kinds` is set | `12h` |
| `watchConfig.tiers.p1.kinds[]` | P1 kinds; each takes `kind`, optional `apiVersion`, optional `resyncInterval` | `Namespace`, `Role`, `RoleBinding`, `ClusterRole`, `ClusterRoleBinding`, `ConfigMap`, `DuckCluster`, `Deployment` |
| `watchConfig.tiers.p3.relistInterval` | Default P3 LIST cadence. Required and must be > 0 when `p3.kinds` is set | `120h` |
| `watchConfig.tiers.p3.kinds[]` | P3 kinds; each takes `kind`, optional `apiVersion`, optional `relistInterval` | `HorizontalPodAutoscaler` |
| `watchConfig.tiers.p4.kinds[]` | WATCH-only kinds, no LIST or seeding | `Event` |
| `watchConfig.tiers.p5.kinds[]` | Kinds ignored entirely | `AuthRequest`, `Lease` |

A kind may appear in only one tier; overlaps fail at agent startup.

### Resource Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `resources.limits.cpu` | CPU limit | `2` |
| `resources.limits.memory` | Memory limit | `8Gi` |
| `resources.requests.cpu` | CPU request | `500m` |
| `resources.requests.memory` | Memory request | `1Gi` |

### Other Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `podAnnotations` | Pod annotations | `{}` |
| `podSecurityContext` | Pod security context | See values.yaml |
| `securityContext` | Container security context | See values.yaml |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |
| `logging.level` | Sets a `LOG_LEVEL` env var. **Currently inert** — the agent always logs at `info` | `info` |

### Values with no Helm equivalent

`buffer.flushConcurrency`, `status_log.*`, and `shard.*` are agent settings the
chart does not render. Supply your own ConfigMap if you need them.

## Examples

### External Cluster with ClickHouse

```yaml
cluster:
  name: "my-production-cluster"

outputs:
  clickhouse:
    enabled: true
    host: "clickhouse.example.com"
    port: 9440   # native protocol over TLS; 9000 without TLS
    database: "kseer"
    table: "kubernetes_events"
    tls: true
    tlsSkipVerify: true
    credentialsSecret:
      name: "kseer-clickhouse-creds"
  kafka:
    enabled: false
```

### Watch Specific Namespaces

```yaml
cluster:
  name: "production"
  namespaces:
    - default
    - production
    - staging
```

### Watch Specific Resources

```yaml
cluster:
  name: "production"
  resources:
    - apiVersion: "v1"
      kind: "Pod"
    - apiVersion: "v1"
      kind: "Service"
    - apiVersion: "apps/v1"
      kind: "Deployment"
    - apiVersion: "apps/v1"
      kind: "StatefulSet"
```

### Enable Periodic Resync

Each resync is a full LIST against the API server, so the cost scales with
object count. Minutes are appropriate for small clusters; prefer hours at scale.

```yaml
watchConfig:
  resyncInterval: "1h"  # Re-LIST every P2 kind hourly
```

### High-Volume Cluster Configuration

```yaml
resources:
  limits:
    cpu: 2000m
    memory: 4Gi
  requests:
    cpu: 500m
    memory: 512Mi

buffer:
  size: 500000
  flushInterval: "2s"
  retryInterval: "15s"
  batchSize: 25000
```

### Inspecting events without a datastore

To see what the agent is actually emitting, switch the output to stdout and read the
pod logs. Note that `logging.level` will not help here — it renders a `LOG_LEVEL`
environment variable the agent does not read, and the log level is fixed at `info`.

```yaml
outputs:
  clickhouse:
    enabled: false
  stdout:
    enabled: true
    pretty: true
```

For one log line per event, set `verbose_event_logging: true` in the agent's watch
config. The chart does not expose it, so that requires supplying your own ConfigMap.

## Upgrading

```bash
# Check for changes
helm diff upgrade kseer-agent ./kseer-agent -n monitoring -f values.yaml

# Upgrade the release
helm upgrade kseer-agent ./kseer-agent -n monitoring -f values.yaml

# Verify the upgrade
kubectl rollout status deployment/kseer-agent -n monitoring
```

## Troubleshooting

### View Logs

```bash
kubectl logs -n monitoring -l app.kubernetes.io/name=kseer-agent -f
```

### Check Pod Status

```bash
kubectl get pods -n monitoring -l app.kubernetes.io/name=kseer-agent
kubectl describe pod -n monitoring -l app.kubernetes.io/name=kseer-agent
```

### Common Issues

**RBAC Permissions**
If you see errors about unauthorized access:
```bash
# Check RBAC
kubectl get clusterrole -l app.kubernetes.io/name=kseer-agent -o yaml
kubectl get clusterrolebinding -l app.kubernetes.io/name=kseer-agent -o yaml
```

**ClickHouse Connection Issues**
If events aren't reaching ClickHouse:
```bash
# Test connectivity from pod
kubectl exec -n monitoring deployment/kseer-agent -- nc -zv <clickhouse-host> <clickhouse-port>

# Check configuration
kubectl get configmap -n monitoring -l app.kubernetes.io/name=kseer-agent -o yaml

# Verify credentials secret exists
kubectl get secret -n monitoring kseer-clickhouse-creds
```

## License

Apache 2.0
