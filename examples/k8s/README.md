# Kubernetes Examples

## Pod with Sidecar

Deploy a pod with the dummy metrics producer and madVisor as a sidecar:

```bash
# Build images first (if using local registry)
make docker-build

# Deploy the pod
kubectl apply -f pod.yaml

# Attach to the visualizer
kubectl attach -it madvisor-demo -c madvisor

# Clean up
kubectl delete -f pod.yaml
```

## Inject into a Running Pod

Use `inject-sidecar.sh` to add madVisor as an ephemeral debug container to any running pod that exposes Prometheus metrics:

```bash
# Inject into a pod, scraping metrics on port 9090
./inject-sidecar.sh <pod-name> 9090

# Inject with a specific namespace
./inject-sidecar.sh <pod-name> 8080 my-namespace

# Inject targeting multiple ports
./inject-sidecar.sh <pod-name> "8080,9090"
```

### Requirements

- Kubernetes 1.25+ (ephemeral containers support)
- `kubectl` configured with appropriate RBAC permissions
- The `dcroche/madvisor:latest` image available to the cluster
