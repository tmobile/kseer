# Kseer Agent

The Kseer Agent is a lightweight Kubernetes watcher that monitors objects across clusters and streams changes to configured outputs (ClickHouse, Kafka, stdout).

This is part of the Kseer project. The ClickHouse schema the agent writes into is
provisioned by [`backend/`](../backend/), and [`object-history/`](../object-history/)
renders the resulting history. See the [top-level README](../README.md) for how the
three fit together.

## Contents

- [Features](#features)
- [How It Works](#how-it-works)
- [Deployment Modes](#deployment-modes)
- [Quick Start](#quick-start)
- [Installation](#installation)
- [Configuration Reference — `config.yaml`](#configuration-reference--configyaml)
  - [How configuration is loaded](#how-configuration-is-loaded)
  - [`mode`](#mode)
  - [`clusters`](#clusters)
  - [`vault`](#vault)
  - [`outputs`](#outputs)
  - [`buffer`](#buffer)
  - [`concurrency_control`](#concurrency_control)
  - [`default_watch_config`](#default_watch_config)
  - [`status_log`](#status_log)
  - [`shard`](#shard)
  - [Environment variables](#environment-variables)
- [Configuration Reference — Helm values](#configuration-reference--helm-values)
- [Priority Tiers and Periodic Resync](#priority-tiers-and-periodic-resync)
- [Dynamic CRD Discovery](#dynamic-crd-discovery)
- [Vault Integration](#vault-integration)
- [Output Plugins](#output-plugins)
- [Event Schema](#event-schema)
- [Sharding](#sharding)
- [Observability](#observability)
- [Running](#running)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Releasing](#releasing)
- [Licensing](#licensing)

## Features

- **Multi-cluster support**: Watch many Kubernetes clusters from a single process
- **Vault integration**: Discover clusters from HashiCorp Vault with token, Kubernetes, or AppRole auth
- **Dynamic discovery**: Add clusters without restarting the agent — just update Vault
- **Flexible deployment**: Run in-cluster or externally
- **Service account auth**: Native Kubernetes authentication without kubeconfigs
- **Resource filtering**: Choose specific kinds and namespaces to watch
- **Priority tiers**: Per-kind P1–P5 sync strategies trading off freshness, durability, and API server cost
- **Periodic resync**: Re-LIST intervals that survive pod restarts, so all objects appear in time-based queries
- **Dynamic CRD discovery**: Detects and watches CRDs installed after startup
- **Multiple outputs**: Fan out to ClickHouse, Kafka, and stdout simultaneously
- **Resume on restart**: Skips the initial LIST for kinds already seeded in ClickHouse
- **Sharding**: Distribute clusters across replicas, optionally weighted by observed workload

## How It Works

The agent drives the Kubernetes **LIST** and **WATCH** endpoints directly through a dynamic client. It does **not** use client-go informers and keeps **no local object cache** — every object it emits comes from a live API response.

```
                  ┌──────────────── per cluster ────────────────┐
Kubernetes API ──►│ discovery → tier dispatch → LIST/WATCH loops │──┐
                  └─────────────────────────────────────────────┘  │
                                                                   ▼
                                            ┌──── output manager ─────┐
                                            │ default pipeline (P2/P3/P4)│──► ClickHouse
                                            │ P1 pipeline (isolated)     │──► Kafka
                                            └────────────────────────────┘──► stdout
```

**Per cluster, at startup:**

1. A CRD watcher starts so custom resources installed later are picked up.
2. If a ClickHouse output is configured, the agent queries it for the most recent `resourceVersion` per kind and the most recent `SEEDED` timestamp per kind. These let it skip redundant initial LISTs and keep resync schedules stable across restarts.
3. API discovery enumerates every resource type that supports both `list` and `watch`. Subresources are skipped.
4. Each resource type is assigned a [priority tier](#priority-tiers-and-periodic-resync), which determines whether it is LISTed, watched, both, or ignored.
5. One goroutine per resource type runs its LIST → WATCH cycle, reconnecting with exponential backoff.

**Things worth knowing:**

- **Client-side rate limiting** is fixed at **10 QPS with a burst of 15** per cluster. It is not configurable — see `pkg/watcher/watcher.go`.
- **Namespace filters are applied when events are emitted**, not when they are requested. LIST and WATCH always run cluster-wide, so a namespace filter reduces output volume but not API server load.
- **The buffer drops events when full.** A full buffer logs `Buffer full, dropping event` and discards the event rather than blocking the watcher.
- **Failed sends are not retried.** If an output returns an error, the batch is logged at `error` level and dropped.

## Deployment Modes

### 1. In-Cluster Mode (recommended for a single cluster)

Deploy inside a cluster with Helm. The agent authenticates with its projected service account token and watches the cluster it runs in.

```bash
helm install kseer-agent ./deploy/helm/kseer-agent -n monitoring --create-namespace
```

Chart-specific details — namespace ownership patterns, credential wiring, and the full
values reference — are in the [chart README](./deploy/helm/kseer-agent/README.md).

### 2. Multi-Cluster Mode (recommended for many clusters)

Run the agent externally — as a binary, container, or systemd service — pointing at many clusters via kubeconfigs, service account tokens, or Vault.

```bash
cp deploy/multi-cluster/config-example.yaml config.yaml
./kseer-agent --config=config.yaml
```

Deployment shapes for running outside a cluster — container, systemd unit, and the RBAC
needed on each watched cluster — are in the
[multi-cluster README](./deploy/multi-cluster/README.md).

> **Note:** The Helm chart also has a `mode: multi-cluster` setting, but it means something different from the agent's own `mode`. The chart's multi-cluster mode renders only RBAC and a service account token Secret, so an agent running *elsewhere* can authenticate to this cluster. It does not deploy the agent. See [Helm values](#configuration-reference--helm-values).

## Quick Start

### In-Cluster (Helm)

```bash
git clone https://github.com/kseer/kseer-agent.git
cd kseer-agent

# stdout-only install, no external dependencies
helm install kseer-agent ./deploy/helm/kseer-agent \
  -n monitoring --create-namespace \
  -f deploy/helm/kseer-agent/values-examples/minimal.yaml \
  --set cluster.name=my-cluster

kubectl logs -n monitoring -l app.kubernetes.io/name=kseer-agent -f
```

### Multi-Cluster (binary)

```bash
make build

cat > config.yaml <<'EOF'
mode: multi-cluster

clusters:
  - name: "prod"
    kubeconfig: "/home/me/.kube/config"
    context: "prod-context"

outputs:
  - type: stdout
    config:
      pretty: true
EOF

./kseer-agent --config config.yaml
```

> Kubeconfig paths are passed to the Kubernetes client verbatim. `~` is **not** expanded — always use absolute paths.

## Installation

```bash
# In-cluster
helm install kseer-agent ./deploy/helm/kseer-agent -n monitoring --create-namespace

# Multi-cluster: prebuilt binary
curl -L https://github.com/kseer/kseer-agent/releases/latest/download/kseer-agent-linux-amd64 -o kseer-agent
chmod +x kseer-agent

# Multi-cluster: from source
make build
./kseer-agent --config config.yaml
```

## Configuration Reference — `config.yaml`

This section documents every key the agent itself understands. If you deploy with Helm, you will set [Helm values](#configuration-reference--helm-values) instead, and the chart renders them into this file.

### How configuration is loaded

| Source | Behavior |
| --- | --- |
| `--config <path>` | Explicit config file. This is what the Helm chart and systemd unit use. |
| Default search | With no `--config`, the agent looks for `.kseer-agent.yaml` in `$HOME` and then the working directory. |
| Environment | Any key can be overridden with a `KSEER_`-prefixed env var, with dots replaced by underscores (`vault.address` → `KSEER_VAULT_ADDRESS`). |

Environment overrides are reliable for scalar, top-level keys. They do **not** work for list entries such as individual outputs, because Viper cannot merge into arrays. Configure outputs in the file.

### `mode`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | `in-cluster` \| `multi-cluster` | `multi-cluster` | Selects the deployment model. Any other value is a startup error. |

**`in-cluster`** allows at most one entry in `clusters`, and that entry must leave `kubeconfig` empty (the agent uses its service account). If `clusters` is omitted entirely, the agent synthesizes one named `local-cluster`. Vault is rejected in this mode.

**`multi-cluster`** requires either at least one entry in `clusters` or a `vault` block.

### `clusters`

A list of clusters to watch. Each entry authenticates with **either** `kubeconfig` **or** `service_account` — specifying both is a startup error.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | string | `local-cluster` (in-cluster) or `cluster-<index>` | Cluster identifier. Stored on every event as `cluster_name` and used as the sharding key. Set this explicitly; the generated fallbacks are rarely what you want. |
| `kubeconfig` | string | — | Absolute path to a kubeconfig file. `~` is not expanded. Empty in in-cluster mode. |
| `context` | string | current context | Named context to select within the kubeconfig. |
| `namespaces` | list of string | `[]` (all) | Restricts which namespaces produce events. Applied client-side at emit time — the agent still LISTs and WATCHes cluster-wide. |
| `resources` | list of `{apiVersion, kind}` | `[]` (all) | Restricts which resource types are watched. An empty list watches every discovered type. Both fields must match exactly. |
| `watch_config` | object | inherits `default_watch_config` | Per-cluster overrides. See [`default_watch_config`](#default_watch_config) for the field list. |
| `service_account` | object | — | Direct API server authentication, as an alternative to a kubeconfig. |

#### `clusters[].service_account`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `server` | string | *required* | API server URL, e.g. `https://cluster.example.com:6443`. |
| `token` | string | — | Bearer token. Mutually exclusive with `token_file`; exactly one is required. |
| `token_file` | string | — | Path to a file containing the bearer token. Read once at startup. |
| `ca_data` | string | — | Base64-encoded CA certificate. Mutually exclusive with `ca_file`. |
| `ca_file` | string | — | Path to a PEM CA certificate. |
| `insecure` | bool | `false` | Skips TLS verification. Required to be `true` if neither `ca_data` nor `ca_file` is set. Not for production. |

### `vault`

Optional. Enables dynamic cluster discovery and is only valid in `multi-cluster` mode. Omit the block entirely to disable it — a `vault` block without an `address` is treated as absent.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `address` | string | *required* | Vault server URL. |
| `secret_path` | string | *required* | KV path holding one secret per cluster, e.g. `secret/data/kseer/clusters`. Each secret supplies `name`, `server`, `token`, and `ca_data`. |
| `auth_method` | `token` \| `kubernetes` \| `approle` | *required* | Authentication method. Any other value is a startup error. |
| `refresh_interval` | duration | `5m` | How often the agent re-reads `secret_path` to pick up added or removed clusters. |
| `token` | string | — | Static token. Required for `token` auth unless `token_file` is set. |
| `token_file` | string | — | File containing a static token. |
| `kubernetes_auth.role` | string | *required for `kubernetes`* | Vault role bound to the agent's service account. |
| `kubernetes_auth.token_path` | string | `/var/run/secrets/kubernetes.io/serviceaccount/token` | Service account JWT used to log in to Vault. |
| `kubernetes_auth.mount_path` | string | `kubernetes` | Mount path of the Kubernetes auth backend. |
| `approle_auth.role_id` | string | — | AppRole role ID. Required for `approle` auth unless `role_id_file` is set. |
| `approle_auth.role_id_file` | string | — | File containing the role ID. |
| `approle_auth.secret_id` | string | — | AppRole secret ID. Required unless `secret_id_file` is set. |
| `approle_auth.secret_id_file` | string | — | File containing the secret ID. |
| `approle_auth.mount_path` | string | `approle` | Mount path of the AppRole auth backend. |
| `namespace` | string | — | Vault Enterprise namespace. |
| `ca_cert` | string | — | CA certificate for Vault's own TLS. |
| `tls_skip_verify` | bool | `false` | Skips verification of Vault's certificate. Not for production. |

### `outputs`

A list of destinations. At least one is required. Every event is delivered to **every** configured output. See [Output Plugins](#output-plugins) for each plugin's `config` block.

```yaml
outputs:
  - type: clickhouse
    config:
      host: "clickhouse.example.com"
      database: "kseer"
      table: "kubernetes_events"
  - type: stdout
    config:
      pretty: true
```

| Key | Type | Description |
| --- | --- | --- |
| `type` | `clickhouse` \| `kafka` \| `stdout` | Plugin name. An unknown or empty type is a startup error. |
| `config` | map | Plugin-specific settings. |

### `buffer`

Events are queued in memory, batched, and flushed to outputs.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `size` | int | `1000` | Capacity of the in-memory event channel. **When full, new events are dropped with a warning** — the watcher is never blocked. Each pipeline (default and P1) gets its own channel of this size. |
| `batch_size` | int | `1000` | Events per batch handed to outputs. A batch is flushed when it reaches this size or when `flush_interval` elapses. |
| `flush_interval` | duration | `5s` | Maximum time a partial batch waits before being sent. |
| `flush_concurrency` | int | `1` | Worker goroutines sending batches in parallel on the default pipeline. `2`–`4` is a reasonable production range. Values `<= 0` mean one in-flight batch. The P1 pipeline always uses exactly one worker. |
| `retry_interval` | duration | `30s` | **Currently inert.** The field is parsed but nothing reads it; a failed send is logged and the batch is dropped. |

### `concurrency_control`

Caps concurrent LIST operations so that a restart across many clusters does not overwhelm API servers or the agent's own memory.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Enables the global LIST and cluster-init semaphores. Note that the per-cluster LIST semaphore is derived from `max_lists_per_cluster` and applies regardless of this flag. |
| `max_global_lists` | int | `30` | Maximum concurrent LISTs across all clusters. |
| `max_lists_per_cluster` | int | `10` | Maximum concurrent LISTs within one cluster. Copied into each cluster's `watch_config.max_lists_per_cluster` at load time. |
| `max_cluster_inits` | int | `5` | Maximum clusters performing their initial discovery and seeding at the same time. |

### `default_watch_config`

Watch settings applied to every cluster that does not override them, including clusters discovered through Vault. The same fields are valid under `clusters[].watch_config`.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `initial_backoff` | duration | `1s` | First delay after a watch fails. Doubles on each consecutive failure and resets after a clean exit. |
| `max_backoff` | duration | `5m` | Upper bound on reconnect backoff. |
| `timeout_seconds` | int | `432000` (5 days) | `timeoutSeconds` sent on every WATCH request — how long the API server holds the connection before closing it cleanly. |
| `allow_bookmarks` | bool | `true` | Requests bookmark events so reconnects can resume from a recent `resourceVersion` instead of re-LISTing. **Currently cannot be disabled**: config loading forces this back to `true` for every cluster, so setting `false` has no effect. |
| `list_page_size` | int | `500` | Page size for initial LISTs. `-1` disables pagination and fetches everything in one request. Does not apply to P1 resyncs, which are never paginated. |
| `resync_interval` | duration | `0` (disabled) | P2 re-LIST cadence. See [Priority Tiers and Periodic Resync](#priority-tiers-and-periodic-resync). |
| `seeder_query_retries` | int | `2` | Additional attempts for the ClickHouse startup queries, so `2` means three attempts total. |
| `seeder_query_backoff` | duration | `2s` | Initial delay between seeder retries, doubling each attempt. With the defaults, the worst case is `2s + 4s = 6s` before falling back. |
| `max_lists_per_cluster` | int | from `concurrency_control` | Per-cluster LIST semaphore size. Normally set globally rather than here. |
| `tiers` | object | — | Per-kind tier assignments. See below. |

#### `default_watch_config.tiers`

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `p1.resync_interval` | duration | — | Default re-LIST cadence for P1 kinds. **Required and must be > 0** when `p1.kinds` is non-empty. |
| `p1.kinds` | list of entry | `[]` | Kinds in the P1 tier. |
| `p3.relist_interval` | duration | — | Default LIST cadence for P3 kinds. **Required and must be > 0** when `p3.kinds` is non-empty. |
| `p3.kinds` | list of entry | `[]` | Kinds in the P3 tier. |
| `p4.kinds` | list of entry | `[]` | Kinds in the P4 tier. No interval — P4 never relists. |
| `p5.kinds` | list of entry | `[]` | Kinds to ignore completely. |

Each entry in a `kinds` list accepts:

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `kind` | string | *required* | Kubernetes kind, e.g. `Namespace`. An empty value is a startup error. |
| `apiVersion` | string | any | Disambiguates a kind that exists in more than one API group. When omitted, the entry matches every group that defines the kind. |
| `resync_interval` | duration | tier default | P1 only — overrides `p1.resync_interval` for this kind. |
| `relist_interval` | duration | tier default | P3 only — overrides `p3.relist_interval` for this kind. |

A kind may appear in only one tier. Overlaps are rejected at startup, and an entry without `apiVersion` shadows every version of that kind for overlap purposes.

### `status_log`

Controls the periodic operator-facing status log. Not exposed by the Helm chart — set it in a config file if you need to change it.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Enables the status reporter goroutine. |
| `interval` | duration | `5m` | Time between reports. The first report is emitted one interval after startup. |
| `per_kind_detail` | bool | `true` | Also emits one `status_kind` line per cluster and kind that produced an event within the last 24 hours. |

### `shard`

Distributes clusters across multiple agent replicas. Only meaningful with Vault discovery and a StatefulSet deployment; the OSS Helm chart in this repository deploys a single-replica Deployment and does not render these keys.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `id` | int | see below | This replica's zero-based shard index. Resolved from `KSEER_SHARD_ID`, then this field, then the trailing ordinal of `HOSTNAME` (so `kseer-agent-3` becomes shard 3), then `0`. |
| `total` | int | `0` | Manual override for the shard count. Ignored when the agent computes a count from Vault. |
| `clusters_per_shard` | int | `0` | Target clusters per replica. The agent computes `ceil(clusterCount / clustersPerShard)` and patches the StatefulSet's replica count to match. `0` disables auto scaling. |
| `pods_per_shard` | int | `0` | When > 0, balances shards by observed pod volume instead of cluster count, using a 10-day window of ClickHouse history. Requires `events_table` on the ClickHouse output. Falls back to cluster-count sharding when history is unavailable. |
| `max_shards` | int | `0` (unlimited) | Caps the computed replica count. |
| `overrides` | map of string to int | `{}` | Pins a named cluster to a specific shard, bypassing the normal assignment. |

See [Sharding](#sharding) for the assignment algorithm.

### Environment variables

| Variable | Effect |
| --- | --- |
| `KSEER_<KEY>` | Overrides any config key; dots become underscores. `KSEER_MODE`, `KSEER_VAULT_ADDRESS`, `KSEER_VAULT_SECRET_PATH`, `KSEER_VAULT_NAMESPACE`, `KSEER_VAULT_AUTH_METHOD`, and `KSEER_VAULT_TLS_SKIP_VERIFY` are explicitly bound and work even when absent from the file. |
| `KSEER_CLICKHOUSE_USERNAME` | Overrides `username` on every ClickHouse output. This is how the Helm chart injects credentials from a Secret. |
| `KSEER_CLICKHOUSE_PASSWORD` | Overrides `password` on every ClickHouse output. |
| `KSEER_SHARD_ID` | Sets this replica's shard index, taking precedence over `shard.id`. |
| `KSEER_RELEASE_NAME` | Helm release name, used to locate the StatefulSet to scale during sharding. |
| `POD_NAMESPACE` | Namespace used for sharding API calls when the service account namespace file is unavailable. |
| `HOSTNAME` | Fallback source for the shard ordinal. |

> `LOG_LEVEL` and `KSEER_LOG_LEVEL` are **not** read by the agent. Logging is fixed at `info` with JSON encoding and ISO8601 timestamps. The Helm chart's `logging.level` sets a `LOG_LEVEL` env var that currently has no effect.

## Configuration Reference — Helm values

The chart lives at `deploy/helm/kseer-agent`. Helm values are camelCase and are rendered into the snake_case `config.yaml` documented above.

### Chart mode and packaging

| Value | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | `in-cluster` \| `multi-cluster` | `in-cluster` | `in-cluster` renders the full workload: ConfigMap, Deployment, credentials, RBAC. `multi-cluster` renders **only** RBAC plus a service account token Secret, so an external agent can authenticate to this cluster — no agent is deployed and all workload values are ignored. |
| `nameOverride` | string | `""` | Overrides the chart name portion of resource names. |
| `fullnameOverride` | string | `""` | Replaces the generated resource name entirely. |
| `image.repository` | string | `registry.example.com/kseer/kseer-agent` | Container image repository. |
| `image.tag` | string | `""` (chart `appVersion`) | Image tag. Pin this in production. |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | list | `[]` | Secrets for pulling from a private registry. |
| `namespace.create` | bool | `false` | Renders a Namespace as part of the release so `helm uninstall` removes it. Leave `false` and use `helm install -n <ns> --create-namespace` for the simpler pattern. In-cluster only. |
| `namespace.name` | string | `""` (chart fullname) | Namespace name when `namespace.create` is true. |

### Workload

| Value | Type | Default | Description |
| --- | --- | --- | --- |
| `resources.limits.cpu` | string/int | `2` | CPU limit. |
| `resources.limits.memory` | string | `8Gi` | Memory limit. Large clusters hold entire LIST responses in memory, so this is deliberately generous. |
| `resources.requests.cpu` | string | `500m` | CPU request. |
| `resources.requests.memory` | string | `1Gi` | Memory request. |
| `podAnnotations` | map | `{}` | Extra pod annotations. A config checksum annotation is always added so config changes trigger a rollout. |
| `podSecurityContext` | map | `runAsNonRoot: true`, `runAsUser: 65534`, `fsGroup: 65534` | Pod-level security context. |
| `securityContext` | map | no privilege escalation, all capabilities dropped, read-only root filesystem | Container-level security context. |
| `nodeSelector` | map | `{}` | Node selector. |
| `tolerations` | list | `[]` | Tolerations. |
| `affinity` | map | `{}` | Affinity rules. |
| `logging.level` | string | `info` | Renders a `LOG_LEVEL` env var. **Currently inert** — the agent does not read it. |

Liveness and readiness probes both run `pgrep -f kseer-agent`. The agent exposes no HTTP endpoints.

### RBAC and API priority

| Value | Type | Default | Description |
| --- | --- | --- | --- |
| `serviceAccount.create` | bool | `true` | Creates a ServiceAccount. |
| `serviceAccount.name` | string | `""` | Name to use or create. Defaults to the chart fullname. |
| `serviceAccount.annotations` | map | `{}` | ServiceAccount annotations, e.g. for IRSA. |
| `rbac.create` | bool | `true` | Creates the ClusterRole and ClusterRoleBinding. |
| `rbac.clusterRoleName` | string | `""` | Overrides the generated ClusterRole name. |
| `rbac.clusterRoleBindingName` | string | `""` | Overrides the generated ClusterRoleBinding name. |
| `rbac.rules` | list | `get`/`list`/`watch` on `*` | ClusterRole rules. Narrow these if you only watch specific kinds. |
| `flowSchema.enabled` | bool | `true` | Renders a FlowSchema so the agent's read traffic is isolated in its own API priority level. |
| `flowSchema.name` | string | `""` | Overrides the generated FlowSchema name. |
| `flowSchema.legacy` | bool | `false` | Uses `flowcontrol.apiserver.k8s.io/v1beta2` for clusters older than 1.29 instead of `v1`. |
| `flowSchema.matchingPrecedence` | int | `2000` | FlowSchema matching precedence. |
| `flowSchema.priorityLevel` | string | `workload-low` | Priority level configuration to bind to. |
| `tokenSecret.suffix` | string | `a` | Multi-cluster chart mode only. Suffix on the service account token Secret name; alternate between `a` and `b` for blue/green token rotation. |

### Cluster

| Value | Type | Default | Description |
| --- | --- | --- | --- |
| `cluster.name` | string | `""` | Cluster identifier stored on every event. **Set this on every install** — an empty value makes the agent fall back to `local-cluster`. |
| `cluster.namespaces` | list | `[]` (all) | Namespace filter. |
| `cluster.resources` | list of `{apiVersion, kind}` | `[]` (all) | Resource type filter. |

### Outputs

| Value | Type | Default | Description |
| --- | --- | --- | --- |
| `outputs.clickhouse.enabled` | bool | `true` | Enables the ClickHouse output. |
| `outputs.clickhouse.host` | string | `""` | ClickHouse host. Required when enabled. |
| `outputs.clickhouse.port` | int | `9000` | Native protocol port. |
| `outputs.clickhouse.database` | string | `""` | Database name. Required when enabled. |
| `outputs.clickhouse.table` | string | `""` | Destination table for events. Required when enabled. |
| `outputs.clickhouse.eventsTable` | string | `""` | Table queried at startup for resume `resourceVersion`s and last-seed timestamps. Leave empty to disable resume and restart-stable resync scheduling. |
| `outputs.clickhouse.tls` | bool | `false` | Enables TLS. |
| `outputs.clickhouse.tlsSkipVerify` | bool | `false` | Skips certificate verification. |
| `outputs.clickhouse.tableVersion` | string | `object_json` | Table schema variant: `object_json`, `object_string`, or `object_normalized`. |
| `outputs.clickhouse.batchSize` | int | `1000` | **Inert.** Rendered into the output config but never read; batching comes from `buffer.batchSize`. |
| `outputs.clickhouse.username` / `.password` | string | `""` | Credentials, used only with `credentialsSecret.create: true`. |
| `outputs.clickhouse.credentialsSecret.create` | bool | `false` | Has Helm create the credentials Secret from `username`/`password`. |
| `outputs.clickhouse.credentialsSecret.name` | string | `""` | Name of an existing or ESO-managed Secret. Defaults to `<release>-clickhouse`. Must contain `username` and `password` keys. |
| `outputs.kafka.enabled` | bool | `false` | Enables the Kafka output. |
| `outputs.kafka.brokers` | list | `["kafka:9092"]` | Broker addresses. |
| `outputs.kafka.topic` | string | `kubernetes-events` | Destination topic. |
| `outputs.kafka.compression` | string | `snappy` | One of `none`, `gzip`, `snappy`, `lz4`, `zstd`. |
| `outputs.kafka.config` | map | `{}` | Extra keys merged into the Kafka output config. |
| `outputs.stdout.enabled` | bool | `false` | Enables the stdout output. |
| `outputs.stdout.pretty` | bool | `false` | Indents the JSON. |

The chart fails at render time if no output is enabled, and if the ClickHouse credential configuration is ambiguous.

### ClickHouse credentials

Pick exactly one path:

| Path | Settings |
| --- | --- |
| **A — External Secrets Operator** | `externalSecret.enabled: true` and `credentialsSecret.create: false`. ESO pulls the credentials from Vault into a Secret. |
| **B — Helm-managed Secret** | `credentialsSecret.create: true` with `username` and `password` set. |
| **C — Pre-existing Secret** | `credentialsSecret.create: false` and `credentialsSecret.name: my-secret`. |

| Value | Type | Default | Description |
| --- | --- | --- | --- |
| `externalSecret.enabled` | bool | `false` | Renders a SecretStore and ExternalSecret pointing at Vault. |
| `externalSecret.vaultServer` | string | `""` | Vault URL. Required when enabled. |
| `externalSecret.vaultMount` | string | `""` | KV v2 mount name. |
| `externalSecret.vaultPath` | string | `""` | Path under the mount holding `username` and `password`. |
| `externalSecret.vaultRole` | string | `""` | Vault Kubernetes auth role bound to the agent's ServiceAccount. |
| `externalSecret.vaultAuthMountPath` | string | `""` | This cluster's Kubernetes auth backend mount path in Vault. |
| `externalSecret.secretStoreName` | string | `""` | Overrides the generated SecretStore name. |
| `externalSecret.refreshInterval` | string | `1h` | How often ESO re-syncs from Vault. |

### Buffer, concurrency, and watch behavior

These behave exactly like the agent keys they render into — only the naming and the defaults differ. Where a chart default differs from the agent's compiled-in default, the chart default is what you get, because the chart always writes the key into the ConfigMap.

#### Buffer

| Value | Default | Description | Agent key |
| --- | --- | --- | --- |
| `buffer.size` | `500000` | Capacity of the in-memory event queue. **Events are dropped with a warning once it fills** — the watcher is never blocked. Each pipeline (default and P1) gets its own queue of this size, so plan memory for two. The chart default is far larger than the agent's own `1000`, sized for a busy cluster. | `buffer.size` |
| `buffer.batchSize` | `25000` | Events per batch handed to the outputs. A batch is sent once it reaches this many events or when `flushInterval` elapses, whichever comes first. Larger batches mean fewer, bigger inserts. | `buffer.batch_size` |
| `buffer.flushInterval` | `5s` | Longest a partial batch waits before being sent. This is the floor on end-to-end latency for a low-traffic cluster. | `buffer.flush_interval` |
| `buffer.retryInterval` | `30s` | **Inert.** Rendered into the ConfigMap and parsed, but nothing reads it. A failed send is logged at `error` and the batch is dropped. | `buffer.retry_interval` |

#### Concurrency control

These bound how much work the agent starts at once, which matters most during a restart when every cluster wants to LIST simultaneously.

| Value | Default | Description | Agent key |
| --- | --- | --- | --- |
| `concurrencyControl.enabled` | `true` | Enables the global LIST semaphore and the cluster-init semaphore. Note that the per-cluster LIST limit below is derived separately and applies even when this is `false`. | `concurrency_control.enabled` |
| `concurrencyControl.maxGlobalLists` | `30` | Ceiling on concurrent LIST calls across every cluster. The main lever for capping peak memory, since each in-flight LIST holds a full page of objects. | `concurrency_control.max_global_lists` |
| `concurrencyControl.maxListsPerCluster` | `10` | Ceiling on concurrent LIST calls within one cluster, so a single large cluster can't consume the whole global budget. Copied into each cluster's watch config at load time. | `concurrency_control.max_lists_per_cluster` |
| `concurrencyControl.maxClusterInits` | `5` | How many clusters may run their initial discovery and seeding at the same time. Staggers a cold start across many clusters. | `concurrency_control.max_cluster_inits` |

#### Watch behavior

| Value | Default | Description | Agent key |
| --- | --- | --- | --- |
| `watchConfig.initialBackoff` | `1s` | Delay before the first reconnect after a watch fails. Doubles on each consecutive failure and resets after a clean exit. | `default_watch_config.initial_backoff` |
| `watchConfig.maxBackoff` | `5m` | Upper bound on that backoff, so a persistently unreachable cluster retries every five minutes rather than backing off forever. | `default_watch_config.max_backoff` |
| `watchConfig.timeoutSeconds` | `432000` (5 days) | `timeoutSeconds` sent on every WATCH request — how long the API server holds the connection before closing it cleanly. A long value avoids needless re-establishment; the agent reconnects from a bookmark either way. | `default_watch_config.timeout_seconds` |
| `watchConfig.allowBookmarks` | `true` | Requests bookmark events so a reconnect can resume from a recent `resourceVersion` instead of re-LISTing. **Cannot currently be disabled** — config loading forces it back to `true` for every cluster. | `default_watch_config.allow_bookmarks` |
| `watchConfig.listPageSize` | `500` | Page size for initial LISTs. Lower it to reduce peak memory on clusters with many objects; `-1` disables paging and fetches everything in one response. Does not apply to P1 resyncs, which never paginate. | `default_watch_config.list_page_size` |
| `watchConfig.resyncInterval` | `"0"` | How often P2 kinds are re-LISTed, with `0` disabling it. Every resync is a full LIST against the API server, so cost scales with object count. | `default_watch_config.resync_interval` |
| `watchConfig.seederQueryRetries` | `2` | Extra attempts for the ClickHouse startup queries that fetch resume `resourceVersion`s and last-seed timestamps, so `2` means three attempts total. On exhaustion the agent falls back to a full LIST and start-relative resync scheduling. | `default_watch_config.seeder_query_retries` |
| `watchConfig.seederQueryBackoff` | `2s` | Initial delay between those retries, doubling each attempt. With the defaults the worst case is `2s + 4s = 6s` added to startup. | `default_watch_config.seeder_query_backoff` |

#### Priority tiers

Kinds absent from every tier are P2. See [Priority Tiers and Periodic Resync](#priority-tiers-and-periodic-resync) for what each tier does; a kind listed in two tiers fails at agent startup.

| Value | Default | Description | Agent key |
| --- | --- | --- | --- |
| `watchConfig.tiers.p1.resyncInterval` | `12h` | Default re-LIST cadence for P1 kinds. **Required and must be greater than zero** whenever `p1.kinds` is non-empty. | `...tiers.p1.resync_interval` |
| `watchConfig.tiers.p1.kinds[]` | `Namespace`, `Role`, `RoleBinding`, `ClusterRole`, `ClusterRoleBinding`, `ConfigMap`, `DuckCluster`, `Deployment` | Kinds re-LISTed sequentially with a durable flush between each. Every entry takes `kind`, an optional `apiVersion` to disambiguate, and an optional `resyncInterval` overriding the tier default. Keep this list low-cardinality — P1 LISTs are never paginated. | `...tiers.p1.kinds[]` |
| `watchConfig.tiers.p3.relistInterval` | `120h` | Default LIST cadence for P3 kinds. **Required and must be greater than zero** whenever `p3.kinds` is non-empty. | `...tiers.p3.relist_interval` |
| `watchConfig.tiers.p3.kinds[]` | `HorizontalPodAutoscaler` | Kinds that are only LISTed, never watched. Entries take `kind`, optional `apiVersion`, and optional `relistInterval`. No `DELETED` events are produced for these. | `...tiers.p3.kinds[]` |
| `watchConfig.tiers.p4.kinds[]` | `Event` | Kinds watched forward from the current moment with no initial LIST and no seeding. Entries take `kind` and optional `apiVersion`. | `...tiers.p4.kinds[]` |
| `watchConfig.tiers.p5.kinds[]` | `AuthRequest`, `Lease` | Kinds ignored completely — no LIST, no WATCH, no relist, excluded from status counts. Entries take `kind` and optional `apiVersion`. | `...tiers.p5.kinds[]` |

#### Not exposed by the chart

`buffer.flushConcurrency`, `status_log.*`, and `shard.*` are agent settings the chart does not render. To use them, supply your own ConfigMap.

### Example values files

| File | Demonstrates |
| --- | --- |
| `values-examples/minimal.yaml` | stdout only, small resource footprint — good for a first install |
| `values-examples/development.yaml` | `latest` image, stdout debugging, reduced buffers |
| `values-examples/production.yaml` | pinned image, namespace and resource filters, pre-existing ClickHouse Secret, anti-affinity |
| `values-examples/internal-container-platform.yaml` | centralized ClickHouse with ESO/Vault credentials |
| `values-examples/external-vault.yaml` | chart `mode: multi-cluster` — RBAC and token Secret only, no agent |

## Priority Tiers and Periodic Resync

The agent periodically re-LISTs objects so that objects which haven't changed recently still appear in time-based queries such as "show me all objects in the last 10 minutes." Because there is no informer and no object cache, **every resync is a real LIST against the API server**.

When a resync runs:

- The agent issues a fresh LIST for the resource type.
- Each returned object is emitted as an `EXISTING` event stamped with the current time.
- A `SEEDED` sentinel is emitted once that kind's objects are durable in the output.

### Tiers

The tier assigned to a kind determines how — and whether — it is synced. Kinds not listed in any tier are P2.

| Tier | Behavior | Use for |
| --- | --- | --- |
| **P1** | LIST+WATCH plus a sequential resync loop. Each P1 kind is re-LISTed on its own interval, and the agent waits for the previous kind's events to flush durably before listing the next. P1 LISTs are **never paginated**, and P1 events use a dedicated output pipeline so this serialization doesn't stall other traffic. | Low-cardinality, high-importance kinds: `Namespace`, `Node`, `NetworkPolicy`, `ResourceQuota`, CRDs |
| **P2** (default) | LIST+WATCH with an optional `resync_interval`. When the interval elapses the watch closes and the LIST→WATCH cycle re-runs. | Most kinds |
| **P3** | No WATCH — periodic LIST only. The agent never synthesizes `DELETED` events, so clients treat anything older than one `relist_interval` as stale. | Slow-moving kinds where event-stream fidelity isn't needed |
| **P4** | WATCH-only from the current moment. No initial LIST, no relist, no `SEEDED` sentinel, and no resume from a stored `resourceVersion`. | Ephemeral, high-volume, stream-shaped kinds like `Event` |
| **P5** | Ignored completely — no LIST, no WATCH, no relist. Excluded from status counts. | High-volume, low-value kinds you don't want stored |

A P1 kind with high cardinality logs a warning above 10,000 objects, since the un-paginated LIST is held in memory.

### Schedules survive restarts

Resync schedules anchor to the most recent `SEEDED` row in ClickHouse rather than to process start. A pod that has been up four days on a five-day interval picks up where it left off instead of resetting the timer, and a kind that is already overdue resyncs immediately. This requires `events_table` on the ClickHouse output; without it, or if the query fails after its retries, the agent falls back to scheduling from startup.

### Configuration

**Agent (`config.yaml`):**

```yaml
default_watch_config:
  # Global P2 resync. 0 (the default) disables it.
  resync_interval: "0"

  tiers:
    p1:
      resync_interval: "12h"
      kinds:
        - kind: Namespace
          resync_interval: "5h"   # per-kind override
        - kind: Node
    p3:
      relist_interval: "120h"
      kinds:
        - kind: HorizontalPodAutoscaler
    p4:
      kinds:
        - kind: Event
    p5:
      kinds:
        - kind: Lease

clusters:
  - name: "production"
    kubeconfig: "/etc/kseer/prod.yaml"
    # Optional per-cluster override
    watch_config:
      resync_interval: "10m"
```

**Helm:**

```yaml
watchConfig:
  resyncInterval: "0"
  tiers:
    p1:
      resyncInterval: "12h"
      kinds:
        - kind: Namespace
          resyncInterval: "5h"
```

> **Sizing note:** because each resync is a full LIST, short intervals add real load that scales with object count. The shipped defaults disable P2 resync entirely and use intervals measured in hours for the tiers. Tune with cluster size in mind.

## Dynamic CRD Discovery

The agent watches `apiextensions.k8s.io/v1/customresourcedefinitions` and re-runs resource discovery when CRDs appear, so operators installed after startup are picked up without a restart.

- A CRD add or modify event triggers a full rediscovery.
- Newly discovered resource types that pass your filters get their own watch goroutine, using the tier assigned to their kind.
- Already-watched resources are skipped, so rediscovery is safe to repeat.
- Reconnecting the CRD watch also triggers a rediscovery, which catches CRDs added while the watch was down.

There is no debouncing — a bulk CRD install triggers one rediscovery per CRD event. Discovery is cheap and idempotent, but expect a burst of discovery logs when installing a large operator bundle.

If the CRD watcher can't start, for example due to missing RBAC, the agent logs a warning and continues with the resource types discovered at startup.

```bash
$ kubectl apply -f prometheus-operator.yaml

# In agent logs:
# INFO  CRD change detected, triggering resource discovery  crd=servicemonitors.monitoring.coreos.com
# INFO  Started watching resource  group=monitoring.coreos.com version=v1 resource=servicemonitors
```

## Vault Integration

In multi-cluster mode the agent can discover clusters from HashiCorp Vault, so adding a cluster means writing a secret rather than editing config.

**How it works:**

1. The agent authenticates to Vault at startup using token, Kubernetes, or AppRole auth.
2. It reads every secret under `secret_path`, each describing one cluster.
3. It starts a watcher per discovered cluster, applying `default_watch_config` to each.
4. It re-reads `secret_path` every `refresh_interval`, starting watchers for new clusters and stopping those that disappear or move to another shard.

**Secret layout.** One secret per cluster, at `<secret_path>/<cluster-name>`:

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Cluster identifier (should match the key name) |
| `server` | Yes | Kubernetes API server URL |
| `token` | Yes | Service account token |
| `ca_data` | No | Base64-encoded CA certificate |
| `insecure` | No | Skip TLS verification (default `false`) |

**Store cluster credentials:**

```bash
vault kv put secret/kseer/clusters/prod-us-east-1 \
  name="prod-us-east-1" \
  server="https://prod-us-east-1.k8s.example.com:6443" \
  token="eyJhbGciOiJSUzI1NiIsImtpZCI6Ii..." \
  ca_data="LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0t..." \
  insecure=false
```

Onboarding many clusters at once is just a loop over this command; there is no bulk
import path.

**Configure the agent:**

```yaml
mode: multi-cluster
clusters: []  # Vault supplies the clusters

vault:
  address: "https://vault.example.com:8200"
  auth_method: "approle"
  approle_auth:
    role_id: "12345678-1234-1234-1234-123456789012"
    secret_id_file: "/etc/kseer/vault-secret-id"
  secret_path: "secret/data/kseer/clusters"
  refresh_interval: "5m"
```

**Authentication methods:**

```yaml
# Token — development and CI
vault:
  auth_method: "token"
  token_file: "/etc/kseer/vault-token"

# Kubernetes — agent running in a cluster
vault:
  auth_method: "kubernetes"
  kubernetes_auth:
    role: "kseer-agent"

# AppRole — production
vault:
  auth_method: "approle"
  approle_auth:
    role_id_file: "/etc/kseer/vault-role-id"
    secret_id_file: "/etc/kseer/vault-secret-id"
```

Both AppRole fields accept either an inline value (`role_id`, `secret_id`) or a file
(`role_id_file`, `secret_id_file`). Prefer the file form so credentials never sit in the
config. `kubernetes_auth` additionally accepts `token_path` and `mount_path`, and
`approle_auth` accepts `mount_path`, all with sensible defaults.

### Setting up Vault

The agent needs read and list on both the data and metadata paths. Create the policy
once, then wire up whichever auth method fits your deployment.

```bash
vault policy write kseer-agent - <<EOF
path "secret/data/kseer/clusters/*" {
  capabilities = ["read", "list"]
}
path "secret/metadata/kseer/clusters/*" {
  capabilities = ["read", "list"]
}
EOF
```

**Kubernetes auth** — for an agent running inside a cluster:

```bash
vault auth enable kubernetes

vault write auth/kubernetes/config \
  kubernetes_host="https://kubernetes.default.svc:443"

vault write auth/kubernetes/role/kseer-agent \
  bound_service_account_names=kseer-agent \
  bound_service_account_namespaces=kube-system \
  policies=kseer-agent \
  ttl=1h
```

**AppRole** — for an agent running outside Kubernetes:

```bash
vault auth enable approle

vault write auth/approle/role/kseer-agent \
  token_policies=kseer-agent \
  token_ttl=1h \
  token_max_ttl=4h

vault read auth/approle/role/kseer-agent/role-id
vault write -f auth/approle/role/kseer-agent/secret-id
```

Write the resulting role ID and secret ID to the files referenced by `role_id_file` and
`secret_id_file`, and `chmod 600` them.

### Mixing Vault and static clusters

`clusters` and Vault discovery coexist. Entries listed in `clusters` are always watched;
Vault-discovered clusters are added on top and are the only ones subject to being
dropped when they disappear from `secret_path`. To migrate gradually, write each static
cluster into Vault, confirm the agent picks it up in the logs, then remove it from
`clusters`.

### Rotating tokens

Because the agent re-reads `secret_path` every `refresh_interval`, rotating a cluster
token is a `vault kv put` — no restart. The change takes effect within one refresh
interval. The same applies to removing a cluster: delete the secret and the agent stops
its watcher on the next refresh.

### Troubleshooting Vault discovery

**No clusters discovered.** Confirm the policy covers both `secret/data/...` and
`secret/metadata/...`; listing fails silently without the metadata path. Confirm
`secret_path` includes the `data/` segment for KV v2 (`secret/data/kseer/clusters`).

**A cluster is skipped.** Every secret must have `name`, `server`, and `token`. The
agent logs the cluster it rejected and why.

**Changes not picked up.** Wait one full `refresh_interval` before concluding anything;
the default is five minutes.

## Output Plugins

Every configured output receives every event. Within a batch, outputs are written in parallel.

### ClickHouse

The primary output, and the only one that supports resume-on-restart and restart-stable resync scheduling.

```yaml
- type: clickhouse
  config:
    host: "clickhouse.example.com"
    port: 9000
    database: "kseer"
    table: "kubernetes_events"
    events_table: "kubernetes_events"
    username: ""
    password: ""
    tls: false
    tls_skip_verify: false
    table_version: "object_json"
    verbose_event_logging: false
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `host` | string | *required* | ClickHouse host. Startup fails if absent. |
| `port` | int | `9000` | Native protocol port. |
| `database` | string | *required* | Database name. |
| `table` | string | *required* | Destination table. Written as `INSERT INTO <table> (timestamp, cluster_name, operation, kind, namespace, uid, object)`, so the table must define all seven of those columns. Both tables provisioned by `backend/` — the `kubernetes_events` distributed table and `kubernetes_events_local` — do. |
| `events_table` | string | `""` | Table queried at startup for the latest `resourceVersion` per kind (a 2-hour window) and the latest `SEEDED` timestamp per kind. Enables skipping the initial LIST and anchoring resync schedules across restarts. Also required for `pods_per_shard` sharding. |
| `username` / `password` | string | `""` | Credentials. Overridden by `KSEER_CLICKHOUSE_USERNAME` and `KSEER_CLICKHOUSE_PASSWORD` when those are set. |
| `tls` | bool | `false` | Enables TLS. |
| `tls_skip_verify` | bool | `false` | Skips certificate verification. |
| `table_version` | string | `object_json` | Schema variant: `object_json` (JSON column), `object_string` (String column), or `object_normalized` (structured Tuple). Any other value is a startup error. This determines the shape of both inserts and the resume queries. |
| `verbose_event_logging` | bool | `false` | Logs one line per event with kind, namespace, name, UID, and `resourceVersion`. Useful for validating a pipeline, very noisy at volume. |

Table and events-table names are validated as identifiers before being interpolated into queries. Connections use async inserts, a 10-second dial timeout, and at most two pooled connections.

> `batch_size` is **not** a ClickHouse output setting. Batching is controlled by `buffer.batch_size`. A `batch_size` key inside this config block is silently ignored.

### Kafka

```yaml
- type: kafka
  config:
    brokers: ["localhost:9092"]
    topic: "kubernetes-events"
    compression: "snappy"
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `brokers` | list of string | *required* | Broker addresses. Startup fails if absent. |
| `topic` | string | `kubernetes-events` | Destination topic. |
| `compression` | string | `snappy` | One of `none`, `gzip`, `snappy`, `lz4`, `zstd`. An unrecognized value leaves compression at the client default. |

Events are published as JSON with an async producer using `WaitForLocal` acks, three retries, and 30-second network timeouts.

> `batch_size` appears in some older examples but is **not** read from config; the producer flushes every 100 messages.

### Stdout

```yaml
- type: stdout
  config:
    pretty: false
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `pretty` | bool | `false` | Indents the JSON. Leave `false` for line-delimited output. |

## Event Schema

Every output receives the same structure. The event envelope has exactly four fields, and the full Kubernetes object is nested under `object`:

```json
{
  "timestamp": "2026-01-01T12:00:00Z",
  "cluster_name": "production",
  "operation": "MODIFIED",
  "object": {
    "apiVersion": "apps/v1",
    "kind": "Deployment",
    "metadata": {
      "name": "nginx",
      "namespace": "default",
      "uid": "12345-67890",
      "resourceVersion": "123456",
      "labels": { "app": "nginx" }
    },
    "spec": {},
    "status": {}
  }
}
```

Fields such as `namespace`, `kind`, `uid`, and `labels` are **not** top-level — read them from inside `object`. The ClickHouse output projects `kind`, `namespace`, and `uid` into their own columns for query performance.

### Operations

| Operation | Meaning |
| --- | --- |
| `ADDED` | Object created, observed on the watch stream. |
| `MODIFIED` | Object updated, observed on the watch stream. |
| `DELETED` | Object deleted, observed on the watch stream. Never synthesized — a P3 kind or a missed watch window produces no delete. |
| `EXISTING` | Object present in a LIST: the initial seed, a P1 resync, a P2 re-LIST, or a P3 relist. |
| `SEEDED` | Sentinel written after a kind's LIST is durable in the output. Carries only the kind and the snapshot `resourceVersion`. Consumers should filter these out of object queries; the agent uses them to anchor resync schedules and to detect partial seeds. |

## Sharding

With Vault discovery, clusters can be spread across replicas. Each replica computes assignments independently and deterministically, so no coordination is needed.

**Assignment**, in priority order:

1. **Explicit override.** A cluster named in `shard.overrides` goes to the shard listed there.
2. **Pod-weighted bin packing.** When `pods_per_shard > 0` and ClickHouse pod history is available, clusters are packed into shards using a largest-first heuristic, so a few large clusters don't land on one replica. Ties break alphabetically to keep the result identical across pods.
3. **Sorted modulo.** Otherwise clusters are sorted by name and cluster *i* goes to shard *i mod total*. This bounds each replica at `ceil(N / total)` clusters.

**Replica count** comes from the Vault cluster list: `ceil(totalPods / pods_per_shard)` when weighting is active, otherwise `ceil(clusterCount / clusters_per_shard)`, clamped to at least 1, at most `max_shards`, and never more than the cluster count. When the agent has in-cluster credentials and `KSEER_RELEASE_NAME` is set, it patches the StatefulSet's replica count to match.

The pod-weighted window is 10 days, ending at the date in the StatefulSet's `kseer.io/shard-window-anchor` annotation. All replicas read the same anchor so they compute the same assignment; without the annotation they use today's date.

## Observability

### Status log

Every `status_log.interval` (default 5 minutes) the agent logs a `status` line per cluster:

| Field | Meaning |
| --- | --- |
| `cluster` | Cluster name |
| `kinds` | Total kinds being handled |
| `p1`, `p2`, `p3` | Kind counts per tier |
| `shard` | This replica's shard and the total, e.g. `0/4` |
| `last_event` | How long ago the most recent event arrived, or `never` |

With `per_kind_detail` enabled it also logs a `status_kind` line for each kind that produced an event in the last 24 hours, with its tier, event count, and time since the last event. `SEEDED` sentinels don't count toward these stats. Silent kinds are omitted from the detail lines but still counted in the summary.

### Logs

Logs are JSON with ISO8601 timestamps, written to stderr at `info` level. Sampling is disabled so high-volume per-event logging is never silently truncated. The level is not currently configurable.

### Metrics and health endpoints

The agent exposes **no HTTP server** — there is no `/metrics` and no `/health`. Kubernetes liveness and readiness are determined by `pgrep`. Prometheus metrics are not yet implemented.

## Running

### In-cluster (Helm)

```bash
helm install kseer-agent ./deploy/helm/kseer-agent -n monitoring --create-namespace
kubectl logs -n monitoring -l app.kubernetes.io/name=kseer-agent -f
helm upgrade kseer-agent ./deploy/helm/kseer-agent -n monitoring -f custom-values.yaml
helm uninstall kseer-agent -n monitoring
```

### Multi-cluster

```bash
# Binary
make build
./kseer-agent --config config.yaml

# Docker
docker build -t kseer-agent .
docker run -v $(pwd)/config.yaml:/config.yaml:ro \
  -v $HOME/.kube:/kube:ro \
  kseer-agent --config /config.yaml

# Docker Compose
cd deploy/multi-cluster && docker-compose up -d

# systemd
sudo cp deploy/multi-cluster/systemd/kseer-agent.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now kseer-agent
sudo journalctl -u kseer-agent -f
```

## Troubleshooting

### Agent won't start

Startup validation errors are fatal and name the offending key. Common ones:

| Message | Cause |
| --- | --- |
| `no outputs configured` | The `outputs` list is empty. |
| `unknown output type: x` | Typo in `type`; valid values are `clickhouse`, `kafka`, `stdout`. |
| `must specify either 'kubeconfig' or 'service_account'` | A cluster entry has neither, or has both. |
| `in-cluster mode should not specify kubeconfig path` | Remove `kubeconfig` from the cluster entry. |
| `tiers.p1.resync_interval must be > 0` | P1 kinds are listed without a tier interval. |
| `kind "X" appears in both tiers.p1 and tiers.p3` | A kind is assigned to two tiers. |
| `clickhouse host not configured` | The ClickHouse output is missing `host`, `database`, or `table`. |

Then check connectivity and permissions:

```bash
kubectl cluster-info
kubectl auth can-i list pods --all-namespaces
kubectl auth can-i watch customresourcedefinitions
```

### Kubeconfig not found

The agent passes the path straight to the Kubernetes client and does **not** expand `~`. Use `/home/me/.kube/config`, not `~/.kube/config`.

### Events aren't appearing

1. Add a `stdout` output temporarily to confirm the agent is producing events at all.
2. Check whether the kind is assigned to P5 (ignored) or P4 (no backfill of existing objects).
3. Confirm the namespace and resource filters aren't excluding it.
4. Look for `Buffer full, dropping event` warnings — raise `buffer.size` or `buffer.flush_concurrency`.
5. Look for `Failed to send events` errors, which indicate the batch was dropped without retry.

### CRDs aren't being watched

1. Confirm RBAC allows watching CustomResourceDefinitions.
2. Look for a `Failed to start CRD watcher` warning at startup.
3. Verify the CRD is `Established`: `kubectl get crd`.
4. Check that the kind isn't in the P5 tier.

### High memory usage

1. Lower `buffer.size` — with large batches this dominates the footprint.
2. Move high-cardinality kinds out of P1, which never paginates and holds a full LIST in memory.
3. Lower `list_page_size` or narrow `resources`.
4. Lower `concurrency_control.max_global_lists` so fewer LIST responses are in flight at once.

### API server load

1. Increase or disable `resync_interval` — every resync is a full LIST.
2. Move slow-moving kinds to P3 with a long `relist_interval`.
3. Move noisy kinds you don't need to P5.
4. Keep the FlowSchema enabled so the agent's reads stay in their own priority level.

## Development

```bash
make build          # build the binary
make test           # run tests
make docker-build   # build the image
make helm-lint      # lint the chart
make helm-template  # render the chart to stdout
```

### Building behind a TLS-inspecting proxy

The image build installs packages from the Alpine CDN, which fails with
`TLS: server certificate not trusted` on a corporate network that intercepts TLS. Point
`CORP_CA_URL` at your root CA and the builder stage will trust it:

```bash
make docker-build CORP_CA_URL=https://ca.example.com/root-ca.pem
```

It also works as an environment variable, so `test/local-kind-e2e.sh` picks it up without
extra flags:

```bash
export CORP_CA_URL=https://ca.example.com/root-ca.pem
```

### Running locally against a real cluster

`scripts/local-debug/run-local.sh` is the fastest way to watch a live cluster and see
events land in ClickHouse. It stands up a throwaway ClickHouse in Docker, applies
`schema.sql`, generates a config pointed at it, and runs the agent with `go run`.

```bash
export K8S_API_SERVER="https://my-cluster:6443"
export K8S_TOKEN="<service account token>"   # prompted if unset, input hidden

./scripts/local-debug/run-local.sh
```

The local ClickHouse publishes the native protocol on host port **9100** by default
(not 9000, to avoid colliding with VPNs and proxies that squat on it) and the HTTP
interface on **8123**. Database `kseer`, table `kubernetes_events`, user `default` with
no password.

Useful overrides, all environment variables: `CLUSTER_NAME`, `K8S_NAMESPACES` (a
comma-separated allowlist), `RESYNC_INTERVAL`, `VERBOSE_EVENTS`, `CH_HOST`, `CH_PORT`,
`CH_HTTP_PORT`, `CH_DB`, `CH_TABLE`, `CH_TABLE_VERSION`, and `SKIP_CLICKHOUSE=true` to
reuse a ClickHouse you already have running.

ClickHouse data persists in a Docker volume across runs, which is the point: on the
second run you can watch kinds resume from their stored resource version and skip the
initial LIST, which is the behavior most worth debugging.

Query what landed:

```bash
docker exec -it kseer-debug-clickhouse clickhouse-client \
  --query "SELECT count() FROM kseer.kubernetes_events"

docker exec -it kseer-debug-clickhouse clickhouse-client \
  --query "SELECT timestamp, cluster_name, operation, kind, namespace
           FROM kseer.kubernetes_events ORDER BY timestamp DESC LIMIT 10"
```

Tear down with `docker compose -f scripts/local-debug/docker-compose.yaml down -v`.

### Credentials during development

ClickHouse credentials can come from the environment rather than the config file:

```bash
export KSEER_CLICKHOUSE_USERNAME="clickhouse"
export KSEER_CLICKHOUSE_PASSWORD="clickhouse"
```

Vault settings work the same way:

```bash
export KSEER_VAULT_ADDRESS="https://vault.example.com"
export KSEER_VAULT_SECRET_PATH="secret/data/kseer/clusters"
export KSEER_VAULT_NAMESPACE="my-namespace"     # Vault Enterprise only
```

Environment variables override **scalar** keys only. They cannot populate list entries,
and `outputs` is a list — so `host`, `port`, `database`, and `table` must be set in
`config.yaml`. See [Environment variables](#environment-variables) for the precedence
rules and the full set of recognized names.

### End-to-end test in kind

`test/local-kind-e2e.sh` stands up a kind cluster and a ClickHouse container, installs
the chart, and asserts that events actually arrive. It is what CI runs as its smoke
test. See the [test README](./test/README.md) for the broader multi-cluster test
harness and the stress tests.

### Adding an output plugin

Implement the `Output` interface and register the factory in an `init` function:

```go
package myoutput

import (
    "context"

    "github.com/kseer/kseer-agent/pkg/outputs"
    "github.com/kseer/kseer-agent/pkg/types"
    "go.uber.org/zap"
)

func init() {
    outputs.Register("myoutput", NewMyOutput)
}

func NewMyOutput(config map[string]interface{}, logger *zap.Logger) (outputs.Output, error) {
    // parse config, return the output
}

func (m *MyOutput) Send(ctx context.Context, events []types.Event) error { ... }
func (m *MyOutput) Close() error                                        { ... }
func (m *MyOutput) Name() string                                        { return "myoutput" }
```

Then add a blank import to `cmd/agent/main.go` so the `init` runs:

```go
_ "github.com/kseer/kseer-agent/pkg/outputs/myoutput"
```

`Send` receives a whole batch and should be safe to call from multiple goroutines when `buffer.flush_concurrency > 1`.

## Releasing

Trunk-based development: `main` is always releasable, and **a release is a git tag**.
There are no release branches — they will be added only when a previous minor actually
needs maintaining.

Pick the next version with semver:

| Change since last tag | Bump |
|---|---|
| Breaking API or values changes | major (`v0.x.y` → `v1.0.0`) |
| New feature, backward-compatible | minor (`v0.3.0` → `v0.4.0`) |
| Bug fix only | patch (`v0.3.0` → `v0.3.1`) |

The chart `version`, the chart `appVersion`, and the image tag are all identical for a
release, derived from the tag at CI time. Version bumps are never committed to
`Chart.yaml`; CI rewrites it transiently when packaging.

To cut a release, confirm `main` is green, review what has landed, then tag:

```bash
git log $(git describe --tags --abbrev=0)..HEAD --oneline

git fetch origin
git checkout main && git pull
git tag v0.4.0
git push origin v0.4.0
```

The tag pipeline runs four stages:

- **validate** — `helm lint`, `helm template` against both example shapes, `go test ./...`
- **build** — builds and pushes the agent image tagged with the version and `latest`
- **test** (`smoke:kind`) — manual; runs `test/local-kind-e2e.sh` against the freshly
  published image in a kind cluster
- **release** — stamps `Chart.yaml`, packages, and publishes the chart tarball

Afterwards, verify the image pulls and the chart appears in the registry, and tell any
consumer that pins `image.tag` to move.

The smoke test is manual because it needs a Docker-in-Docker-capable runner, and it is
marked `allow_failure`, so the chart still publishes if it goes red. Treat a red
`smoke:kind` as "investigate before telling users to install," not "release blocked."

To exercise the pipeline without releasing, push a throwaway tag. Note that
`v0.0.0-citest` does **not** match the release regex `^v\d+\.\d+\.\d+$`, so only the
validate stage fires; use a real-shape tag like `v0.0.999` to test build and publish,
then delete it.

```bash
git tag v0.0.0-citest
git push origin v0.0.0-citest

# clean up after checking the pipeline
git tag -d v0.0.0-citest
git push origin :refs/tags/v0.0.0-citest
```

## Licensing

Copyright 2026 T-Mobile US, Inc.

The source code in this repository, including the database setup, schema-management, migration, and related executable scripts, is licensed under the Apache License, Version 2.0. See the [LICENSE](../LICENSE) file for details.

The documentation in this repository, including this README, is licensed under the Creative Commons Attribution 4.0 International License. See the [LICENSE-docs](../LICENSE-docs) file for details.
