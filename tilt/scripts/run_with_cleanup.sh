#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${1:-tigerbeetle-operator}"
REGISTRY_NAME="${2:-tigerbeetle-operator-registry}"

function cleanup {
  echo "Tearing down kind cluster '${CLUSTER_NAME}'..."
  bash "$(dirname "${BASH_SOURCE[0]}")/teardown.sh" "${CLUSTER_NAME}" "${REGISTRY_NAME}"
}

trap cleanup EXIT

bash "$(dirname "${BASH_SOURCE[0]}")/kind.sh" "${CLUSTER_NAME}" "${REGISTRY_NAME}"
tilt up --context "kind-${CLUSTER_NAME}"
