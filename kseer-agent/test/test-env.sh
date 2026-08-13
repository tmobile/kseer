#!/bin/bash

# Kseer Agent Test Environment Script
# Creates multiple Kind clusters and runs the agent to watch them

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
AGENT_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_ROOT="$(dirname "$AGENT_DIR")"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_step() {
    echo -e "${BLUE}[STEP]${NC} $1"
}

# Check requirements
check_requirements() {
    log_info "Checking requirements..."
    
    for tool in docker kind kubectl go; do
        if ! command -v $tool &> /dev/null; then
            log_error "$tool is required but not installed."
            case $tool in
                kind)
                    echo "Install with: go install sigs.k8s.io/kind@latest"
                    ;;
                kubectl)
                    echo "Install with: curl -LO https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/darwin/amd64/kubectl"
                    ;;
            esac
            exit 1
        fi
    done
    
    log_info "All requirements satisfied"
}

# Start kseer-backend if not running
start_backend() {
    log_step "Checking kseer-backend status..."
    
    if ! docker ps | grep -q kseer-kafka; then
        log_info "Starting kseer-backend..."
        cd "$PROJECT_ROOT/kseer-backend"
        ./scripts/setup.sh
        log_info "Backend started successfully"
    else
        log_info "Backend is already running"
    fi
    
    # Wait for backend to be ready
    log_info "Waiting for backend to be ready..."
    until curl -s http://localhost:8081/status > /dev/null 2>&1; do
        echo -n "."
        sleep 2
    done
    echo ""
    log_info "Backend is ready"
}

# Create Kind clusters
create_clusters() {
    log_step "Creating Kind clusters..."
    
    # Create cluster 1
    if ! kind get clusters | grep -q "kseer-test-1"; then
        log_info "Creating kseer-test-1 cluster..."
        kind create cluster --config "$SCRIPT_DIR/kind-cluster-1.yaml"
    else
        log_info "Cluster kseer-test-1 already exists"
    fi
    
    # Create cluster 2
    if ! kind get clusters | grep -q "kseer-test-2"; then
        log_info "Creating kseer-test-2 cluster..."
        kind create cluster --config "$SCRIPT_DIR/kind-cluster-2.yaml"
    else
        log_info "Cluster kseer-test-2 already exists"
    fi
    
    # Export kubeconfigs
    log_info "Exporting kubeconfigs..."
    mkdir -p "$SCRIPT_DIR/kubeconfigs"
    kind export kubeconfig --name kseer-test-1 --kubeconfig "$SCRIPT_DIR/kubeconfigs/kseer-test-1.kubeconfig"
    kind export kubeconfig --name kseer-test-2 --kubeconfig "$SCRIPT_DIR/kubeconfigs/kseer-test-2.kubeconfig"
}

# Generate test workloads
generate_workloads() {
    log_step "Generating test workloads..."
    
    # Apply to cluster 1
    log_info "Deploying workloads to kseer-test-1..."
    kubectl --kubeconfig="$SCRIPT_DIR/kubeconfigs/kseer-test-1.kubeconfig" apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-deployment
  namespace: default
  labels:
    app: nginx
    cluster: test-1
spec:
  replicas: 2
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
        cluster: test-1
    spec:
      containers:
      - name: nginx
        image: nginx:1.20
        ports:
        - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: nginx-service
  namespace: default
  labels:
    app: nginx
    cluster: test-1
spec:
  selector:
    app: nginx
  ports:
  - port: 80
    targetPort: 80
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: default
  labels:
    app: nginx
    cluster: test-1
data:
  config.yaml: |
    server:
      port: 80
      name: nginx-test-1
EOF

    # Apply to cluster 2
    log_info "Deploying workloads to kseer-test-2..."
    kubectl --kubeconfig="$SCRIPT_DIR/kubeconfigs/kseer-test-2.kubeconfig" apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis-deployment
  namespace: default
  labels:
    app: redis
    cluster: test-2
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
        cluster: test-2
    spec:
      containers:
      - name: redis
        image: redis:6-alpine
        ports:
        - containerPort: 6379
---
apiVersion: v1
kind: Service
metadata:
  name: redis-service
  namespace: default
  labels:
    app: redis
    cluster: test-2
spec:
  selector:
    app: redis
  ports:
  - port: 6379
    targetPort: 6379
---
apiVersion: batch/v1
kind: Job
metadata:
  name: test-job
  namespace: default
  labels:
    app: test
    cluster: test-2
spec:
  template:
    spec:
      containers:
      - name: test
        image: busybox
        command: ["sh", "-c", "echo 'Hello from test-2' && sleep 30"]
      restartPolicy: Never
EOF

    log_info "Workloads deployed successfully"
}

# Generate agent configuration
generate_agent_config() {
    log_step "Generating agent configuration..."
    
    cat > "$SCRIPT_DIR/agent-test-config.yaml" <<EOF
clusters:
  - name: "kseer-test-1"
    kubeconfig: "$SCRIPT_DIR/kubeconfigs/kseer-test-1.kubeconfig"
    # Watch all namespaces and resources
    namespaces: []
    resources: []
    
  - name: "kseer-test-2"
    kubeconfig: "$SCRIPT_DIR/kubeconfigs/kseer-test-2.kubeconfig"
    # Watch all namespaces and resources
    namespaces: []
    resources: []

outputs:
  # Send to Kafka
  - type: kafka
    config:
      brokers:
        - "localhost:9092"
      topic: "kubernetes-events"
      compression: "snappy"
      
  # Also output to stdout for debugging
  - type: stdout
    config:
      pretty: true

buffer:
  size: 1000
  flush_interval: "3s"
  retry_interval: "10s"
EOF

    log_info "Agent configuration generated at $SCRIPT_DIR/agent-test-config.yaml"
}

# Build and run agent
run_agent() {
    log_step "Building and running kseer-agent..."
    
    cd "$AGENT_DIR"
    
    # Build agent
    log_info "Building agent..."
    go build -o kseer-agent cmd/agent/main.go
    
    # Run agent in background
    log_info "Starting agent (PID will be saved to $SCRIPT_DIR/agent.pid)..."
    ./kseer-agent --config "$SCRIPT_DIR/agent-test-config.yaml" > "$SCRIPT_DIR/agent.log" 2>&1 &
    echo $! > "$SCRIPT_DIR/agent.pid"
    
    log_info "Agent started with PID $(cat "$SCRIPT_DIR/agent.pid")"
    log_info "Agent logs: $SCRIPT_DIR/agent.log"
}

# Generate continuous workload changes
start_workload_generator() {
    log_step "Starting workload generator..."
    
    cat > "$SCRIPT_DIR/workload-generator.sh" <<'EOF'
#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
COUNTER=0

log_info() {
    echo "[$(date '+%H:%M:%S')] $1"
}

while true; do
    COUNTER=$((COUNTER + 1))
    
    # Update deployment replicas
    if [ $((COUNTER % 3)) -eq 0 ]; then
        REPLICAS=$((2 + COUNTER % 3))
        log_info "Scaling nginx deployment to $REPLICAS replicas"
        kubectl --kubeconfig="$SCRIPT_DIR/kubeconfigs/kseer-test-1.kubeconfig" \
            scale deployment nginx-deployment --replicas=$REPLICAS
    fi
    
    # Create/delete configmaps
    if [ $((COUNTER % 5)) -eq 0 ]; then
        log_info "Creating test configmap $COUNTER"
        kubectl --kubeconfig="$SCRIPT_DIR/kubeconfigs/kseer-test-2.kubeconfig" \
            create configmap "test-config-$COUNTER" --from-literal="counter=$COUNTER" || true
    fi
    
    # Delete old configmaps
    if [ $COUNTER -gt 5 ] && [ $((COUNTER % 7)) -eq 0 ]; then
        OLD_COUNTER=$((COUNTER - 5))
        log_info "Deleting old configmap $OLD_COUNTER"
        kubectl --kubeconfig="$SCRIPT_DIR/kubeconfigs/kseer-test-2.kubeconfig" \
            delete configmap "test-config-$OLD_COUNTER" || true
    fi
    
    # Create test pods
    if [ $((COUNTER % 4)) -eq 0 ]; then
        log_info "Creating test pod $COUNTER"
        kubectl --kubeconfig="$SCRIPT_DIR/kubeconfigs/kseer-test-1.kubeconfig" \
            run "test-pod-$COUNTER" --image=busybox --restart=Never \
            --command -- sh -c "echo 'Test pod $COUNTER' && sleep 60" || true
    fi
    
    sleep 10
done
EOF

    chmod +x "$SCRIPT_DIR/workload-generator.sh"
    
    log_info "Starting workload generator in background..."
    "$SCRIPT_DIR/workload-generator.sh" > "$SCRIPT_DIR/workload-generator.log" 2>&1 &
    echo $! > "$SCRIPT_DIR/workload-generator.pid"
    
    log_info "Workload generator started with PID $(cat "$SCRIPT_DIR/workload-generator.pid")"
    log_info "Generator logs: $SCRIPT_DIR/workload-generator.log"
}

# Show status and next steps
show_status() {
    log_step "Test environment is ready!"
    echo ""
    echo "📊 Monitoring URLs:"
    echo "   - Kafka UI: http://localhost:8080"
    echo "   - Druid Console: http://localhost:8888"
    echo "   - Druid Coordinator: http://localhost:8081"
    echo ""
    echo "📁 Files created:"
    echo "   - Agent config: $SCRIPT_DIR/agent-test-config.yaml"
    echo "   - Agent logs: $SCRIPT_DIR/agent.log"
    echo "   - Workload logs: $SCRIPT_DIR/workload-generator.log"
    echo ""
    echo "🔍 Check data ingestion:"
    echo "   curl -X POST http://localhost:8888/druid/v2/sql \\"
    echo "     -H 'Content-Type: application/json' \\"
    echo "     -d '{\"query\": \"SELECT cluster_name, COUNT(*) FROM kubernetes_objects GROUP BY cluster_name\"}' | jq"
    echo ""
    echo "📋 View recent events:"
    echo "   curl -X POST http://localhost:8888/druid/v2/sql \\"
    echo "     -H 'Content-Type: application/json' \\"
    echo "     -d '{\"query\": \"SELECT * FROM kubernetes_objects ORDER BY __time DESC LIMIT 10\"}' | jq"
    echo ""
    echo "🛑 To stop everything:"
    echo "   $SCRIPT_DIR/../test/stop-test-env.sh"
}

# Cleanup function
cleanup() {
    log_info "Cleaning up test environment..."
    
    # Stop processes
    if [ -f "$SCRIPT_DIR/agent.pid" ]; then
        log_info "Stopping agent..."
        kill $(cat "$SCRIPT_DIR/agent.pid") 2>/dev/null || true
        rm -f "$SCRIPT_DIR/agent.pid"
    fi
    
    if [ -f "$SCRIPT_DIR/workload-generator.pid" ]; then
        log_info "Stopping workload generator..."
        kill $(cat "$SCRIPT_DIR/workload-generator.pid") 2>/dev/null || true
        rm -f "$SCRIPT_DIR/workload-generator.pid"
    fi
    
    # Delete Kind clusters
    log_info "Deleting Kind clusters..."
    kind delete cluster --name kseer-test-1 2>/dev/null || true
    kind delete cluster --name kseer-test-2 2>/dev/null || true
    
    # Clean up files
    rm -rf "$SCRIPT_DIR/kubeconfigs"
    rm -f "$SCRIPT_DIR/agent-test-config.yaml"
    rm -f "$SCRIPT_DIR/workload-generator.sh"
    
    log_info "Cleanup complete"
}

# Main function
main() {
    case "${1:-start}" in
        start)
            log_info "Starting Kseer Agent test environment..."
            check_requirements
            start_backend
            create_clusters
            generate_workloads
            generate_agent_config
            run_agent
            sleep 5  # Give agent time to start
            start_workload_generator
            show_status
            ;;
        stop)
            cleanup
            ;;
        status)
            if [ -f "$SCRIPT_DIR/agent.pid" ] && kill -0 $(cat "$SCRIPT_DIR/agent.pid") 2>/dev/null; then
                echo "✅ Agent is running (PID: $(cat "$SCRIPT_DIR/agent.pid"))"
            else
                echo "❌ Agent is not running"
            fi
            
            if [ -f "$SCRIPT_DIR/workload-generator.pid" ] && kill -0 $(cat "$SCRIPT_DIR/workload-generator.pid") 2>/dev/null; then
                echo "✅ Workload generator is running (PID: $(cat "$SCRIPT_DIR/workload-generator.pid"))"
            else
                echo "❌ Workload generator is not running"
            fi
            
            echo ""
            echo "Kind clusters:"
            kind get clusters | grep kseer-test || echo "No kseer test clusters found"
            ;;
        *)
            echo "Usage: $0 {start|stop|status}"
            echo "  start  - Start the complete test environment"
            echo "  stop   - Stop and cleanup everything"
            echo "  status - Show current status"
            exit 1
            ;;
    esac
}

# Handle Ctrl+C
trap cleanup INT TERM

main "$@"
