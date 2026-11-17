#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/charts/registry-proxy"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

helm lint "$chart" >/dev/null || fail "registry-proxy chart lint failed"
helm template registry-proxy "$chart" --namespace dragonfly-system \
  --set ingress.enabled=true --set ingress.host=proxy.example.com >"$tmp/proxy.yaml"

grep -q 'kind: Deployment' "$tmp/proxy.yaml" || fail "proxy Deployment is missing"
grep -q 'kind: Service' "$tmp/proxy.yaml" || fail "proxy Service is missing"
grep -q 'kind: NetworkPolicy' "$tmp/proxy.yaml" || fail "proxy NetworkPolicy is missing"
grep -q 'kind: Ingress' "$tmp/proxy.yaml" || fail "proxy Ingress is missing"
grep -q 'host: "proxy.example.com"' "$tmp/proxy.yaml" || fail "proxy Ingress host is missing"
grep -q 'command:' "$tmp/proxy.yaml" || fail "proxy command is missing"
grep -q '/registry-proxy' "$tmp/proxy.yaml" || fail "proxy command does not use /registry-proxy"
grep -q 'containerPort: 8080' "$tmp/proxy.yaml" || fail "proxy service port is missing"
grep -q 'runAsUser: 65532' "$tmp/proxy.yaml" || fail "proxy is not UID 65532"
grep -q 'readOnlyRootFilesystem: true' "$tmp/proxy.yaml" || fail "proxy rootfs is not read-only"
grep -q 'secretName: harbor-admin' "$tmp/proxy.yaml" || fail "proxy admin Secret mount is missing"
grep -q 'secretName: registry-proxy-webhook' "$tmp/proxy.yaml" || fail "proxy webhook Secret mount is missing"
grep -q 'name: harbor-robot' "$tmp/proxy.yaml" || fail "proxy robot Secret mount is missing"
grep -q 'kind: Role' "$tmp/proxy.yaml" || fail "proxy robot Secret Role is missing"
grep -q 'kind: RoleBinding' "$tmp/proxy.yaml" || fail "proxy robot Secret RoleBinding is missing"
grep -q 'serviceAccountName:' "$tmp/proxy.yaml" || fail "proxy ServiceAccount is missing"
grep -q 'name: POD_NAMESPACE' "$tmp/proxy.yaml" || fail "proxy POD_NAMESPACE env is missing"
grep -q 'optional: true' "$tmp/proxy.yaml" || fail "proxy robot/webhook Secrets must be optional"
grep -q '/usr/local/bin/nydus-image' "$tmp/proxy.yaml" || fail "conversion nydus image path is missing"

if grep -q 'kind: PersistentVolumeClaim' "$tmp/proxy.yaml"; then
  fail "registry-proxy must not render a PVC"
fi
if grep -q 'kind: DaemonSet' "$tmp/proxy.yaml"; then
  fail "registry-proxy must not render a DaemonSet"
fi

helm template c1-containerd-mirror "$root/charts/containerd-mirror" \
  --set proxy.enabled=true --set proxy.url=https://proxy.example.com >"$tmp/mirror.yaml"
grep -q 'X-Dragonfly-Registry = \["https://proxy.example.com/docker.io"\]' "$tmp/mirror.yaml" || \
  fail "containerd-mirror does not point at the external proxy"
if grep -q 'kind: Service' "$tmp/mirror.yaml" || grep -q 'kind: Deployment' "$tmp/mirror.yaml"; then
  fail "containerd-mirror must not render a proxy Service/Deployment"
fi

echo "registry-proxy render checks passed"
