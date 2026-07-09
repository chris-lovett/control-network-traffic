#!/usr/bin/env bash
# check-health.sh
#
# Checks the health and request-chain response of the deployed
# control-network-traffic application.
#
# Usage:
#   ./scripts/check-health.sh [NAMESPACE]
#
# Defaults:
#   NAMESPACE = control-network-traffic

set -euo pipefail

NAMESPACE="${1:-control-network-traffic}"
PORT=18080   # local port for port-forward

echo "==> Checking deployment status in namespace: ${NAMESPACE}"
echo ""

oc get pods -n "${NAMESPACE}" -l app.kubernetes.io/instance \
  -o wide 2>/dev/null || oc get pods -n "${NAMESPACE}"

echo ""
echo "==> Starting port-forward to frontend (localhost:${PORT})..."
oc port-forward svc/frontend "${PORT}":8080 -n "${NAMESPACE}" &
PF_PID=$!
sleep 2   # give the tunnel a moment to open

cleanup() {
  kill "${PF_PID}" 2>/dev/null || true
}
trap cleanup EXIT

echo ""
echo "==> /health endpoints:"
echo "--- frontend ---"
curl -sf "http://localhost:${PORT}/health" | python3 -m json.tool 2>/dev/null || \
  curl -s "http://localhost:${PORT}/health"

echo ""
echo "==> Full request chain (frontend → api → backend):"
curl -sf "http://localhost:${PORT}/" | python3 -m json.tool 2>/dev/null || \
  curl -s "http://localhost:${PORT}/"

echo ""
echo "==> Done."
