#!/usr/bin/env bash
#
# local-kind-stress.sh — spam a variety of Kubernetes objects into the local
# kind cluster created by local-kind-e2e.sh, then verify the events made it
# into ClickHouse.
#
# Prerequisites:
#   - local-kind-e2e.sh has been run (kind cluster + CH container + agent installed)
#   - CLICKHOUSE_USERNAME and CLICKHOUSE_PASSWORD env vars set (same as e2e)
#
# Usage:
#   CLICKHOUSE_USERNAME=admin CLICKHOUSE_PASSWORD=changeme ./local-kind-stress.sh           # default count
#   CLICKHOUSE_USERNAME=admin CLICKHOUSE_PASSWORD=changeme ./local-kind-stress.sh 50         # 50 of each kind
#   ./local-kind-stress.sh --cleanup                                                         # remove the test namespace

set -euo pipefail

# --- Tunables ---------------------------------------------------------------
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kseer-local}"
CH_CONTAINER_NAME="${CH_CONTAINER_NAME:-kseer-local-clickhouse}"
CH_DATABASE="${CH_DATABASE:-kseer}"
KSEER_AGENT_NAMESPACE="${KSEER_AGENT_NAMESPACE:-kseer-agent}"
TEST_NAMESPACE="${TEST_NAMESPACE:-kseer-load-test}"
KUBE_CONTEXT="kind-${KIND_CLUSTER_NAME}"
SETTLE_SECONDS="${SETTLE_SECONDS:-15}"

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
if [[ "${1:-}" == "--cleanup" ]]; then
    log_step "Removing test namespace $TEST_NAMESPACE..."
    if kubectl --context "$KUBE_CONTEXT" get ns "$TEST_NAMESPACE" >/dev/null 2>&1; then
        kubectl --context "$KUBE_CONTEXT" delete ns "$TEST_NAMESPACE" --wait=false
        log_info "Namespace deletion initiated (kubectl returns immediately; cluster GC handles the rest)"
    else
        log_info "Test namespace not found — nothing to clean up"
    fi
    exit 0
fi

# --- Args -------------------------------------------------------------------
COUNT="${1:-20}"
if ! [[ "$COUNT" =~ ^[0-9]+$ ]]; then
    log_error "Count must be a positive integer (got: $COUNT)"
    exit 1
fi

# --- Preflight --------------------------------------------------------------
log_step "Preflight checks..."

for tool in kind kubectl docker; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        log_error "$tool is required but not installed."
        exit 1
    fi
done

: "${CLICKHOUSE_USERNAME:?CLICKHOUSE_USERNAME must be set in the environment}"
: "${CLICKHOUSE_PASSWORD:?CLICKHOUSE_PASSWORD must be set in the environment}"

# Verify kind cluster exists
if ! kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER_NAME"; then
    log_error "Kind cluster '$KIND_CLUSTER_NAME' not found."
    log_error "Run the setup script first:"
    echo "  CLICKHOUSE_USERNAME=$CLICKHOUSE_USERNAME CLICKHOUSE_PASSWORD=*** ./kseer-agent/test/local-kind-e2e.sh"
    exit 1
fi

# Verify ClickHouse container is running
if ! docker ps --format '{{.Names}}' | grep -qx "$CH_CONTAINER_NAME"; then
    log_error "ClickHouse container '$CH_CONTAINER_NAME' is not running."
    log_error "Run the setup script first."
    exit 1
fi

# Verify agent pod is running
if ! kubectl --context "$KUBE_CONTEXT" -n "$KSEER_AGENT_NAMESPACE" get pod \
        -l app.kubernetes.io/name=kseer-agent --no-headers 2>/dev/null | grep -q Running; then
    log_error "kseer-agent pod is not Running in namespace $KSEER_AGENT_NAMESPACE."
    log_error "Inspect with: kubectl -n $KSEER_AGENT_NAMESPACE get pod"
    exit 1
fi

log_info "All checks passed (cluster=$KIND_CLUSTER_NAME, ch=$CH_CONTAINER_NAME, agent ready)"

# --- Helpers ----------------------------------------------------------------
ch_query() {
    docker exec -i "$CH_CONTAINER_NAME" clickhouse-client \
        --user "$CLICKHOUSE_USERNAME" --password "$CLICKHOUSE_PASSWORD" \
        -q "$1"
}

count_events_in_test_ns() {
    ch_query "SELECT count() FROM ${CH_DATABASE}.kubernetes_events_local WHERE namespace = '${TEST_NAMESPACE}'"
}

# --- Capture baseline -------------------------------------------------------
log_step "Capturing baseline event count for namespace $TEST_NAMESPACE..."
BASELINE=$(count_events_in_test_ns | tr -d '[:space:]')
log_info "Baseline events for $TEST_NAMESPACE: $BASELINE"

# --- Create test namespace --------------------------------------------------
log_step "Ensuring test namespace $TEST_NAMESPACE exists..."
kubectl --context "$KUBE_CONTEXT" create namespace "$TEST_NAMESPACE" --dry-run=client -o yaml \
    | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null

# --- Spam objects -----------------------------------------------------------
log_step "Spamming $COUNT of each kind into $TEST_NAMESPACE..."

# ConfigMaps
log_info "Creating $COUNT ConfigMaps..."
for i in $(seq 1 "$COUNT"); do
    kubectl --context "$KUBE_CONTEXT" -n "$TEST_NAMESPACE" create configmap "stress-cm-$i" \
        --from-literal="key=value-$i" --dry-run=client -o yaml \
        | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null &
done
wait

# Secrets
log_info "Creating $COUNT Secrets..."
for i in $(seq 1 "$COUNT"); do
    kubectl --context "$KUBE_CONTEXT" -n "$TEST_NAMESPACE" create secret generic "stress-secret-$i" \
        --from-literal="password=p-$i" --dry-run=client -o yaml \
        | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null &
done
wait

# ServiceAccounts
log_info "Creating $COUNT ServiceAccounts..."
for i in $(seq 1 "$COUNT"); do
    cat <<EOF | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null &
apiVersion: v1
kind: ServiceAccount
metadata:
  name: stress-sa-$i
  namespace: $TEST_NAMESPACE
EOF
done
wait

# Roles
log_info "Creating $COUNT Roles..."
for i in $(seq 1 "$COUNT"); do
    cat <<EOF | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null &
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: stress-role-$i
  namespace: $TEST_NAMESPACE
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list"]
EOF
done
wait

# Services
log_info "Creating $COUNT Services..."
for i in $(seq 1 "$COUNT"); do
    cat <<EOF | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null &
apiVersion: v1
kind: Service
metadata:
  name: stress-svc-$i
  namespace: $TEST_NAMESPACE
spec:
  selector:
    app: stress-$i
  ports:
  - port: 80
    targetPort: 80
EOF
done
wait

# Pods (using pause image — minimal, doesn't actually run anything that pulls)
log_info "Creating $COUNT Pods..."
for i in $(seq 1 "$COUNT"); do
    cat <<EOF | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null &
apiVersion: v1
kind: Pod
metadata:
  name: stress-pod-$i
  namespace: $TEST_NAMESPACE
  labels:
    app: stress-$i
spec:
  containers:
  - name: pause
    image: registry.k8s.io/pause:3.9
EOF
done
wait

# Deployments (each creates a ReplicaSet, which creates a Pod — multiplier!)
DEPLOY_COUNT=$((COUNT / 2))
[[ $DEPLOY_COUNT -lt 1 ]] && DEPLOY_COUNT=1
log_info "Creating $DEPLOY_COUNT Deployments (each spawns a ReplicaSet + Pod)..."
for i in $(seq 1 "$DEPLOY_COUNT"); do
    cat <<EOF | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null &
apiVersion: apps/v1
kind: Deployment
metadata:
  name: stress-deploy-$i
  namespace: $TEST_NAMESPACE
spec:
  replicas: 1
  selector:
    matchLabels:
      app: stress-deploy-$i
  template:
    metadata:
      labels:
        app: stress-deploy-$i
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
EOF
done
wait

# Jobs
JOB_COUNT=$((COUNT / 4))
[[ $JOB_COUNT -lt 1 ]] && JOB_COUNT=1
log_info "Creating $JOB_COUNT Jobs..."
for i in $(seq 1 "$JOB_COUNT"); do
    cat <<EOF | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null &
apiVersion: batch/v1
kind: Job
metadata:
  name: stress-job-$i
  namespace: $TEST_NAMESPACE
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: hello
        image: registry.k8s.io/pause:3.9
EOF
done
wait

log_info "All objects submitted to the API server."

# --- Wait for the agent to flush -------------------------------------------
log_step "Waiting ${SETTLE_SECONDS}s for the agent to LIST/WATCH and flush to ClickHouse..."
sleep "$SETTLE_SECONDS"

# --- Verify -----------------------------------------------------------------
log_step "Querying ClickHouse for events in $TEST_NAMESPACE..."
AFTER=$(count_events_in_test_ns | tr -d '[:space:]')
DELTA=$((AFTER - BASELINE))

log_info "Baseline:        $BASELINE events"
log_info "After spam:      $AFTER events"
log_info "Delta:           $DELTA new events"
echo

log_step "Per-kind breakdown for $TEST_NAMESPACE:"
ch_query "SELECT kind, count() AS n
          FROM ${CH_DATABASE}.kubernetes_events_local
          WHERE namespace = '${TEST_NAMESPACE}'
          GROUP BY kind ORDER BY n DESC FORMAT PrettyCompact"

echo
log_step "Per-operation breakdown for $TEST_NAMESPACE:"
ch_query "SELECT operation, count() AS n
          FROM ${CH_DATABASE}.kubernetes_events_local
          WHERE namespace = '${TEST_NAMESPACE}'
          GROUP BY operation ORDER BY n DESC FORMAT PrettyCompact"

echo
if [[ $DELTA -gt 0 ]]; then
    log_info "SUCCESS — events are flowing from agent → ClickHouse"
    echo
    log_info "Inspect further:"
    echo "  kubectl --context $KUBE_CONTEXT -n $TEST_NAMESPACE get all"
    echo "  $0 --cleanup    # remove the test namespace"
    exit 0
else
    log_error "No new events landed in ClickHouse for namespace $TEST_NAMESPACE"
    echo
    log_warn "Possible causes:"
    echo "  - Agent isn't running (kubectl -n $KSEER_AGENT_NAMESPACE get pod)"
    echo "  - Agent isn't writing (kubectl -n $KSEER_AGENT_NAMESPACE logs -l app.kubernetes.io/name=kseer-agent --tail=50)"
    echo "  - MV not routing (docker exec -it $CH_CONTAINER_NAME clickhouse-client -u $CLICKHOUSE_USERNAME --password '***' -q 'SHOW TABLES FROM $CH_DATABASE')"
    exit 1
fi
