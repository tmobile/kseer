#!/bin/bash

# Stop Kseer Agent Test Environment

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_info "Stopping Kseer Agent test environment..."

# Stop processes
if [ -f "$SCRIPT_DIR/agent.pid" ]; then
    PID=$(cat "$SCRIPT_DIR/agent.pid")
    if kill -0 "$PID" 2>/dev/null; then
        log_info "Stopping agent (PID: $PID)..."
        kill "$PID"
        sleep 2
        if kill -0 "$PID" 2>/dev/null; then
            log_info "Force killing agent..."
            kill -9 "$PID"
        fi
    fi
    rm -f "$SCRIPT_DIR/agent.pid"
else
    log_info "No agent PID file found"
fi

if [ -f "$SCRIPT_DIR/workload-generator.pid" ]; then
    PID=$(cat "$SCRIPT_DIR/workload-generator.pid")
    if kill -0 "$PID" 2>/dev/null; then
        log_info "Stopping workload generator (PID: $PID)..."
        kill "$PID"
        sleep 1
        if kill -0 "$PID" 2>/dev/null; then
            kill -9 "$PID"
        fi
    fi
    rm -f "$SCRIPT_DIR/workload-generator.pid"
else
    log_info "No workload generator PID file found"
fi

# Delete Kind clusters
log_info "Deleting Kind clusters..."
for cluster in kseer-test-1 kseer-test-2; do
    if kind get clusters 2>/dev/null | grep -q "$cluster"; then
        log_info "Deleting cluster $cluster..."
        kind delete cluster --name "$cluster"
    fi
done

# Clean up files
log_info "Cleaning up files..."
rm -rf "$SCRIPT_DIR/kubeconfigs"
rm -f "$SCRIPT_DIR/agent-test-config.yaml"
rm -f "$SCRIPT_DIR/workload-generator.sh"
rm -f "$SCRIPT_DIR/agent.log"
rm -f "$SCRIPT_DIR/workload-generator.log"

# Optionally stop backend (commented out to avoid disrupting other tests)
# log_info "Stopping kseer-backend..."
# cd "$(dirname "$SCRIPT_DIR")/../../kseer-backend" && docker-compose down

log_info "Test environment stopped and cleaned up"
echo ""
echo "Note: kseer-backend is still running. To stop it:"
echo "  cd ../kseer-backend && docker-compose down"
