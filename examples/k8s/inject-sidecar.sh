#!/usr/bin/env bash
set -euo pipefail

USAGE="Usage: $0 <pod-name> [metric-port(s)] [namespace]

Inject madVisor as an ephemeral debug container into a running pod.
Uses 'kubectl debug' with ephemeral containers (requires Kubernetes 1.25+).

Arguments:
  pod-name        Name of the target pod
  metric-port(s)  Comma-separated metric ports (default: 8080)
  namespace       Kubernetes namespace (default: current context namespace)

Environment:
  MADVISOR_IMAGE  Override the madVisor image (default: dcroche/madvisor:latest)

Examples:
  $0 my-app
  $0 my-app 9090
  $0 my-app 8080,9090 production
"

POD_NAME="${1:?$USAGE}"
METRIC_PORTS="${2:-8080}"
NAMESPACE="${3:-}"

IMAGE="${MADVISOR_IMAGE:-dcroche/madvisor:latest}"

# Build comma-separated localhost:port targets
TARGETS=""
IFS=',' read -ra PORTS <<< "$METRIC_PORTS"
for port in "${PORTS[@]}"; do
  port=$(echo "$port" | tr -d ' ')
  if [ -n "$TARGETS" ]; then
    TARGETS="${TARGETS},localhost:${port}"
  else
    TARGETS="localhost:${port}"
  fi
done

NS_ARGS=()
if [ -n "$NAMESPACE" ]; then
  NS_ARGS=(--namespace "$NAMESPACE")
fi

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " madVisor — ephemeral inject"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Pod:     $POD_NAME"
echo "  Image:   $IMAGE"
echo "  Targets: $TARGETS"
[ -n "$NAMESPACE" ] && echo "  NS:      $NAMESPACE"
echo ""
echo "Attaching... (press Q or ESC to exit)"
echo ""

exec kubectl debug "${NS_ARGS[@]}" -it "$POD_NAME" \
  --image="$IMAGE" \
  --profile=general \
  --env="METRIC_TARGETS=$TARGETS" \
  --env="TERM=xterm-256color" \
  -- /madvisor
