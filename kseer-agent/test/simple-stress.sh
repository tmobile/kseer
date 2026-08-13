#!/bin/bash

# Simple but effective stress test for macOS
# Generates diverse Kubernetes objects across both clusters

COUNTER=0
DEPLOY_COUNT=0
CONFIG_COUNT=0
SVC_COUNT=0
JOB_COUNT=0

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
NC='\033[0m'

echo -e "${CYAN}Starting Simple Stress Test${NC}"
echo "This will create and modify various Kubernetes resources"
echo "Press Ctrl+C to stop"
echo ""

# Create initial namespaces
echo -e "${BLUE}Creating stress test namespaces...${NC}"
for i in {1..3}; do
    kubectl --kubeconfig=kubeconfigs/kseer-test-1.kubeconfig create namespace stress-$i 2>/dev/null || true
    kubectl --kubeconfig=kubeconfigs/kseer-test-2.kubeconfig create namespace stress-$i 2>/dev/null || true
done

# Main loop
while true; do
    COUNTER=$((COUNTER + 1))
    
    # Select random cluster and namespace
    CLUSTER=$((RANDOM % 2 + 1))
    NAMESPACE="stress-$((RANDOM % 3 + 1))"
    KUBECONFIG="kubeconfigs/kseer-test-$CLUSTER.kubeconfig"
    
    # Perform random action
    ACTION=$((RANDOM % 10))
    
    case $ACTION in
        0|1|2)
            # Create deployment
            DEPLOY_COUNT=$((DEPLOY_COUNT + 1))
            NAME="app-$DEPLOY_COUNT"
            REPLICAS=$((RANDOM % 3 + 1))
            IMAGE=("nginx:alpine" "redis:alpine" "busybox" "httpd:alpine")
            IMG="${IMAGE[$((RANDOM % 4))]}"
            
            kubectl --kubeconfig=$KUBECONFIG create deployment $NAME \
                --image=$IMG \
                --replicas=$REPLICAS \
                -n $NAMESPACE >/dev/null 2>&1 && \
                echo -e "${GREEN}[CREATE]${NC} Cluster-$CLUSTER/$NAMESPACE: Deployment $NAME (image=$IMG, replicas=$REPLICAS)"
            ;;
            
        3|4)
            # Scale existing deployment
            DEPLOY=$(kubectl --kubeconfig=$KUBECONFIG get deploy -n $NAMESPACE -o name 2>/dev/null | sort -R | head -1)
            if [ ! -z "$DEPLOY" ]; then
                REPLICAS=$((RANDOM % 5 + 1))
                kubectl --kubeconfig=$KUBECONFIG scale -n $NAMESPACE $DEPLOY --replicas=$REPLICAS >/dev/null 2>&1 && \
                    echo -e "${YELLOW}[SCALE]${NC} Cluster-$CLUSTER/$NAMESPACE: $(basename $DEPLOY) -> $REPLICAS replicas"
            fi
            ;;
            
        5|6)
            # Create ConfigMap
            CONFIG_COUNT=$((CONFIG_COUNT + 1))
            NAME="config-$CONFIG_COUNT"
            kubectl --kubeconfig=$KUBECONFIG create configmap $NAME \
                -n $NAMESPACE \
                --from-literal="key1=value$COUNTER" \
                --from-literal="timestamp=$(date)" \
                --from-literal="cluster=$CLUSTER" >/dev/null 2>&1 && \
                echo -e "${GREEN}[CREATE]${NC} Cluster-$CLUSTER/$NAMESPACE: ConfigMap $NAME"
            ;;
            
        7)
            # Create Service
            SVC_COUNT=$((SVC_COUNT + 1))
            NAME="svc-$SVC_COUNT"
            kubectl --kubeconfig=$KUBECONFIG create service clusterip $NAME \
                --tcp=80:8080 \
                -n $NAMESPACE >/dev/null 2>&1 && \
                echo -e "${GREEN}[CREATE]${NC} Cluster-$CLUSTER/$NAMESPACE: Service $NAME"
            ;;
            
        8)
            # Create Job
            JOB_COUNT=$((JOB_COUNT + 1))
            NAME="job-$JOB_COUNT"
            kubectl --kubeconfig=$KUBECONFIG create job $NAME \
                --image=busybox \
                -n $NAMESPACE \
                -- sh -c "echo 'Job $NAME running'; sleep $((RANDOM % 30 + 10))" >/dev/null 2>&1 && \
                echo -e "${GREEN}[CREATE]${NC} Cluster-$CLUSTER/$NAMESPACE: Job $NAME"
            ;;
            
        9)
            # Update deployment labels
            DEPLOY=$(kubectl --kubeconfig=$KUBECONFIG get deploy -n $NAMESPACE -o name 2>/dev/null | sort -R | head -1)
            if [ ! -z "$DEPLOY" ]; then
                LABEL="version=v$((RANDOM % 10 + 1))"
                kubectl --kubeconfig=$KUBECONFIG label -n $NAMESPACE $DEPLOY $LABEL --overwrite >/dev/null 2>&1 && \
                    echo -e "${YELLOW}[UPDATE]${NC} Cluster-$CLUSTER/$NAMESPACE: $(basename $DEPLOY) label: $LABEL"
            fi
            ;;
    esac
    
    # Show stats every 20 operations
    if [ $((COUNTER % 20)) -eq 0 ]; then
        echo -e "\n${MAGENTA}=== Stats: $COUNTER operations completed ===${NC}"
        echo "Deployments: $DEPLOY_COUNT | ConfigMaps: $CONFIG_COUNT | Services: $SVC_COUNT | Jobs: $JOB_COUNT"
        
        # Quick resource count
        TOTAL_PODS=$(kubectl --kubeconfig=kubeconfigs/kseer-test-1.kubeconfig get pods -A --no-headers 2>/dev/null | wc -l)
        TOTAL_PODS2=$(kubectl --kubeconfig=kubeconfigs/kseer-test-2.kubeconfig get pods -A --no-headers 2>/dev/null | wc -l)
        echo "Total Pods: Cluster-1: $TOTAL_PODS | Cluster-2: $TOTAL_PODS2"
        echo ""
    fi
    
    # Short pause between operations
    sleep 1
done

