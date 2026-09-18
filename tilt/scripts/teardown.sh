#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${1:-tigerbeetle-operator}"
REGISTRY_NAME="${2:-tigerbeetle-operator-registry}"

echo "Deleting kind cluster '${CLUSTER_NAME}'..."
kind delete cluster --name "${CLUSTER_NAME}" || true

if [[ "$(docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null || true)" == 'true' ]]; then
  echo "Stopping registry '${REGISTRY_NAME}'..."
  docker stop "${REGISTRY_NAME}"
  docker rm "${REGISTRY_NAME}"
fi
