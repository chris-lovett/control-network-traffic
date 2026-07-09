#!/usr/bin/env bash
# deploy-baseline.sh
#
# Deploys (or upgrades) the control-network-traffic baseline application
# into a single OpenShift namespace.
#
# Usage:
#   ./scripts/deploy-baseline.sh [NAMESPACE] [RELEASE_NAME]
#
# Defaults:
#   NAMESPACE    = control-network-traffic
#   RELEASE_NAME = cnt

set -euo pipefail

NAMESPACE="${1:-control-network-traffic}"
RELEASE_NAME="${2:-cnt}"
CHART_DIR="$(cd "$(dirname "$0")/../charts/control-network-traffic" && pwd)"

echo "==> Deploying baseline to namespace: ${NAMESPACE}"
echo "    Release:  ${RELEASE_NAME}"
echo "    Chart:    ${CHART_DIR}"
echo ""

# Create namespace / project if it doesn't exist
oc get project "${NAMESPACE}" &>/dev/null || oc new-project "${NAMESPACE}"

# Install or upgrade the Helm release
helm upgrade --install "${RELEASE_NAME}" "${CHART_DIR}" \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  --atomic \
  --timeout 5m

echo ""
echo "==> Waiting for rollouts to complete..."
oc rollout status deployment/frontend -n "${NAMESPACE}"
oc rollout status deployment/api      -n "${NAMESPACE}"
oc rollout status deployment/backend  -n "${NAMESPACE}"

echo ""
echo "==> Baseline deployed successfully!"
echo ""
echo "    To test the request chain, run:"
echo "      ./scripts/check-health.sh ${NAMESPACE}"
