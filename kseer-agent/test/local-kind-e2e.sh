#!/usr/bin/env bash
#
# local-kind-e2e.sh — stand up a local ClickHouse container + kind cluster,
# build and load the kseer-agent image, and install the Helm chart in
# in-cluster mode to verify the write path end-to-end.
#
# Usage:
#   CLICKHOUSE_USERNAME=admin CLICKHOUSE_PASSWORD=changeme ./local-kind-e2e.sh
#   ./local-kind-e2e.sh --cleanup
#
# The same CLICKHOUSE_USERNAME / CLICKHOUSE_PASSWORD pair is used for:
#   1) bootstrapping the CH container (mapped to CLICKHOUSE_USER/CLICKHOUSE_PASSWORD
#      inside the image), AND
#   2) the agent's CH auth (baked into a K8s Secret via credentialsSecret.create=true)
# so the agent has exactly the credentials the CH server was started with.

set -euo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
AGENT_DIR="$(dirname "$SCRIPT_DIR")"
CHART_DIR="$AGENT_DIR/deploy/helm/kseer-agent"

# --- Tunables ---------------------------------------------------------------
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kseer-local}"
CH_CONTAINER_NAME="${CH_CONTAINER_NAME:-kseer-local-clickhouse}"
CH_DATABASE="${CH_DATABASE:-kseer}"
CH_IMAGE="${CH_IMAGE:-clickhouse/clickhouse-server:25.9}"
# Host port mappings. Defaults are off-standard so they don't clash with the
# backend docker-compose stack (which uses 8123/9000). Pods inside kind reach
# CH via the docker network (always port 9000 inside the container) regardless
# of these values — these only control how YOU reach CH from the host.
CH_HTTP_PORT="${CH_HTTP_PORT:-8124}"
CH_NATIVE_PORT="${CH_NATIVE_PORT:-9001}"
AGENT_IMAGE="${AGENT_IMAGE:-kseer/kseer-agent}"
AGENT_TAG="${AGENT_TAG:-latest}"
HELM_RELEASE="${HELM_RELEASE:-kseer-agent}"
KSEER_NAMESPACE="${KSEER_NAMESPACE:-kseer-agent}"
VALUES_FILE="${VALUES_FILE:-/tmp/kseer-local-values.yaml}"

# --- Logging ----------------------------------------------------------------
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1" >&2; }
log_step()  { echo -e "${BLUE}[STEP]${NC} $1"; }

# --- Cleanup ----------------------------------------------------------------
cleanup() {
    log_step "Cleaning up local test environment..."

    if kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER_NAME"; then
        log_info "Deleting kind cluster $KIND_CLUSTER_NAME"
        kind delete cluster --name "$KIND_CLUSTER_NAME"
    else
        log_info "Kind cluster $KIND_CLUSTER_NAME not found (skipping)"
    fi

    if docker ps -a --format '{{.Names}}' | grep -qx "$CH_CONTAINER_NAME"; then
        log_info "Removing ClickHouse container $CH_CONTAINER_NAME"
        docker rm -f "$CH_CONTAINER_NAME" >/dev/null
    else
        log_info "ClickHouse container $CH_CONTAINER_NAME not found (skipping)"
    fi

    rm -f "$VALUES_FILE"
    log_info "Cleanup complete"
}

if [[ "${1:-}" == "--cleanup" ]]; then
    cleanup
    exit 0
fi

# --- Phase 1: Preflight -----------------------------------------------------
log_step "Preflight checks..."

for tool in docker kind kubectl helm make; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        log_error "$tool is required but not installed."
        case "$tool" in
            kind) echo "  Install: go install sigs.k8s.io/kind@latest" ;;
            kubectl) echo "  Install: brew install kubectl (or see k8s.io docs)" ;;
            helm) echo "  Install: brew install helm (or see helm.sh docs)" ;;
        esac
        exit 1
    fi
done

: "${CLICKHOUSE_USERNAME:?CLICKHOUSE_USERNAME must be set in the environment}"
: "${CLICKHOUSE_PASSWORD:?CLICKHOUSE_PASSWORD must be set in the environment}"

log_info "All tools present, credentials provided"

# --- Phase 2: ClickHouse container -----------------------------------------
log_step "Starting ClickHouse container..."

# kind auto-creates a docker network called "kind"; attach CH to the same
# network so pods inside the cluster can resolve it by container name.
if ! docker network inspect kind >/dev/null 2>&1; then
    log_info "Creating docker network 'kind'"
    docker network create kind >/dev/null
fi

if docker ps -a --format '{{.Names}}' | grep -qx "$CH_CONTAINER_NAME"; then
    log_warn "Existing $CH_CONTAINER_NAME container found; removing for a clean start"
    docker rm -f "$CH_CONTAINER_NAME" >/dev/null
fi

docker run -d \
    --name "$CH_CONTAINER_NAME" \
    --network kind \
    -p "${CH_HTTP_PORT}:8123" \
    -p "${CH_NATIVE_PORT}:9000" \
    -e CLICKHOUSE_USER="$CLICKHOUSE_USERNAME" \
    -e CLICKHOUSE_PASSWORD="$CLICKHOUSE_PASSWORD" \
    -e CLICKHOUSE_DB="$CH_DATABASE" \
    --ulimit nofile=262144:262144 \
    "$CH_IMAGE" >/dev/null

log_info "Waiting for ClickHouse to accept connections (host port ${CH_HTTP_PORT})..."
for i in {1..60}; do
    if curl -sf "http://localhost:${CH_HTTP_PORT}/ping" >/dev/null 2>&1; then
        log_info "ClickHouse is up"
        break
    fi
    if [[ $i -eq 60 ]]; then
        log_error "ClickHouse did not become ready within 60s"
        docker logs "$CH_CONTAINER_NAME" | tail -30
        exit 1
    fi
    sleep 1
done

# --- Phase 3: Initialize CH schema -----------------------------------------
log_step "Initializing ClickHouse schema..."

docker exec -i "$CH_CONTAINER_NAME" clickhouse-client \
    --user "$CLICKHOUSE_USERNAME" --password "$CLICKHOUSE_PASSWORD" \
    --multiquery <<SQL
CREATE DATABASE IF NOT EXISTS $CH_DATABASE;

CREATE TABLE IF NOT EXISTS $CH_DATABASE.kubernetes_events_local
(
    timestamp   DateTime64(6) CODEC(Delta, LZ4),
    cluster_name LowCardinality(String) CODEC(ZSTD(1)),
    operation    LowCardinality(String) CODEC(ZSTD(1)),
    kind         LowCardinality(String) CODEC(ZSTD(1)),
    namespace    LowCardinality(String) CODEC(ZSTD(1)),
    uid          String CODEC(ZSTD(1)),
    object       JSON CODEC(ZSTD(3))
) ENGINE = MergeTree()
PARTITION BY toStartOfHour(timestamp)
ORDER BY (cluster_name, kind, namespace, timestamp)
TTL toDateTime(timestamp) + INTERVAL 90 DAY;
SQL

log_info "Schema initialized (agent writes directly to kubernetes_events_local)"

# --- Phase 4: kind cluster --------------------------------------------------
log_step "Creating kind cluster $KIND_CLUSTER_NAME..."

if kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER_NAME"; then
    log_warn "Kind cluster $KIND_CLUSTER_NAME already exists; reusing it"
else
    kind create cluster --name "$KIND_CLUSTER_NAME" --wait 60s
fi

kubectl config use-context "kind-$KIND_CLUSTER_NAME" >/dev/null
log_info "Using kubeconfig context kind-$KIND_CLUSTER_NAME"

# --- Phase 5: Build + load agent image -------------------------------------
log_step "Building agent docker image via make docker-build..."
(
    cd "$AGENT_DIR"
    make docker-build
)

log_step "Loading image into kind cluster..."
kind load docker-image "${AGENT_IMAGE}:${AGENT_TAG}" --name "$KIND_CLUSTER_NAME"
log_info "Loaded ${AGENT_IMAGE}:${AGENT_TAG} into $KIND_CLUSTER_NAME"

# --- Phase 6: Helm install --------------------------------------------------
log_step "Generating values file at $VALUES_FILE..."

cat > "$VALUES_FILE" <<YAML
mode: in-cluster

# namespace.create=false because we use `helm install --create-namespace`
# below to pre-create the namespace. If the chart also tried to create it,
# Helm would fail with "already exists" (the pre-created namespace has no
# Helm ownership annotations). Tradeoff: on `helm uninstall`, the namespace
# persists — remove it with `kubectl delete ns $KSEER_NAMESPACE`.
namespace:
  create: false
  name: $KSEER_NAMESPACE

cluster:
  name: $KIND_CLUSTER_NAME
  namespaces: []
  resources: []

image:
  repository: $AGENT_IMAGE
  tag: "$AGENT_TAG"
  pullPolicy: Never

outputs:
  clickhouse:
    enabled: true
    host: "$CH_CONTAINER_NAME"
    port: 9000
    database: "$CH_DATABASE"
    table: "kubernetes_events_local"
    eventsTable: "kubernetes_events_local"
    tls: false
    tlsSkipVerify: false
    batchSize: 100
    tableVersion: "object_json"
    username: "$CLICKHOUSE_USERNAME"
    password: "$CLICKHOUSE_PASSWORD"
    credentialsSecret:
      create: true
      name: ""
  kafka:
    enabled: false
  stdout:
    enabled: false

externalSecret:
  enabled: false

flowSchema:
  enabled: false

watchConfig:
  resyncInterval: "0"

logging:
  level: info
YAML

log_step "Installing Helm chart..."
helm upgrade --install "$HELM_RELEASE" "$CHART_DIR" \
    -n "$KSEER_NAMESPACE" --create-namespace \
    -f "$VALUES_FILE"

log_info "Chart installed. Waiting for agent pod to be Ready..."
kubectl -n "$KSEER_NAMESPACE" wait \
    --for=condition=Ready pod \
    -l app.kubernetes.io/name=kseer-agent \
    --timeout=120s

# --- Phase 7: Smoke verification -------------------------------------------
log_step "Waiting 30s for agent to LIST + start WATCH + flush events..."
sleep 30

log_step "Querying ClickHouse for event counts..."
EVENT_COUNT=$(docker exec -i "$CH_CONTAINER_NAME" clickhouse-client \
    --user "$CLICKHOUSE_USERNAME" --password "$CLICKHOUSE_PASSWORD" \
    -q "SELECT count() FROM $CH_DATABASE.kubernetes_events_local")

log_info "Total events in kubernetes_events_local: $EVENT_COUNT"

if [[ "$EVENT_COUNT" -gt 0 ]]; then
    log_info "SUCCESS — events are flowing from agent → ClickHouse"
    echo
    log_step "Event counts by kind:"
    docker exec -i "$CH_CONTAINER_NAME" clickhouse-client \
        --user "$CLICKHOUSE_USERNAME" --password "$CLICKHOUSE_PASSWORD" \
        -q "SELECT kind, count() AS n FROM $CH_DATABASE.kubernetes_events_local GROUP BY kind ORDER BY n DESC"
else
    log_error "No events written yet. Inspect agent logs:"
    echo "  kubectl -n $KSEER_NAMESPACE logs -l app.kubernetes.io/name=kseer-agent --tail=100"
    exit 1
fi

echo
log_info "Useful follow-up commands:"
echo "  # Tail agent logs"
echo "  kubectl -n $KSEER_NAMESPACE logs -f -l app.kubernetes.io/name=kseer-agent"
echo
echo "  # Pod status"
echo "  kubectl -n $KSEER_NAMESPACE get pod"
echo
echo "  # Query CH via docker exec"
echo "  docker exec -i $CH_CONTAINER_NAME clickhouse-client -u $CLICKHOUSE_USERNAME --password '***' \\"
echo "    -q \"SELECT kind, namespace, count() FROM $CH_DATABASE.kubernetes_events_local GROUP BY kind, namespace\""
echo
echo "  # Query CH from host (HTTP):    http://localhost:${CH_HTTP_PORT}/play"
echo "  # Connect a GUI (DBeaver etc.): localhost:${CH_NATIVE_PORT}  (native protocol)"
echo
echo "  # Tear everything down"
echo "  $0 --cleanup"
