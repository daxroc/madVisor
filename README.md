# madVisor

[![GitHub](https://img.shields.io/badge/github-daxroc/madVisor-blue)](https://github.com/daxroc/madVisor)

A real-time terminal dashboard that visualizes Prometheus metrics from containers in a Kubernetes pod. Runs as a sidecar or ephemeral debug container — only active when a TTY is attached, near-zero overhead otherwise.

## Architecture

```
┌─── Pod ──────────────────────────────────────────┐
│                                                   │
│  ┌──────────────┐        ┌────────────────────┐   │
│  │ app container │        │  madVisor sidecar  │   │
│  │ :8080/metrics │◄───────│  (termdash TUI)    │   │
│  └──────────────┘  localhost  └────────────────┘   │
│                                                   │
└───────────────────────────────────────────────────┘
```

## Features

- **Metric browser** — scrollable list of all discovered series with live values
- **Label-aware** — parses full Prometheus exposition format including `{key="val"}` labels
- **Interactive filter** — press `/` to filter metrics by name or label substring
- **Live chart** — large line chart for the selected metric with 120-sample history
- **TTY guard** — idles with zero CPU when no terminal is attached
- **Ephemeral inject** — attach to any running pod without redeployment

## Quick Start (Local)

```bash
# Run both processes locally (dummy metrics on :8080, TUI pointing at it)
make run-local
```

Or run them separately in two terminals:

```bash
# Terminal 1: start the dummy metrics producer
make run-dummy

# Terminal 2: start madVisor
make run-viz
```

### Keyboard Controls

| Key | Action |
|---|---|
| `↑` / `↓` or `j` / `k` | Navigate metric list |
| `/` | Enter filter mode |
| `Backspace` | Delete filter character |
| `Enter` | Confirm filter |
| `Esc` | Clear filter (or quit if no filter) |
| `Q` | Quit |

## Examples

See the [`examples/`](examples/) directory for ready-to-use deployment configurations:

- **[Docker Compose](examples/docker-compose/)** — run madVisor alongside a dummy metrics producer locally
- **[Kubernetes Pod](examples/k8s/)** — deploy as a sidecar in a pod manifest
- **[Ephemeral Inject](examples/k8s/inject-sidecar.sh)** — attach madVisor to any running pod via `kubectl debug`

### Quick Kubernetes Deploy

```bash
# Build container images
make docker-build

# Deploy the demo pod
kubectl apply -f examples/k8s/pod.yaml

# Attach to madVisor
kubectl attach -it madvisor-demo -c madvisor

# Clean up
kubectl delete -f examples/k8s/pod.yaml
```

### Inject into a Running Pod

```bash
# Attach to any pod that exposes metrics
./examples/k8s/inject-sidecar.sh <pod-name> <metric-port>
```

## Configuration

| Env Var | Default | Description |
|---|---|---|
| `METRIC_TARGETS` | `localhost:8080` | Comma-separated `host:port` list of Prometheus endpoints to scrape |
| `TERM` | `xterm-256color` | Terminal type for color support |

## How It Works

1. **TTY guard** — on startup, checks if stdin is a terminal. If not, idles with near-zero CPU until a terminal is attached.
2. **Scraper** — polls each target's `/metrics` endpoint every second, parsing the Prometheus exposition format with full label support.
3. **Ring buffer** — stores the last 120 samples per metric series for chart rendering.
4. **TUI** — interactive dashboard built with [termdash](https://github.com/mum4k/termdash): metric list on the right, live line chart on the left, with filtering and keyboard navigation.

## Project Structure

```
cmd/
  madvisor/           # The madVisor TUI binary
  madvisor-dummy/     # Fake workload producing synthetic labeled metrics
docker/
  Dockerfile.madvisor
  Dockerfile.madvisor-dummy
examples/
  docker-compose/     # Docker Compose example
  k8s/                # Kubernetes pod manifest + ephemeral inject script
```
