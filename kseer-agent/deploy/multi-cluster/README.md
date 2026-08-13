# Multi-Cluster Deployment

This directory contains resources for deploying kseer-agent in **multi-cluster mode**, where a single agent instance watches multiple Kubernetes clusters from an external location.

## Overview

In multi-cluster mode, the agent:
- Runs outside of Kubernetes clusters (as a binary, container, or systemd service)
- Connects to multiple clusters using kubeconfig files
- Requires network access to all cluster API servers
- Uses kubeconfig credentials for authentication
- Centralizes monitoring of multiple clusters

## Quick Start

### Using Docker Compose

```bash
# 1. Copy and customize the config
cp config-example.yaml config.yaml
# Edit config.yaml with your cluster details

# 2. Create kubeconfigs directory and add your cluster kubeconfigs
mkdir -p kubeconfigs
cp ~/.kube/config kubeconfigs/cluster-1.yaml
# Add more kubeconfigs as needed

# 3. Start the agent
docker-compose up -d

# 4. View logs
docker-compose logs -f kseer-agent
```

### Using Binary

```bash
# 1. Build or download the binary
go build -o kseer-agent ../../cmd/agent/main.go

# Or use the provided binary
cp ../../kseer-agent .

# 2. Create configuration
cp config-example.yaml config.yaml
# Edit config.yaml

# 3. Run the agent
./kseer-agent --config=config.yaml
```

### Using Systemd

```bash
# 1. Install the binary
sudo cp kseer-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/kseer-agent

# 2. Create user and directories
sudo useradd -r -s /bin/false kseer
sudo mkdir -p /etc/kseer-agent
sudo mkdir -p /var/lib/kseer-agent

# 3. Install configuration
sudo cp config.yaml /etc/kseer-agent/
sudo cp -r kubeconfigs /etc/kseer-agent/
sudo chown -R kseer:kseer /etc/kseer-agent /var/lib/kseer-agent

# 4. Install and start service
sudo cp systemd/kseer-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable kseer-agent
sudo systemctl start kseer-agent

# 5. Check status
sudo systemctl status kseer-agent
sudo journalctl -u kseer-agent -f
```

## Configuration

### Basic Configuration Structure

```yaml
mode: multi-cluster

clusters:
  - name: "cluster-name"
    kubeconfig: "/path/to/kubeconfig.yaml"
    context: "context-name"  # Optional, uses default if empty
    namespaces: []  # Empty = all namespaces
    resources: []   # Empty = all resources
    watch_config:   # Per-cluster overrides of default_watch_config
      resync_interval: "5m"

outputs:
  - type: kafka
    config:
      brokers: ["kafka:9092"]
      topic: "kubernetes-events"

buffer:
  size: 10000
  flush_interval: "5s"
  retry_interval: "30s"
```

### Kubeconfig Management

#### Separate Files Per Cluster

```yaml
clusters:
  - name: "prod-1"
    kubeconfig: "/etc/kseer-agent/kubeconfigs/prod-1.yaml"
  - name: "prod-2"
    kubeconfig: "/etc/kseer-agent/kubeconfigs/prod-2.yaml"
```

#### Merged Kubeconfig with Contexts

```bash
# Merge multiple kubeconfigs
KUBECONFIG=cluster1.yaml:cluster2.yaml:cluster3.yaml \
  kubectl config view --flatten > merged.yaml
```

```yaml
clusters:
  - name: "prod-1"
    kubeconfig: "/etc/kseer-agent/merged.yaml"
    context: "prod-1-context"
  - name: "prod-2"
    kubeconfig: "/etc/kseer-agent/merged.yaml"
    context: "prod-2-context"
```

### Filtering Resources

```yaml
# Watch only specific resource types
clusters:
  - name: "production"
    resources:
      - apiVersion: "v1"
        kind: "Pod"
      - apiVersion: "v1"
        kind: "Service"
      - apiVersion: "apps/v1"
        kind: "Deployment"
      - apiVersion: "apps/v1"
        kind: "StatefulSet"
```

### Filtering Namespaces

```yaml
# Watch only specific namespaces
clusters:
  - name: "production"
    namespaces:
      - default
      - production
      - api-services
```

## Scaling Considerations

### For Many Clusters

```yaml
# Increase buffer size
buffer:
  size: 50000
  flush_interval: "3s"

# Adjust resync intervals for all clusters at once
default_watch_config:
  resync_interval: "15m"  # Less frequent for many clusters
```

### Resource Requirements

- **Small deployment (1-3 clusters)**: 256MB RAM, 0.1 CPU
- **Medium deployment (4-10 clusters)**: 512MB RAM, 0.5 CPU
- **Large deployment (10+ clusters)**: 1-2GB RAM, 1-2 CPU

## Monitoring

### Health Checks

```bash
# Check if process is running
pgrep -f kseer-agent

# View resource usage
ps aux | grep kseer-agent
```

### Logs

```bash
# Systemd
sudo journalctl -u kseer-agent -f

# Docker
docker logs -f kseer-agent

# Binary (if redirected)
tail -f /var/log/kseer-agent.log
```

### Metrics

Enable stdout output for debugging:

```yaml
outputs:
  - type: stdout
    config:
      pretty: true
```

## Security

### Kubeconfig Permissions

```bash
# Restrict kubeconfig access
chmod 600 /etc/kseer-agent/kubeconfigs/*.yaml
chown kseer:kseer /etc/kseer-agent/kubeconfigs/*.yaml
```

### Network Security

- Ensure firewall rules allow access to cluster API servers
- Use VPN or private network when possible
- Consider using kubectl proxy or port forwarding for additional security

### RBAC in Clusters

Create read-only service accounts in each cluster:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kseer-agent
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kseer-agent
rules:
- apiGroups: ["*"]
  resources: ["*"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kseer-agent
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kseer-agent
subjects:
- kind: ServiceAccount
  name: kseer-agent
  namespace: kube-system
```

Then generate kubeconfig for this service account.

## Troubleshooting

### Connection Issues

```bash
# Test cluster connectivity
kubectl --kubeconfig=/path/to/kubeconfig.yaml cluster-info

# Test network access to API server
curl -k https://<cluster-api>:6443
```

### High Memory Usage

- Reduce number of resources watched
- Filter by namespaces
- Increase resync interval
- Reduce buffer size

### Events Not Reaching Output

- Check Kafka connectivity
- Verify authentication credentials
- Check agent logs for errors
- Ensure buffer is not full

## Examples

See the included files:
- `config-example.yaml` - Comprehensive configuration example
- `docker-compose.yaml` - Docker Compose setup
- `systemd/kseer-agent.service` - Systemd service unit

## Migration from In-Cluster

If migrating from in-cluster mode:

1. Export kubeconfig for service account
2. Update mode to `multi-cluster`
3. Specify kubeconfig path
4. Deploy externally
5. Remove in-cluster Helm deployment

```bash
# Export kubeconfig from service account
kubectl get secret <sa-token-secret> -o jsonpath='{.data.token}' | base64 -d > token
kubectl config set-cluster mycluster --server=https://api.cluster.com:6443
kubectl config set-credentials kseer --token=$(cat token)
kubectl config set-context kseer@mycluster --cluster=mycluster --user=kseer
kubectl config use-context kseer@mycluster
```

