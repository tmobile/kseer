# Test harnesses

Two independent setups live here. The end-to-end test is the maintained one and is what
CI runs; the multi-cluster harness is older and has an external dependency described
below.

- [End-to-end test (kind + ClickHouse)](#end-to-end-test-kind--clickhouse)
- [Multi-cluster harness](#multi-cluster-harness)
- [Stress testing](#stress-testing)
- [Requirements](#requirements)

## End-to-end test (kind + ClickHouse)

`local-kind-e2e.sh` verifies the whole write path: it starts a ClickHouse container,
creates a kind cluster, builds and loads the agent image, installs the Helm chart in
in-cluster mode, and confirms events actually land in the table. It is self-contained —
nothing outside this repository is needed.

```bash
CLICKHOUSE_USERNAME=admin CLICKHOUSE_PASSWORD=changeme ./local-kind-e2e.sh

# tear everything down
./local-kind-e2e.sh --cleanup
```

The same username and password pair bootstraps the ClickHouse container *and* is baked
into the Kubernetes Secret the agent uses, so the agent holds exactly the credentials the
server was started with.

Defaults, all overridable by environment variable:

| Variable | Default | Meaning |
|----------|---------|---------|
| `KIND_CLUSTER_NAME` | `kseer-local` | kind cluster to create |
| `CH_CONTAINER_NAME` | `kseer-local-clickhouse` | ClickHouse container name |
| `CH_IMAGE` | `clickhouse/clickhouse-server:25.9` | ClickHouse image |
| `CH_DATABASE` | `kseer` | Database created for the test |
| `CH_HTTP_PORT` | `8124` | Host port for the HTTP interface |
| `CH_NATIVE_PORT` | `9001` | Host port for the native protocol |
| `AGENT_IMAGE` / `AGENT_TAG` | `kseer/kseer-agent` / `latest` | Image under test |
| `HELM_RELEASE` | `kseer-agent` | Helm release name |
| `KSEER_NAMESPACE` | `kseer-agent` | Namespace the chart installs into |

The host ports deliberately avoid the usual 8123 and 9000 so the test can run alongside
a local ClickHouse you already have up.

The script builds the agent image via `make docker-build`. On a corporate network that
intercepts TLS, that build fails at `apk add` unless you export `CORP_CA_URL` pointing at
your root CA — see [Building behind a TLS-inspecting proxy](../README.md#building-behind-a-tls-inspecting-proxy).

CI runs this same script as its `smoke:kind` job, passing `AGENT_IMAGE` and `AGENT_TAG`
for the freshly published image. That job needs a Docker-in-Docker-capable runner, which
is why it is manual — see [Releasing](../README.md#releasing).

## Multi-cluster harness

`test-env.sh` builds a richer environment: two kind clusters (`kseer-test-1` with a
control plane plus worker, `kseer-test-2` control plane only, on separate pod and service
subnets), test workloads in each, an agent watching both, and a generator that keeps
making changes.

> **This harness does not run as-is from this repository.** `test-env.sh` expects a
> sibling `kseer-backend` checkout providing Kafka, Druid, ZooKeeper, and PostgreSQL, and
> it points the agent's output at that Kafka. That component is not part of this repo,
> and the storage layer here is ClickHouse rather than Druid. Use `local-kind-e2e.sh`
> unless you have that stack, or repoint the generated `agent-test-config.yaml` at an
> output you actually run.

```bash
./test-env.sh start
./test-env.sh status
./stop-test-env.sh
```

What it generates, all inside `test/`:

```
kubeconfigs/kseer-test-1.kubeconfig
kubeconfigs/kseer-test-2.kubeconfig
agent-test-config.yaml       # generated agent config
agent.log / agent.pid
workload-generator.log / workload-generator.pid
```

Reach the clusters directly with those kubeconfigs:

```bash
kubectl --kubeconfig=kubeconfigs/kseer-test-1.kubeconfig get pods
kubectl --kubeconfig=kubeconfigs/kseer-test-2.kubeconfig get pods
```

Creating a workload by hand is often the quickest way to confirm events flow:

```bash
kubectl --kubeconfig=kubeconfigs/kseer-test-1.kubeconfig create deployment test-manual --image=nginx
kubectl --kubeconfig=kubeconfigs/kseer-test-1.kubeconfig scale deployment test-manual --replicas=3
kubectl --kubeconfig=kubeconfigs/kseer-test-1.kubeconfig delete deployment test-manual
```

### When something goes wrong

Read `agent.log` first — the agent logs why it rejected a cluster or failed to connect.
Confirm the kubeconfigs exist under `kubeconfigs/`. If the kind clusters themselves are
unhealthy, recreating them is usually faster than debugging:

```bash
kind delete clusters kseer-test-1 kseer-test-2
./test-env.sh start
```

## Stress testing

The stress scripts push enough volume and churn through the harness to exercise UI
performance, filters, and live updates. They drive the kind clusters created by
`test-env.sh` and read its kubeconfigs, so **start that first**.

```bash
./test-env.sh start     # prerequisite
./run-stress-test.sh    # wrapper: setup, then continuous changes
```

`stress-test.sh` takes a subcommand if you want finer control:

| Command | Effect |
|---------|--------|
| `./stress-test.sh run` | Full test: initial state, then continuous changes (default) |
| `./stress-test.sh setup` | Create the initial objects and stop |
| `./stress-test.sh stats` | Print current object and change counts |
| `./stress-test.sh cleanup` | Remove everything the stress test created |

It creates 5 stress-test namespaces per cluster and a spread of kinds — Deployments,
StatefulSets, DaemonSets, Services, Jobs, CronJobs, ConfigMaps, and Secrets — with
randomized environment, team, and app labels, then performs roughly three changes every
three seconds: scaling, creating, updating, and deleting.

Interrupting with Ctrl-C stops the churn but leaves the objects behind, so run
`./stress-test.sh cleanup` afterwards or the next run starts from a dirty state.

`quick-stress.sh`, `simple-stress.sh`, and `local-kind-stress.sh` are smaller variants of
the same idea, useful when the full stress test is more than you need.

## Requirements

- Docker
- kind (`go install sigs.k8s.io/kind@latest`)
- kubectl
- Helm (for the end-to-end test)
- Go 1.21+
- curl and jq
