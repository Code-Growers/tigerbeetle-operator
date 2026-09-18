#!/usr/bin/env bash
# Based on https://kind.sigs.k8s.io/docs/user/local-registry/
set -euo pipefail

CLUSTER_NAME="${1:-tigerbeetle-operator}"
REGISTRY_NAME="${2:-tigerbeetle-operator-registry}"
REGISTRY_PORT="5001"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Workers to create. Three is the default so `podAntiAffinity: Required` can place three
# replicas. With 1 or 2 workers, set `podAntiAffinity: Preferred` in the TigerBeetleCluster.
KIND_WORKERS="${KIND_WORKERS:-3}"

# Create the registry unless it already exists.
if [[ "$(docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null || true)" != 'true' ]]; then
  echo "Creating local registry '${REGISTRY_NAME}' on port ${REGISTRY_PORT}..."
  docker run \
    -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" \
    --network bridge --name "${REGISTRY_NAME}" \
    registry:2
fi

# Create the kind cluster unless it already exists.
if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
  echo "Creating kind cluster '${CLUSTER_NAME}' with ${KIND_WORKERS} worker(s)..."
  config="$(mktemp)"
  trap 'rm -f "${config}"' EXIT
  cp "${SCRIPT_DIR}/kind-config.yaml" "${config}"
  for _ in $(seq 1 "${KIND_WORKERS}"); do
    printf '  - role: worker\n' >>"${config}"
  done
  kind create cluster --name "${CLUSTER_NAME}" --config "${config}"
fi

# Make localhost:${REGISTRY_PORT} inside the nodes resolve to the registry container.
REGISTRY_DIR="/etc/containerd/certs.d/localhost:${REGISTRY_PORT}"
for node in $(kind get nodes --name "${CLUSTER_NAME}"); do
  docker exec "${node}" mkdir -p "${REGISTRY_DIR}"
  printf '[host."http://%s:5000"]\n' "${REGISTRY_NAME}" | docker exec -i "${node}" cp /dev/stdin "${REGISTRY_DIR}/hosts.toml"
done

# Connect the registry to the cluster network if not already connected.
if [[ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${REGISTRY_NAME}" 2>/dev/null || true)" == 'null' ]]; then
  echo "Connecting registry to kind network..."
  docker network connect "kind" "${REGISTRY_NAME}"
fi

# Document the local registry in the cluster so that Tilt/kubectl can use it.
cat <<EOF | kubectl --context "kind-${CLUSTER_NAME}" apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${REGISTRY_PORT}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF

echo "Kind cluster '${CLUSTER_NAME}' is ready. Registry: localhost:${REGISTRY_PORT}"
