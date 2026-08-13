#!/bin/bash

# Helper script to run stress test with the test environment
# This ensures the test clusters are running and starts the stress test

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_step() {
    echo -e "${BLUE}[STEP]${NC} $1"
}

# Check if clusters are running
check_clusters() {
    log_step "Checking test clusters..."
    
    if ! kind get clusters 2>/dev/null | grep -q "kseer-test-1"; then
        log_error "Test cluster kseer-test-1 not found!"
        echo "Please run: cd $SCRIPT_DIR && ./test-env.sh start"
        exit 1
    fi
    
    if ! kind get clusters 2>/dev/null | grep -q "kseer-test-2"; then
        log_error "Test cluster kseer-test-2 not found!"
        echo "Please run: cd $SCRIPT_DIR && ./test-env.sh start"
        exit 1
    fi
    
    if [ ! -f "$SCRIPT_DIR/kubeconfigs/kseer-test-1.kubeconfig" ]; then
        log_error "Kubeconfig files not found!"
        echo "Please run: cd $SCRIPT_DIR && ./test-env.sh start"
        exit 1
    fi
    
    log_info "Test clusters are ready"
}

# Show current state
show_current_state() {
    log_info "Current cluster state:"
    echo ""
    
    for kubeconfig in "$SCRIPT_DIR/kubeconfigs/"*.kubeconfig; do
        local cluster_name=$(basename "$kubeconfig" .kubeconfig)
        echo "  📦 $cluster_name:"
        echo "    Nodes: $(kubectl --kubeconfig="$kubeconfig" get nodes --no-headers 2>/dev/null | wc -l)"
        echo "    Namespaces: $(kubectl --kubeconfig="$kubeconfig" get ns --no-headers 2>/dev/null | wc -l)"
        echo "    Pods: $(kubectl --kubeconfig="$kubeconfig" get pods --all-namespaces --no-headers 2>/dev/null | wc -l)"
        echo "    Deployments: $(kubectl --kubeconfig="$kubeconfig" get deployments --all-namespaces --no-headers 2>/dev/null | wc -l)"
        echo ""
    done
}

# Main
main() {
    log_info "Kseer Stress Test Runner"
    echo ""
    
    check_clusters
    show_current_state
    
    log_step "Starting stress test..."
    echo ""
    echo "This will:"
    echo "  ✓ Create 5 namespaces in each cluster"
    echo "  ✓ Generate diverse K8s objects (Deployments, Services, Jobs, etc.)"
    echo "  ✓ Continuously modify resources every few seconds"
    echo "  ✓ Create realistic workload patterns"
    echo ""
    echo "Press Ctrl+C to stop the stress test at any time"
    echo ""
    
    # Give user a chance to cancel
    sleep 3
    
    # Run stress test
    exec "$SCRIPT_DIR/stress-test.sh" run
}

# Handle command line args
case "${1:-}" in
    cleanup)
        log_info "Cleaning up stress test resources..."
        exec "$SCRIPT_DIR/stress-test.sh" cleanup
        ;;
    stats)
        exec "$SCRIPT_DIR/stress-test.sh" stats
        ;;
    setup)
        check_clusters
        exec "$SCRIPT_DIR/stress-test.sh" setup
        ;;
    *)
        main
        ;;
esac

