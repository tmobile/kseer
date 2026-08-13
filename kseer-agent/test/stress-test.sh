#!/bin/bash

# Kseer Stress Test Script
# Generates diverse Kubernetes objects and continuously modifies them
# to stress test the UI with lots of changes and variety

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Configuration
NUM_NAMESPACES=5
BASE_DEPLOYMENTS=10
BASE_SERVICES=8
BASE_CONFIGMAPS=15
BASE_SECRETS=8
BASE_JOBS=5
CHANGE_INTERVAL=3  # seconds between changes
BATCH_SIZE=3       # number of changes per cycle

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
NC='\033[0m'

# Track counters
COUNTER=0
DEPLOYMENT_COUNTER=0
POD_COUNTER=0
CONFIG_COUNTER=0
JOB_COUNTER=0
SCALE_COUNTER=0

log() {
    local color=$1
    local label=$2
    shift 2
    echo -e "${color}[$(date '+%H:%M:%S')] [${label}]${NC} $@"
}

log_create() { log "$GREEN" "CREATE" "$@"; }
log_update() { log "$YELLOW" "UPDATE" "$@"; }
log_delete() { log "$RED" "DELETE" "$@"; }
log_scale() { log "$BLUE" "SCALE" "$@"; }
log_info() { log "$CYAN" "INFO" "$@"; }
log_cycle() { log "$MAGENTA" "CYCLE" "$@"; }

# Get random cluster kubeconfig
get_random_cluster() {
    local clusters=(
        "$SCRIPT_DIR/kubeconfigs/kseer-test-1.kubeconfig"
        "$SCRIPT_DIR/kubeconfigs/kseer-test-2.kubeconfig"
    )
    echo "${clusters[$((RANDOM % 2))]}"
}

# Get random namespace from available
get_random_namespace() {
    local namespaces=(default stress-test-1 stress-test-2 stress-test-3 stress-test-4 stress-test-5)
    echo "${namespaces[$((RANDOM % ${#namespaces[@]}))]}"
}

# Generate random labels
generate_labels() {
    local envs=(dev staging prod test qa)
    local teams=(frontend backend platform data mobile)
    local apps=(api worker scheduler processor collector analyzer dashboard monitor)
    
    local env="${envs[$((RANDOM % ${#envs[@]}))]}"
    local team="${teams[$((RANDOM % ${#teams[@]}))]}"
    local app="${apps[$((RANDOM % ${#apps[@]}))]}"
    
    echo "environment=$env,team=$team,app=$app,stress-test=true"
}

# Setup: Create namespaces and initial workloads
setup_initial_state() {
    log_info "Setting up initial state..."
    
    for kubeconfig in "$SCRIPT_DIR/kubeconfigs/"*.kubeconfig; do
        local cluster_name=$(basename "$kubeconfig" .kubeconfig)
        log_info "Initializing $cluster_name..."
        
        # Create namespaces
        for i in $(seq 1 $NUM_NAMESPACES); do
            kubectl --kubeconfig="$kubeconfig" create namespace "stress-test-$i" --dry-run=client -o yaml | \
                kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 || true
        done
        
        # Create initial deployments
        for i in $(seq 1 $BASE_DEPLOYMENTS); do
            create_deployment "$kubeconfig" "$(get_random_namespace)" "initial-deploy-$i" $((RANDOM % 3 + 1))
        done
        
        # Create services
        for i in $(seq 1 $BASE_SERVICES); do
            create_service "$kubeconfig" "$(get_random_namespace)" "service-$i"
        done
        
        # Create configmaps
        for i in $(seq 1 $BASE_CONFIGMAPS); do
            create_configmap "$kubeconfig" "$(get_random_namespace)" "config-$i"
        done
        
        # Create secrets
        for i in $(seq 1 $BASE_SECRETS); do
            create_secret "$kubeconfig" "$(get_random_namespace)" "secret-$i"
        done
        
        # Create initial jobs
        for i in $(seq 1 $BASE_JOBS); do
            create_job "$kubeconfig" "$(get_random_namespace)" "init-job-$i"
        done
        
        # Create some statefulsets
        create_statefulset "$kubeconfig" "$(get_random_namespace)" "stateful-app-1"
        create_statefulset "$kubeconfig" "$(get_random_namespace)" "stateful-app-2"
        
        # Create daemonsets
        create_daemonset "$kubeconfig" "$(get_random_namespace)" "daemon-monitor"
        
        # Create network policies
        create_network_policy "$kubeconfig" "$(get_random_namespace)" "default-deny"
        
        # Create service accounts and RBAC
        create_service_account "$kubeconfig" "$(get_random_namespace)" "app-sa"
        create_role "$kubeconfig" "$(get_random_namespace)" "app-role"
        create_rolebinding "$kubeconfig" "$(get_random_namespace)" "app-binding" "app-sa" "app-role"
        
        # Create PVCs
        create_pvc "$kubeconfig" "$(get_random_namespace)" "data-pvc-1"
        create_pvc "$kubeconfig" "$(get_random_namespace)" "data-pvc-2"
        
        # Create cronjobs
        create_cronjob "$kubeconfig" "$(get_random_namespace)" "backup-job"
        create_cronjob "$kubeconfig" "$(get_random_namespace)" "cleanup-job"
    done
    
    log_info "Initial setup complete!"
}

# Create deployment
create_deployment() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local replicas=$4
    local labels=$(generate_labels)
    
    local images=(nginx:alpine redis:alpine busybox postgres:alpine httpd:alpine)
    local image="${images[$((RANDOM % ${#images[@]}))]}"
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $name
  namespace: $namespace
  labels:
    $(echo "$labels" | tr ',' '\n' | sed 's/^/    /')
    resource-type: deployment
  annotations:
    stress-test: "true"
    created-at: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
spec:
  replicas: $replicas
  selector:
    matchLabels:
      app: $name
  template:
    metadata:
      labels:
        app: $name
        $(echo "$labels" | tr ',' '\n' | sed 's/^/        /')
    spec:
      containers:
      - name: app
        image: $image
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 200m
            memory: 256Mi
EOF
    DEPLOYMENT_COUNTER=$((DEPLOYMENT_COUNTER + 1))
}

# Create service
create_service() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local labels=$(generate_labels)
    
    local types=(ClusterIP NodePort)
    local type="${types[$((RANDOM % ${#types[@]}))]}"
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: v1
kind: Service
metadata:
  name: $name
  namespace: $namespace
  labels:
    $(echo "$labels" | tr ',' '\n' | sed 's/^/    /')
    resource-type: service
spec:
  type: $type
  selector:
    app: $name
  ports:
  - port: 80
    targetPort: 80
    protocol: TCP
EOF
}

# Create configmap
create_configmap() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local labels=$(generate_labels)
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: $name
  namespace: $namespace
  labels:
    $(echo "$labels" | tr ',' '\n' | sed 's/^/    /')
    resource-type: configmap
data:
  config.yaml: |
    server:
      port: 8080
      host: 0.0.0.0
    database:
      host: postgres
      port: 5432
    cache:
      enabled: true
      ttl: 3600
  settings.json: |
    {
      "debug": true,
      "log_level": "info",
      "features": ["auth", "api", "metrics"]
    }
  script.sh: |
    #!/bin/bash
    echo "Running initialization..."
    sleep 5
    echo "Done!"
EOF
    CONFIG_COUNTER=$((CONFIG_COUNTER + 1))
}

# Create secret
create_secret() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local labels=$(generate_labels)
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: $name
  namespace: $namespace
  labels:
    $(echo "$labels" | tr ',' '\n' | sed 's/^/    /')
    resource-type: secret
type: Opaque
stringData:
  username: "admin-$(date +%s)"
  password: "$(openssl rand -base64 12)"
  api-key: "key-$(uuidgen | tr '[:upper:]' '[:lower:]')"
  database-url: "postgresql://user:pass@postgres:5432/mydb"
EOF
}

# Create job
create_job() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local labels=$(generate_labels)
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $name
  namespace: $namespace
  labels:
    $(echo "$labels" | tr ',' '\n' | sed 's/^/    /')
    resource-type: job
spec:
  backoffLimit: 3
  template:
    metadata:
      labels:
        app: $name
        $(echo "$labels" | tr ',' '\n' | sed 's/^/        /')
    spec:
      containers:
      - name: worker
        image: busybox
        command: ["sh", "-c"]
        args:
        - |
          echo "Job started at \$(date)"
          sleep $((RANDOM % 30 + 10))
          echo "Job completed successfully"
      restartPolicy: Never
EOF
    JOB_COUNTER=$((JOB_COUNTER + 1))
}

# Create statefulset
create_statefulset() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local labels=$(generate_labels)
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: $name
  namespace: $namespace
  labels:
    $(echo "$labels" | tr ',' '\n' | sed 's/^/    /')
    resource-type: statefulset
spec:
  serviceName: $name
  replicas: 2
  selector:
    matchLabels:
      app: $name
  template:
    metadata:
      labels:
        app: $name
        $(echo "$labels" | tr ',' '\n' | sed 's/^/        /')
    spec:
      containers:
      - name: app
        image: nginx:alpine
        ports:
        - containerPort: 80
        volumeMounts:
        - name: data
          mountPath: /data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: [ "ReadWriteOnce" ]
      resources:
        requests:
          storage: 1Gi
EOF
}

# Create daemonset
create_daemonset() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local labels=$(generate_labels)
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: $name
  namespace: $namespace
  labels:
    $(echo "$labels" | tr ',' '\n' | sed 's/^/    /')
    resource-type: daemonset
spec:
  selector:
    matchLabels:
      app: $name
  template:
    metadata:
      labels:
        app: $name
        $(echo "$labels" | tr ',' '\n' | sed 's/^/        /')
    spec:
      containers:
      - name: monitor
        image: busybox
        command: ["sh", "-c", "while true; do date; sleep 60; done"]
        resources:
          limits:
            memory: 64Mi
            cpu: 50m
EOF
}

# Create network policy
create_network_policy() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: $name
  namespace: $namespace
  labels:
    resource-type: networkpolicy
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
  ingress:
  - from:
    - podSelector: {}
  egress:
  - to:
    - podSelector: {}
EOF
}

# Create service account
create_service_account() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: $name
  namespace: $namespace
  labels:
    resource-type: serviceaccount
EOF
}

# Create role
create_role() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: $name
  namespace: $namespace
  labels:
    resource-type: role
rules:
- apiGroups: [""]
  resources: ["pods", "services"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "list"]
EOF
}

# Create rolebinding
create_rolebinding() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local sa=$4
    local role=$5
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: $name
  namespace: $namespace
  labels:
    resource-type: rolebinding
subjects:
- kind: ServiceAccount
  name: $sa
  namespace: $namespace
roleRef:
  kind: Role
  name: $role
  apiGroup: rbac.authorization.k8s.io
EOF
}

# Create PVC
create_pvc() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $name
  namespace: $namespace
  labels:
    resource-type: pvc
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
EOF
}

# Create cronjob
create_cronjob() {
    local kubeconfig=$1
    local namespace=$2
    local name=$3
    local labels=$(generate_labels)
    
    kubectl --kubeconfig="$kubeconfig" apply -f - >/dev/null 2>&1 <<EOF
apiVersion: batch/v1
kind: CronJob
metadata:
  name: $name
  namespace: $namespace
  labels:
    $(echo "$labels" | tr ',' '\n' | sed 's/^/    /')
    resource-type: cronjob
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: job
            image: busybox
            command: ["sh", "-c", "echo 'Running scheduled task'; date"]
          restartPolicy: OnFailure
EOF
}

# Continuous change operations
perform_random_changes() {
    local kubeconfig=$(get_random_cluster)
    local namespace=$(get_random_namespace)
    local cluster_name=$(basename "$kubeconfig" .kubeconfig)
    
    # Random operation selection
    local operations=(
        "scale_deployment"
        "create_pod"
        "create_deployment"
        "update_configmap"
        "create_job"
        "delete_old_jobs"
        "update_deployment_labels"
        "create_service"
        "delete_old_pods"
        "patch_deployment_image"
    )
    
    local operation="${operations[$((RANDOM % ${#operations[@]}))]}"
    
    case $operation in
        scale_deployment)
            local deployments=$(kubectl --kubeconfig="$kubeconfig" get deployments -n "$namespace" -o name 2>/dev/null | head -5)
            if [ ! -z "$deployments" ]; then
                local deployment=$(echo "$deployments" | shuf -n 1)
                local replicas=$((RANDOM % 5 + 1))
                kubectl --kubeconfig="$kubeconfig" scale -n "$namespace" "$deployment" --replicas=$replicas >/dev/null 2>&1
                log_scale "$cluster_name/$namespace: $(basename $deployment) -> $replicas replicas"
                SCALE_COUNTER=$((SCALE_COUNTER + 1))
            fi
            ;;
            
        create_pod)
            local pod_name="stress-pod-$POD_COUNTER-$RANDOM"
            kubectl --kubeconfig="$kubeconfig" run "$pod_name" \
                --namespace="$namespace" \
                --image=busybox \
                --labels="$(generate_labels)" \
                --restart=Never \
                --command -- sh -c "echo 'Pod $pod_name running'; sleep $((RANDOM % 120 + 60))" \
                >/dev/null 2>&1 || true
            log_create "$cluster_name/$namespace: Pod $pod_name"
            POD_COUNTER=$((POD_COUNTER + 1))
            ;;
            
        create_deployment)
            local deploy_name="stress-deploy-$DEPLOYMENT_COUNTER-$((RANDOM % 1000))"
            create_deployment "$kubeconfig" "$namespace" "$deploy_name" $((RANDOM % 3 + 1))
            log_create "$cluster_name/$namespace: Deployment $deploy_name"
            ;;
            
        update_configmap)
            local cm_name="stress-config-$CONFIG_COUNTER-$RANDOM"
            create_configmap "$kubeconfig" "$namespace" "$cm_name"
            log_update "$cluster_name/$namespace: ConfigMap $cm_name"
            ;;
            
        create_job)
            local job_name="stress-job-$JOB_COUNTER-$RANDOM"
            create_job "$kubeconfig" "$namespace" "$job_name"
            log_create "$cluster_name/$namespace: Job $job_name"
            ;;
            
        delete_old_jobs)
            local old_jobs=$(kubectl --kubeconfig="$kubeconfig" get jobs -n "$namespace" \
                --field-selector status.successful=1 -o name 2>/dev/null | head -3)
            if [ ! -z "$old_jobs" ]; then
                echo "$old_jobs" | while read job; do
                    kubectl --kubeconfig="$kubeconfig" delete -n "$namespace" "$job" >/dev/null 2>&1 || true
                    log_delete "$cluster_name/$namespace: $(basename $job)"
                done
            fi
            ;;
            
        update_deployment_labels)
            local deployments=$(kubectl --kubeconfig="$kubeconfig" get deployments -n "$namespace" -o name 2>/dev/null | head -5)
            if [ ! -z "$deployments" ]; then
                local deployment=$(echo "$deployments" | shuf -n 1)
                local new_label="version=v$((RANDOM % 10 + 1))"
                kubectl --kubeconfig="$kubeconfig" label -n "$namespace" "$deployment" "$new_label" --overwrite >/dev/null 2>&1
                log_update "$cluster_name/$namespace: $(basename $deployment) labeled $new_label"
            fi
            ;;
            
        create_service)
            local svc_name="stress-svc-$RANDOM"
            create_service "$kubeconfig" "$namespace" "$svc_name"
            log_create "$cluster_name/$namespace: Service $svc_name"
            ;;
            
        delete_old_pods)
            local old_pods=$(kubectl --kubeconfig="$kubeconfig" get pods -n "$namespace" \
                --field-selector status.phase=Succeeded -o name 2>/dev/null | head -5)
            if [ ! -z "$old_pods" ]; then
                echo "$old_pods" | while read pod; do
                    kubectl --kubeconfig="$kubeconfig" delete -n "$namespace" "$pod" >/dev/null 2>&1 || true
                    log_delete "$cluster_name/$namespace: $(basename $pod)"
                done
            fi
            ;;
            
        patch_deployment_image)
            local deployments=$(kubectl --kubeconfig="$kubeconfig" get deployments -n "$namespace" -o name 2>/dev/null | head -5)
            if [ ! -z "$deployments" ]; then
                local deployment=$(echo "$deployments" | shuf -n 1)
                local images=(nginx:alpine nginx:1.21 nginx:1.20 httpd:alpine redis:alpine)
                local new_image="${images[$((RANDOM % ${#images[@]}))]}"
                kubectl --kubeconfig="$kubeconfig" set image -n "$namespace" "$deployment" app="$new_image" >/dev/null 2>&1 || true
                log_update "$cluster_name/$namespace: $(basename $deployment) image -> $new_image"
            fi
            ;;
    esac
}

# Statistics display
show_stats() {
    log_info "=== Stress Test Statistics ==="
    echo "  Cycles: $COUNTER"
    echo "  Deployments created: $DEPLOYMENT_COUNTER"
    echo "  Pods created: $POD_COUNTER"
    echo "  ConfigMaps created: $CONFIG_COUNTER"
    echo "  Jobs created: $JOB_COUNTER"
    echo "  Scale operations: $SCALE_COUNTER"
    echo ""
    
    for kubeconfig in "$SCRIPT_DIR/kubeconfigs/"*.kubeconfig; do
        local cluster_name=$(basename "$kubeconfig" .kubeconfig)
        echo "  $cluster_name:"
        echo "    Namespaces: $(kubectl --kubeconfig="$kubeconfig" get ns --no-headers 2>/dev/null | wc -l)"
        echo "    Deployments: $(kubectl --kubeconfig="$kubeconfig" get deployments --all-namespaces --no-headers 2>/dev/null | wc -l)"
        echo "    Pods: $(kubectl --kubeconfig="$kubeconfig" get pods --all-namespaces --no-headers 2>/dev/null | wc -l)"
        echo "    Services: $(kubectl --kubeconfig="$kubeconfig" get services --all-namespaces --no-headers 2>/dev/null | wc -l)"
        echo "    Jobs: $(kubectl --kubeconfig="$kubeconfig" get jobs --all-namespaces --no-headers 2>/dev/null | wc -l)"
    done
}

# Main stress test loop
run_stress_test() {
    log_info "Starting Kubernetes stress test..."
    log_info "Change interval: ${CHANGE_INTERVAL}s, Batch size: $BATCH_SIZE"
    log_info "Press Ctrl+C to stop"
    echo ""
    
    setup_initial_state
    
    log_info "Starting continuous change generation..."
    sleep 3
    
    while true; do
        COUNTER=$((COUNTER + 1))
        
        # Perform batch of changes
        for i in $(seq 1 $BATCH_SIZE); do
            perform_random_changes &
        done
        
        # Wait for batch to complete
        wait
        
        log_cycle "Cycle $COUNTER complete - $BATCH_SIZE changes applied"
        
        # Show stats every 20 cycles
        if [ $((COUNTER % 20)) -eq 0 ]; then
            echo ""
            show_stats
            echo ""
        fi
        
        sleep $CHANGE_INTERVAL
    done
}

# Cleanup old stress test resources
cleanup_stress_test() {
    log_info "Cleaning up stress test resources..."
    
    for kubeconfig in "$SCRIPT_DIR/kubeconfigs/"*.kubeconfig; do
        local cluster_name=$(basename "$kubeconfig" .kubeconfig)
        log_info "Cleaning $cluster_name..."
        
        # Delete stress test namespaces
        for i in $(seq 1 $NUM_NAMESPACES); do
            kubectl --kubeconfig="$kubeconfig" delete namespace "stress-test-$i" \
                --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
        done
        
        # Delete stress test resources in default namespace
        kubectl --kubeconfig="$kubeconfig" delete all,configmaps,secrets,pvc,networkpolicies,roles,rolebindings,serviceaccounts \
            -n default -l stress-test=true --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
    done
    
    log_info "Cleanup complete"
}

# Handle signals
trap 'echo ""; log_info "Stopping stress test..."; show_stats; exit 0' INT TERM

# Main
case "${1:-run}" in
    run)
        run_stress_test
        ;;
    setup)
        setup_initial_state
        log_info "Initial state setup complete. Run '$0' to start continuous changes."
        ;;
    cleanup)
        cleanup_stress_test
        ;;
    stats)
        show_stats
        ;;
    *)
        echo "Usage: $0 {run|setup|cleanup|stats}"
        echo "  run     - Run full stress test (setup + continuous changes)"
        echo "  setup   - Only setup initial state"
        echo "  cleanup - Remove all stress test resources"
        echo "  stats   - Show current statistics"
        exit 1
        ;;
esac

