# madVisor

![madVisor screenshot](img/screenshot.png)

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

- **Metric name sidebar** — right panel lists discovered metric names with type badges (`[C]` counter, `[G]` gauge, `[H]` histogram, `[S]` summary) and series counts
- **Series detail panel** — bottom panel shows all series for the selected metric with labels, formatted values, and raw values
- **Live chart** — line chart with 120-sample history, auto-scaled Y-axis with unit-aware formatting
- **Metric type detection** — uses `# TYPE` annotations from the Prometheus scrape response
- **Unit-aware formatting** — automatically formats values based on metric name patterns: bytes (MiB/GiB), durations, percentages, timestamps (relative age), and counts
- **Customizable unit patterns** — regex-based patterns defined in YAML, overridable at startup
- **Regex filtering** — press `/` to filter metrics by name using regex (falls back to substring match)
- **Dual-panel navigation** — switch focus between metric list and series table with `Tab`
- **Rate calculation** — automatic `/s` rate display for counters and histogram/summary `_count`/`_sum` series, with adjustable time window
- **Label-aware** — parses full Prometheus exposition format including `{key="val"}` labels
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

## UI Layout

```
┌─────────────── Chart ───────────────┬──── Metrics ─────┐
│                                     │ [C] http_reqs  3 │
│   ╭───────╮                         │ [G] memory     1 │
│   │       ╰──╮    ╭──               │ [H] latency    5 │
│   ╯           ╰──╯                  │ [G] cpu_usage  2 │
│                                     │ [S] gc_pause   1 │
├─────────── Series ──────────────────│                  │
│ [C] http_requests_total — 3 series  │                  │
│ ▶ {method="GET"}  = 1.50k/s (4511) │                  │
│   {method="POST"} = 0.23/s (892)   │                  │
│   {method="PUT"}  = 0.01/s (45)    │                  │
├─────────── Status ──────────────────┴──────────────────┤
│ filter: /http  focus: sidebar  rate: 5s  targets: 1    │
└────────────────────────────────────────────────────────┘
```

### Keyboard Controls

| Key | Action |
|---|---|
| `↑` / `↓` or `j` / `k` | Navigate in the focused panel |
| `Tab` | Switch focus between metric list and series table |
| `/` | Enter filter mode (regex supported) |
| `Backspace` | Delete filter character |
| `Enter` | Confirm filter |
| `]` / `+` | Increase rate calculation window |
| `[` / `-` | Decrease rate calculation window |
| `Esc` | Clear filter (or quit if no filter) |
| `Q` | Quit |

## Unit Patterns

madVisor formats metric values based on regex patterns that match metric names. Patterns are defined in a YAML file and are evaluated in order — first match wins.

### Built-in Patterns

| Unit | Suffix | Matches | Display Example |
|---|---|---|---|
| `timestamp` | `[time]` | `_time_seconds$`, `_timestamp$` | `3d4h ago (1707900000)` |
| `bytes` | `[bytes]` | `_bytes$`, `_bytes_total$` | `22.81 MiB (23921616)` |
| `duration` | `[duration]` | `_seconds$`, `_seconds_total$` | `1.5s (1.5)` |
| `duration_ms` | `[duration]` | `_milliseconds$`, `_ms$` | `150.0ms (150)` |
| `percent` | `[%]` | `_percent$`, `_ratio$` | `85.3% (0.853)` |
| `count` | `[count]` | `_total$` | `1.50k (1500)` |

Timestamp patterns are evaluated before duration patterns so that metrics like `go_memstats_last_gc_time_seconds` display as a relative age rather than a duration.

### Custom Patterns

Override or extend the built-in patterns by providing a YAML file at startup:

```bash
madvisor --patterns ./my-patterns.yaml --targets localhost:9090
```

Example custom patterns file:

```yaml
units:
  - unit: bytes
    suffix: " [bytes]"
    matchers:
      - "_bytes$"
      - "_bytes_total$"
      - "_octets$"

  - unit: custom_rate
    suffix: " [ops]"
    matchers:
      - "_ops$"
      - "_operations$"
```

**Merge behavior:** user-defined units override built-in units of the same name. Units not present in the user file are preserved from the built-in defaults. New unit names are added.

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

### CLI Flags

| Flag | Default | Description |
|---|---|---|
| `--targets` | `localhost:8080` | Comma-separated `host:port` list of Prometheus endpoints to scrape |
| `--rate-window` | `5s` | Rate calculation window duration (e.g. `10s`, `30s`) |
| `--patterns` | *(built-in)* | Path to a custom unit patterns YAML file |
| `--version` | | Print version and exit |

### Environment Variables

| Env Var | Default | Description |
|---|---|---|
| `METRIC_TARGETS` | `localhost:8080` | Comma-separated `host:port` list of Prometheus endpoints to scrape |
| `RATE_WINDOW` | `5s` | Rate calculation window duration |
| `TERM` | `xterm-256color` | Terminal type for color support |

CLI flags take precedence over environment variables.

## How It Works

1. **TTY guard** — on startup, checks if stdin is a terminal. If not, idles with near-zero CPU until a terminal is attached.
2. **Scraper** — polls each target's `/metrics` endpoint every second, parsing the Prometheus exposition format with full label and `# TYPE`/`# HELP` support.
3. **Type detection** — metric types (counter, gauge, histogram, summary) are determined from `# TYPE` annotations in the scrape response. Falls back to gauge when no annotation is present.
4. **Unit matching** — metric names are matched against regex patterns (built-in or custom YAML) to determine display formatting (bytes, duration, timestamp, etc.).
5. **Ring buffer** — stores the last 120 samples per metric series for chart rendering.
6. **TUI** — interactive dashboard built with [termdash](https://github.com/mum4k/termdash): metric names on the right, series detail and chart on the left, with regex filtering and dual-panel keyboard navigation.

## Project Structure

```
cmd/
  madvisor/                  # The madVisor TUI binary
    main.go                  # Core application logic
    patterns.go              # Unit pattern engine (YAML loading, regex matching)
    patterns_default.yaml    # Built-in unit patterns (embedded in binary)
  madvisor-dummy/            # Fake workload producing synthetic labeled metrics
docker/
  Dockerfile.madvisor
  Dockerfile.madvisor-dummy
examples/
  docker-compose/            # Docker Compose example
  k8s/                       # Kubernetes pod manifest + ephemeral inject script
```
