# Examples

## Docker Compose

Run madVisor locally alongside a dummy metrics producer using Docker Compose.

```bash
cd docker-compose
docker compose up --build
# In another terminal:
docker attach $(docker compose ps -q madvisor)
```

See [docker-compose/README.md](docker-compose/README.md) for details.

## Kubernetes — Pod with Sidecar

Deploy a pod with madVisor as a sidecar container:

```bash
cd k8s
kubectl apply -f pod.yaml
kubectl attach -it madvisor-demo -c madvisor
```

See [k8s/README.md](k8s/README.md) for details.

## Kubernetes — Inject into Running Pod

Attach madVisor to any running pod using ephemeral containers (K8s 1.25+):

```bash
cd k8s
./inject-sidecar.sh <pod-name> <metric-port>
```

This uses `kubectl debug` to inject a temporary madVisor container that shares the pod's network namespace, letting it scrape `localhost:<port>/metrics` from any container in the pod.
