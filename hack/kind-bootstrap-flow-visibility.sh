#!/usr/bin/env bash
# Copyright 2026 Antrea Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Create a kind cluster, load locally built Antrea / Flow Aggregator / Antrea UI
# images, install Antrea with FlowExporter enabled, then install Flow Aggregator
# and Antrea UI using those same images (IfNotPresent + preloaded tags).
#
# Usage:
#   ./hack/kind-bootstrap-flow-visibility.sh [CLUSTER_NAME] [--skip-build] [--recreate]
#
# Defaults:
#   CLUSTER_NAME=kind
#   Builds: make build-ubuntu flow-aggregator-image; (cd antrea-ui && make build)
#
# Examples:
#   ./hack/kind-bootstrap-flow-visibility.sh --recreate
#   ./hack/kind-bootstrap-flow-visibility.sh dev --skip-build --recreate

set -euo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${THIS_DIR}/.." && pwd)"

CLUSTER_NAME="kind"
SKIP_BUILD=false
RECREATE=false
positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-build)
      SKIP_BUILD=true
      shift
      ;;
    --recreate)
      RECREATE=true
      shift
      ;;
    -*)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
    *)
      positional+=("$1")
      shift
      ;;
  esac
done
if [[ ${#positional[@]} -gt 0 ]]; then
  CLUSTER_NAME="${positional[0]}"
fi

CTX="kind-${CLUSTER_NAME}"
IMG_LIST="antrea/antrea-agent-ubuntu:latest antrea/antrea-controller-ubuntu:latest antrea/flow-aggregator:latest antrea/antrea-ui-frontend:latest antrea/antrea-ui-backend:latest"

echo "===> Cluster: ${CLUSTER_NAME} (kubectl context: ${CTX}) <==="

if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  if ${RECREATE}; then
    echo "===> Deleting existing kind cluster ${CLUSTER_NAME} <==="
    kind delete cluster --name "${CLUSTER_NAME}"
  else
    echo "Kind cluster '${CLUSTER_NAME}' already exists. Pass --recreate to delete it first," >&2
    echo "or: kind delete cluster --name ${CLUSTER_NAME}" >&2
    exit 1
  fi
fi

if ! ${SKIP_BUILD}; then
  echo "===> Building Antrea agent/controller images <==="
  (cd "${REPO_ROOT}" && make build-ubuntu)
  echo "===> Building Flow Aggregator image <==="
  (cd "${REPO_ROOT}" && make flow-aggregator-image)
  echo "===> Building Antrea UI images <==="
  (cd "${REPO_ROOT}/antrea-ui" && make build)
else
  echo "===> Skipping image builds (--skip-build) <==="
fi

echo "===> Creating kind cluster and loading images <==="
"${REPO_ROOT}/ci/kind/kind-setup.sh" create "${CLUSTER_NAME}" --images "${IMG_LIST}"

echo "===> Applying Antrea with FlowExporter feature gate and flow exporter enabled <==="
"${REPO_ROOT}/hack/generate-manifest.sh" \
  --feature-gates FlowExporter=true \
  --extra-helm-values flowExporter.enable=true |
  kubectl apply --context "${CTX}" -f -

echo "===> Waiting for Antrea <==="
kubectl rollout status --context "${CTX}" -n kube-system daemonset/antrea-agent --timeout=5m
kubectl rollout status --context "${CTX}" -n kube-system deployment/antrea-controller --timeout=5m

echo "===> Installing Flow Aggregator (image: antrea/flow-aggregator:latest) <==="
helm upgrade --install flow-aggregator "${REPO_ROOT}/build/charts/flow-aggregator" \
  --kube-context "${CTX}" \
  --namespace flow-aggregator \
  --create-namespace \
  --set image.repository=antrea/flow-aggregator \
  --set image.tag=latest \
  --wait --timeout=5m

echo "===> Installing Antrea UI (frontend/backend: latest) <==="
helm upgrade --install antrea-ui "${REPO_ROOT}/antrea-ui/build/charts/antrea-ui" \
  --kube-context "${CTX}" \
  --namespace flow-aggregator \
  --set antreaNamespace=kube-system \
  --set flowAggregator.enabled=true \
  --set flowAggregator.address=flow-aggregator.flow-aggregator.svc:14739 \
  --set frontend.image.repository=antrea/antrea-ui-frontend \
  --set frontend.image.tag=latest \
  --set backend.image.repository=antrea/antrea-ui-backend \
  --set backend.image.tag=latest \
  --wait --timeout=5m

echo "===> Sample: Antrea / Flow Aggregator / UI container images <==="
kubectl --context "${CTX}" -n kube-system get pod -l component=antrea-agent -o jsonpath='{.items[0].spec.containers[0].image}' 2>/dev/null || true
echo ""
kubectl --context "${CTX}" -n flow-aggregator get deploy flow-aggregator -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true
echo ""
kubectl --context "${CTX}" -n flow-aggregator get deploy antrea-ui -o jsonpath='{range .spec.template.spec.containers[*]}{.name}{":"}{.image}{" "}{end}' 2>/dev/null || true
echo ""

echo "===> Done. Port-forward UI: kubectl --context ${CTX} -n flow-aggregator port-forward service/antrea-ui 3000:3000 <==="
