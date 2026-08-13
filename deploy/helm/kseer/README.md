# kseer — unified Helm chart

One release that deploys any combination of the platform's parts. Each is
independently switchable, so the same chart covers a laptop evaluation, an
agent-only install on a watched cluster, and a production deployment against an
existing ClickHouse.

| Component | Value | Default | What it deploys |
|-----------|-------|---------|-----------------|
| ClickHouse | `clickhouse.enabled` | `false` | A single-node ClickHouse StatefulSet, for evaluation only |
| Schema | `schema.enabled` | `false` | A Job that creates the `kubernetes_events` table(s) |
| Agent | `agent.enabled` | `true` | The watcher, via the `kseer-agent` subchart |
| UI | `ui.enabled` | `true` | nginx serving the object-history app and proxying queries |

The default values enable the agent and the UI, which is the common case: both
run in the cluster, ClickHouse lives somewhere else. That combination still
needs to be told where ClickHouse is, so a bare `helm install` with no values
stops with a message naming what to set.

## Prerequisites

- Kubernetes 1.29 or newer (the agent's FlowSchema uses `flowcontrol/v1`; set
  `agent.flowSchema.legacy=true` for older clusters).
- Helm 3.8 or newer.
- Images for the agent and UI. No public images are published yet, so build and
  push them first:

```bash
make -C kseer-agent docker-build docker-push IMAGE_REPO=<registry>/kseer-agent
docker build -t <registry>/kseer-object-history:<tag> object-history/
docker push <registry>/kseer-object-history:<tag>
```

  Behind a TLS-inspecting proxy, pass your root CA to the build:
  `--build-arg CORP_CA_URL=https://ca.example.com/root-ca.pem`, or
  `CORP_CA_URL=... make -C kseer-agent docker-build`.

- A ClickHouse endpoint, unless you enable the bundled one.

### Vendor the subchart first

The agent is not reimplemented here; the chart depends on the chart that ships
with the agent source. Helm needs that dependency vendored into `charts/`
before the first install, and after any change to the agent chart:

```bash
helm dependency update deploy/helm/kseer
```

Skipping it produces `found in Chart.yaml, but missing in charts/ directory`.
`charts/` and `Chart.lock` are build output and are not committed.

## Deploy

### Evaluate everything on a scratch cluster

Brings up ClickHouse, creates the schema, watches the local cluster, and serves
the UI. The bundled ClickHouse has no replication and no backups — see
[Bundled ClickHouse](#bundled-clickhouse).

```bash
helm dependency update deploy/helm/kseer

helm install kseer deploy/helm/kseer \
  -n kseer --create-namespace \
  -f deploy/helm/kseer/values-examples/evaluation.yaml \
  --set clickhouse.auth.password="$(openssl rand -base64 24)" \
  --set agent.cluster.name=my-cluster \
  --set agent.image.repository=<registry>/kseer-agent \
  --set ui.image.repository=<registry>/kseer-object-history
```

The example file assumes the release is named `kseer`, because the agent has to
be given the ClickHouse service name and Helm cannot compute a value for a
subchart. Under a different release name, update
`agent.outputs.clickhouse.host` and `agent.outputs.clickhouse.credentialsSecret.name`
to match.

Then watch it come up:

```bash
kubectl -n kseer get pods -w
kubectl -n kseer logs -f job/kseer-schema-1          # schema creation
kubectl -n kseer port-forward svc/kseer-ui 8080:80   # then open localhost:8080
```

Log in with the ClickHouse username and password you set. On a fresh install
the agent restarts a few times while it waits for ClickHouse — that is
expected, see [Troubleshooting](#troubleshooting).

### Deploy against an existing ClickHouse

The production shape. Credentials come from Secrets you create yourself, so
nothing sensitive ends up in a values file or in the release.

```bash
kubectl create namespace kseer

# The agent needs INSERT; the schema Job needs CREATE TABLE.
kubectl -n kseer create secret generic clickhouse-writer \
  --from-literal=username=kseer_writer --from-literal=password=<password>
kubectl -n kseer create secret generic clickhouse-admin \
  --from-literal=username=kseer_admin --from-literal=password=<password>

helm dependency update deploy/helm/kseer
helm install kseer deploy/helm/kseer -n kseer \
  -f deploy/helm/kseer/values-examples/external-clickhouse.yaml \
  --set agent.cluster.name=prod-us-east-1
```

Read [`values-examples/external-clickhouse.yaml`](values-examples/external-clickhouse.yaml)
before running it: it points at `clickhouse.example.com`, asks for
`schema.mode=distributed`, and turns on an Ingress for the UI.

### Fleet: one agent per cluster, one UI in the middle

Install [`values-examples/agent-only.yaml`](values-examples/agent-only.yaml) in
each watched cluster, changing `agent.cluster.name` every time — that value
becomes the `cluster_name` column and is how the UI tells clusters apart. Then
install [`values-examples/ui-only.yaml`](values-examples/ui-only.yaml) once,
wherever you want the UI, plus the schema Job once against the shared
ClickHouse.

The agent also supports watching many clusters from a single deployment using
credentials from Vault, which is a different topology; see
[`kseer-agent/README.md`](../../../kseer-agent/README.md).

## How the components fit together

Three of the four talk to ClickHouse, and they have to agree or the pipeline
silently goes nowhere — the agent writes to one table while the UI reads
another, and all you see is an empty graph. The chart therefore refuses to
render a release whose components disagree.

The agent connects over the **native protocol** (port 9000, or 9440 with TLS),
while the schema Job and the UI's nginx use the **HTTP interface** (8123, or
8443 with TLS). Those are separate ports on the same server, which is why
`agent.outputs.clickhouse.host` is a bare hostname and the other two take full
URLs.

```
              agent.outputs.clickhouse.host   ──native 9000──┐
schema.clickhouse.url ─────────────────────────HTTP 8123────▶│ ClickHouse
browser ──▶ UI (nginx /clickhouse) ─ ui.clickhouse.url ──────┘
```

These must line up, and the chart checks each one at render time:

| Must match | Values involved |
|------------|-----------------|
| Database | `schema.database`, `ui.clickhouse.database`, `agent.outputs.clickhouse.database` |
| Table | `ui.clickhouse.table`, `agent.outputs.clickhouse.table`, always `kubernetes_events` when the schema Job runs |
| Endpoint | `schema.clickhouse.url`, `ui.clickhouse.url`, `agent.outputs.clickhouse.host` |

Every guard names the values it is unhappy about and what to set. They are all
in [`templates/_validations.tpl`](templates/_validations.tpl) if you want to
read them; the ones people hit most are a `distributed` schema aimed at the
bundled single node, a bundled ClickHouse with no password, and a UI or agent
with no endpoint.

## Configuration

Only the values that need explanation are listed. Every key, with comments, is
in [`values.yaml`](values.yaml).

### Chart-wide

| Key | Default | Description |
|-----|---------|-------------|
| `nameOverride` / `fullnameOverride` | `""` | Override the generated resource name prefix |
| `imagePullSecrets` | `[]` | Applied to every pod, the agent's included |
| `labels.{deployment,pod,service}` | `{}` | Extra labels by resource kind |
| `annotations.{deployment,pod,service}` | `{}` | Extra annotations by resource kind |
| `nodeSelector` / `tolerations` / `affinity` | empty | Scheduling defaults for this chart's pods |

Each component has the same five keys. A component's value wins where it is
set; otherwise the chart-wide one applies. The agent subchart does not read
these — it has its own `agent.nodeSelector` and friends.

### Bundled ClickHouse

A single pod so you can watch the whole pipeline work without provisioning a
datastore. No replication, no Keeper, no backups, and no upgrade path. For
anything you intend to keep, leave it off and point the other components at a
real deployment.

| Key | Default | Description |
|-----|---------|-------------|
| `clickhouse.enabled` | `false` | Deploy the bundled server |
| `clickhouse.replicaCount` | `1` | Must stay 1; more requires Keeper |
| `clickhouse.image.{repository,tag,pullPolicy}` | `clickhouse/clickhouse-server:25.8.16.34` | Server image |
| `clickhouse.database` | `kseer` | Created on first start; keep equal to `schema.database` |
| `clickhouse.auth.username` | `kseer` | Admin user, created on first start |
| `clickhouse.auth.password` | `""` | Required unless `existingSecret` is set |
| `clickhouse.auth.existingSecret` | `""` | Secret with `username` and `password` keys |
| `clickhouse.service.{type,httpPort,nativePort}` | `ClusterIP`, `8123`, `9000` | Both protocols on one Service |
| `clickhouse.persistence.enabled` | `true` | Off means an emptyDir, wiped on restart |
| `clickhouse.persistence.{storageClass,size}` | `""`, `50Gi` | Empty class uses the cluster default |
| `clickhouse.settings.logLevel` | `information` | `trace`, `debug`, `information`, `warning`, `error` |
| `clickhouse.settings.maxMemoryUsage` | `4000000000` | Per-query ceiling, in bytes |
| `clickhouse.settings.maxThreads` | `4` | Threads per query |
| `clickhouse.resources` | 2 CPU / 6Gi limit | Keep `maxMemoryUsage` well under the memory limit |
| `clickhouse.podSecurityContext` | uid/gid 101 | 101 is the `clickhouse` user in the image |

Naming the admin user anything other than `default` makes the image's
entrypoint replace its built-in passwordless account rather than leave it
alongside yours. Keep `maxMemoryUsage` comfortably below the memory limit, or
ClickHouse gets OOM-killed instead of returning a clean "memory limit
exceeded". Note that `helm uninstall` does not delete the PVC — that is
Kubernetes' StatefulSet behavior, and it is what lets you reinstall without
losing data.

### Schema Job

Runs the same DDL as [`backend/`](../../../backend/), from inside the cluster: it
waits for ClickHouse to answer, runs the same preflight checks, then applies
the tables. `${database}` and `${cluster}` are substituted at render time, so
the container needs nothing but curl.

The DDL is vendored into [`files/sql/`](files/sql) because Helm cannot read
files outside a chart directory. [`backend/sql/`](../../../backend/sql/) remains
the source of truth, so a change there has to be copied across:

```bash
cp -R backend/sql/single backend/sql/distributed deploy/helm/kseer/files/sql/
diff -r backend/sql deploy/helm/kseer/files/sql   # should print nothing
```

The Job's name carries the release revision, so `helm upgrade` runs it again
and prunes the previous one. Every statement is `CREATE ... IF NOT EXISTS`, so
re-running is safe and keeps the schema reconciled. Disable it after the first
install if you would rather manage the schema by hand.

| Key | Default | Description |
|-----|---------|-------------|
| `schema.enabled` | `false` | Run the Job |
| `schema.mode` | `single` | `single` or `distributed` |
| `schema.database` | `kseer` | Target database |
| `schema.cluster` | `default` | `distributed` only: a name from `system.clusters` |
| `schema.createDatabase` | `false` | Issue `CREATE DATABASE IF NOT EXISTS` first |
| `schema.dropExisting` | `false` | Drop the tables first — deletes all history |
| `schema.waitTimeoutSeconds` | `300` | How long to wait for ClickHouse to answer |
| `schema.backoffLimit` | `3` | Pod retries before the Job fails |
| `schema.ttlSecondsAfterFinished` | `""` | Empty keeps the finished Job and its logs |
| `schema.clickhouse.url` | `""` | HTTP endpoint; empty uses the bundled server |
| `schema.clickhouse.tlsSkipVerify` | `false` | For an unverifiable HTTPS certificate |
| `schema.clickhouse.auth.{username,password}` | `""` | Needs `CREATE TABLE` on the database |
| `schema.clickhouse.auth.existingSecret` | `""` | Secret with `username` and `password` keys |
| `schema.image.{repository,tag,pullPolicy}` | `curlimages/curl:8.11.1` | Any image with curl and a POSIX shell |

`mode` picks the topology. `single` creates one plain MergeTree table.
`distributed` creates ReplicatedMergeTree storage plus a Distributed table
`ON CLUSTER`, which needs a real cluster with ClickHouse Keeper — it cannot be
applied to the bundled server, and the chart says so rather than letting the
DDL fail. Both modes expose the same `kubernetes_events` table with the same
columns, so the agent and UI are configured identically either way.

When the bundled ClickHouse is in use the Job reuses its admin Secret, so
credentials live in exactly one place and `schema.clickhouse.auth` can be left
empty.

`dropExisting` deserves care. It is how you switch topology on an existing
install, since `CREATE ... IF NOT EXISTS` would otherwise leave the old table
in place and the agent would keep writing to a table that is no longer part of
the intended layout. Because the Job re-runs on every `helm upgrade`, leaving
it on wipes the data every time. Set it for the one upgrade that performs the
switch, then set it back.

### UI

A static bundle served by nginx, which also proxies `/clickhouse`. That proxy
is what keeps ClickHouse off the public internet and out of CORS
configuration: the browser only ever talks to this pod.

| Key | Default | Description |
|-----|---------|-------------|
| `ui.enabled` | `true` | Deploy the UI |
| `ui.replicaCount` | `1` | Stateless, so scale freely |
| `ui.image.{repository,tag,pullPolicy}` | `registry.example.com/...` | Empty tag uses the chart appVersion |
| `ui.container.containerPort` | `8080` | Unprivileged, so the pod runs as non-root |
| `ui.service.{type,port,nodePort}` | `ClusterIP`, `80` | `nodePort` applies to `type: NodePort` |
| `ui.ingress.enabled` | `false` | Create an Ingress |
| `ui.ingress.{className,host,path,pathType}` | `""`, `""`, `/`, `Prefix` | `host` is required when enabled |
| `ui.ingress.annotations` | `{}` | Controller-specific settings |
| `ui.ingress.tls` | `[]` | Standard Ingress TLS list |
| `ui.clickhouse.url` | `""` | Proxy target; empty uses the bundled server |
| `ui.clickhouse.database` | `kseer` | Must match `schema.database` |
| `ui.clickhouse.table` | `kubernetes_events` | Must match what the agent writes |
| `ui.clickhouse.sslVerify` | `true` | False skips certificate verification upstream |
| `ui.clickhouse.maxQuerySize` | `10m` | Ceiling on one query body |
| `ui.clickhouse.proxyReadTimeout` | `300s` | Long time ranges take a while to return |
| `ui.azure.enabled` | `false` | Microsoft Entra sign-in |
| `ui.azure.{tenantId,clientId,proxyClientId,useIdToken}` | `""` | Required when `azure.enabled` |

The image is built once and configured at deploy time. `index.html` loads
`/env-config.js` before the app starts, and the chart serves that file from a
ConfigMap, so changing the database or table is a values change and a pod roll
rather than a rebuild. The same ConfigMap holds `nginx.conf`, which is why the
Deployment carries a checksum annotation: nginx reads its configuration only at
startup.

Authentication has two shapes. By default the UI shows a login form and sends
what the user types as HTTP Basic credentials, which **ClickHouse itself**
authenticates — so access control is ClickHouse users and grants, and the UI
holds nothing. Set `ui.azure.enabled=true` and the UI acquires a token and
sends it as a bearer token instead, which only works if `ui.clickhouse.url`
points at a proxy that validates the token and maps it to a ClickHouse
identity. This repository does not ship that proxy.

### Agent

Everything under `agent` goes to the `kseer-agent` subchart, so any value in
[`kseer-agent/deploy/helm/kseer-agent/values.yaml`](../../../kseer-agent/deploy/helm/kseer-agent/values.yaml)
can be set here. This chart's `values.yaml` repeats only the ones you almost
always have to set.

| Key | Default | Description |
|-----|---------|-------------|
| `agent.enabled` | `true` | Deploy the agent |
| `agent.cluster.name` | `""` | Goes in the `cluster_name` column; set it per install |
| `agent.image.{repository,tag}` | `registry.example.com/...` | Empty tag uses the subchart appVersion |
| `agent.outputs.clickhouse.host` | `""` | Native-protocol host, no scheme or port |
| `agent.outputs.clickhouse.port` | `9000` | `9440` for TLS |
| `agent.outputs.clickhouse.{database,table}` | `kseer`, `kubernetes_events` | Must match the schema and UI |
| `agent.outputs.clickhouse.eventsTable` | `kubernetes_events` | Resume point after a restart; empty disables |
| `agent.outputs.clickhouse.credentialsSecret.{create,name}` | `false`, `""` | Where credentials come from |
| `agent.mode` | `in-cluster` | Or `multi-cluster`: RBAC and a token Secret only |
| `agent.watchConfig.tiers` | see subchart | Per-kind sync strategy |

`agent.cluster.name` is worth setting deliberately. Leave it empty and the
agent falls back to `local-cluster`, which is fine for one throwaway install
and confusing the moment a second cluster reports into the same table.

`eventsTable` lets the agent query the last stored `resourceVersion` per kind
on startup and resume from there instead of re-listing every object, which
matters on large clusters. It needs the events table to exist, so leave it
empty if the schema is not in place yet.

Credentials come from one of three places, and the subchart insists you pick
exactly one: reference an existing Secret with
`credentialsSecret.name` (including the bundled ClickHouse's, named
`<release>-clickhouse`), have Helm create one from `username`/`password` with
`credentialsSecret.create=true`, or let the External Secrets Operator sync it
from Vault with `agent.externalSecret.enabled=true`. Either way the Secret
needs `username` and `password` keys.

For deployment modes, priority tiers, Vault-based multi-cluster discovery, and
the event schema, see [`kseer-agent/README.md`](../../../kseer-agent/README.md).

## Operating

**Upgrading.** `helm upgrade` with the same flags. The schema Job runs again
(harmless, and it keeps the schema reconciled), and nginx and ClickHouse pods
roll when their configuration changes. Re-run `helm dependency update` after
changing the agent chart.

**Switching schema topology.** Set `schema.mode` to the other value and
`schema.dropExisting=true` for that one upgrade, then set `dropExisting` back.
This discards stored history, so export anything you need first. Going from
`distributed` to `single` leaves `kubernetes_events_local` behind, since single
mode only manages `kubernetes_events` and will not drop a table it did not
create; drop it by hand once you are sure:
`DROP TABLE IF EXISTS <database>.kubernetes_events_local SYNC`.

**Turning a component off.** Set its `enabled` to `false` and upgrade; Helm
removes its resources. Turning off everything is rejected, since the release
would create nothing.

**Uninstalling.** `helm uninstall kseer -n kseer`. The bundled ClickHouse's PVC
survives, as StatefulSet PVCs always do — delete it explicitly to reclaim the
storage.

## Troubleshooting

**The agent restarts a few times right after a fresh install.** Expected. The
agent pings ClickHouse at startup and exits if it cannot reach it, and on a
fresh install both start at the same moment. Kubernetes backs off and retries,
and the agent settles once ClickHouse is ready. If it is still restarting after
a few minutes, read the log — a wrong host, database, or password fails the
same way:

```bash
kubectl -n kseer logs -l app.kubernetes.io/name=agent,app.kubernetes.io/instance=kseer
```

That selector uses `name=agent` because the dependency is aliased to `agent`,
which is also why the subchart's resources are labelled that way.

**The schema Job sits in `Waiting for ClickHouse ...`.** It is polling the
endpoint in its log. If it times out, the URL is wrong or unreachable, or
ClickHouse never became ready — check `schema.clickhouse.url` and the server's
own logs.

**The UI loads but the graph is empty.** Check, in order: the schema Job
completed, the agent logged `Successfully sent events to ClickHouse`, and rows
exist. With the bundled server:

```bash
kubectl -n kseer exec kseer-clickhouse-0 -- \
  clickhouse-client --user kseer --password <password> \
  -q "SELECT count(), uniqExact(kind) FROM kseer.kubernetes_events"
```

If rows exist but the UI shows nothing, confirm the browser is authenticating:
queries go through the UI's `/clickhouse` path and are authenticated by
ClickHouse, so bad credentials surface as HTTP 403 in the browser console.

**`found in Chart.yaml, but missing in charts/ directory`.** Run
`helm dependency update deploy/helm/kseer`.

**A render fails with a `kseer:` message.** That is a deliberate guard. It
names the values that disagree and what to set — see
[How the components fit together](#how-the-components-fit-together).
