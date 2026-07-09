#!/usr/bin/env bash
# cleanup.sh
#
# Removes the control-network-traffic Helm release and optionally deletes
# the associated Consul config entries and OpenShift namespace.
#
# Usage:
#   ./scripts/cleanup.sh [NAMESPACE] [RELEASE_NAME] [--delete-namespace]
#
# Defaults:
#   NAMESPACE      = control-network-traffic
#   RELEASE_NAME   = cnt

set -euo pipefail

NAMESPACE="${1:-control-network-traffic}"
RELEASE_NAME="${2:-cnt}"
DELETE_NS="${3:-}"

echo "==> Cleaning up release '${RELEASE_NAME}' in namespace '${NAMESPACE}'"

# Uninstall the Helm release
if helm status "${RELEASE_NAME}" --namespace "${NAMESPACE}" &>/dev/null; then
  helm uninstall "${RELEASE_NAME}" --namespace "${NAMESPACE}"
  echo "    Helm release '${RELEASE_NAME}' removed."
else
  echo "    No Helm release '${RELEASE_NAME}' found – skipping."
fi

# Remove Consul config entries (best-effort; consul CLI must be present + logged in)
if command -v consul &>/dev/null; then
  echo ""
  echo "==> Removing Consul config entries (best-effort)..."
  consul config delete -kind service-router   -name backend   2>/dev/null && echo "    Deleted: service-router/backend"   || true
  consul config delete -kind service-splitter -name backend   2>/dev/null && echo "    Deleted: service-splitter/backend"  || true
  consul config delete -kind service-resolver -name backend   2>/dev/null && echo "    Deleted: service-resolver/backend"  || true
  consul config delete -kind service-defaults -name backend   2>/dev/null && echo "    Deleted: service-defaults/backend"  || true
else
  echo "    consul CLI not found – skipping config entry cleanup."
fi

# Optionally delete the namespace
if [[ "${DELETE_NS}" == "--delete-namespace" ]]; then
  echo ""
  echo "==> Deleting namespace '${NAMESPACE}'..."
  oc delete project "${NAMESPACE}" --ignore-not-found
  echo "    Namespace deleted."
fi

echo ""
echo "==> Cleanup complete."
