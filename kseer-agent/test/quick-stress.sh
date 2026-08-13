#!/bin/bash
COUNTER=0
echo "Starting quick stress test..."

while true; do
    COUNTER=$((COUNTER + 1))
    CLUSTER=$((RANDOM % 2 + 1))
    NS="stress-test-$((RANDOM % 5 + 1))"
    KUBECONFIG="kubeconfigs/kseer-test-$CLUSTER.kubeconfig"
    
    # Create deployment
    if [ $((COUNTER % 3)) -eq 0 ]; then
        NAME="app-$COUNTER-$RANDOM"
        kubectl --kubeconfig=$KUBECONFIG create deployment $NAME --image=nginx:alpine -n $NS >/dev/null 2>&1 && \
            echo "[CREATE] Cluster $CLUSTER/$NS: Deployment $NAME"
    fi
    
    # Scale deployment
    if [ $((COUNTER % 2)) -eq 0 ]; then
        DEPLOY=$(kubectl --kubeconfig=$KUBECONFIG get deploy -n $NS -o name 2>/dev/null | shuf -n 1)
        if [ ! -z "$DEPLOY" ]; then
            REPLICAS=$((RANDOM % 5 + 1))
            kubectl --kubeconfig=$KUBECONFIG scale -n $NS $DEPLOY --replicas=$REPLICAS >/dev/null 2>&1 && \
                echo "[SCALE] Cluster $CLUSTER/$NS: $DEPLOY -> $REPLICAS replicas"
        fi
    fi
    
    # Create configmap
    if [ $((COUNTER % 4)) -eq 0 ]; then
        NAME="config-$COUNTER-$RANDOM"
        kubectl --kubeconfig=$KUBECONFIG create configmap $NAME --from-literal="data=$COUNTER" -n $NS >/dev/null 2>&1 && \
            echo "[CREATE] Cluster $CLUSTER/$NS: ConfigMap $NAME"
    fi
    
    # Create service
    if [ $((COUNTER % 5)) -eq 0 ]; then
        NAME="svc-$COUNTER-$RANDOM"
        kubectl --kubeconfig=$KUBECONFIG create service clusterip $NAME --tcp=80:80 -n $NS >/dev/null 2>&1 && \
            echo "[CREATE] Cluster $CLUSTER/$NS: Service $NAME"
    fi
    
    # Show stats every 10 cycles
    if [ $((COUNTER % 10)) -eq 0 ]; then
        echo "=== Stats: $COUNTER cycles completed ==="
    fi
    
    sleep 2
done
