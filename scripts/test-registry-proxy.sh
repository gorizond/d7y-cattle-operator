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
  --set ingress.enabled=true --set ingress.host=proxy.example.com \
  --set ingress.path=/proxy --set ingress.tlsSecret=poligon-harbor-tls \
  --set proxy.basePath=/proxy \
  --set webhook.address=http://c1-registry-proxy.dragonfly-system.svc:8080/api/convert \
  --set proxy.hostAliases[0].ip=10.43.0.10 \
  --set proxy.hostAliases[0].hostnames[0]=harbor.koldun.negash.ru >"$tmp/proxy.yaml"

helm template registry-proxy "$chart" --namespace dragonfly-system \
  --show-only templates/networkpolicy.yaml >"$tmp/netpol.yaml"

helm template registry-proxy "$chart" --namespace dragonfly-system \
  --show-only templates/ciliumnetworkpolicy.yaml >"$tmp/cnp.yaml"

grep -q 'kind: Deployment' "$tmp/proxy.yaml" || fail "proxy Deployment is missing"
grep -q 'kind: Service' "$tmp/proxy.yaml" || fail "proxy Service is missing"
grep -q 'kind: NetworkPolicy' "$tmp/proxy.yaml" || fail "proxy NetworkPolicy is missing"
grep -q 'kind: CiliumNetworkPolicy' "$tmp/cnp.yaml" || fail "proxy CiliumNetworkPolicy is missing"
grep -q 'name: registry-proxy-apiserver' "$tmp/cnp.yaml" || fail "CiliumNetworkPolicy name is missing"
grep -q 'toEntities:' "$tmp/cnp.yaml" || fail "CiliumNetworkPolicy toEntities is missing"
grep -q -- '- kube-apiserver' "$tmp/cnp.yaml" || fail "CiliumNetworkPolicy kube-apiserver entity is missing"
grep -q 'port: "6443"' "$tmp/cnp.yaml" || fail "CiliumNetworkPolicy kube-apiserver port 6443 is missing"
grep -q 'app.kubernetes.io/name: registry-proxy' "$tmp/cnp.yaml" || fail "CiliumNetworkPolicy endpoint selector is missing"
grep -q 'kubernetes.io/metadata.name: kube-system' "$tmp/netpol.yaml" || fail "NetworkPolicy DNS/Traefik peer is missing"
grep -A3 'kubernetes.io/metadata.name: kube-system' "$tmp/netpol.yaml" | grep -q 'port: 443' || \
  fail "NetworkPolicy kube-system egress does not allow TCP 443"
# Cilium matches post-DNAT backend targetPort, not the Service port.
grep -A5 'kubernetes.io/metadata.name: kube-system' "$tmp/netpol.yaml" | grep -q 'port: 8443' || \
  fail "NetworkPolicy kube-system egress does not allow Traefik websecure TCP 8443"
grep -A9 'kubernetes.io/metadata.name: harbor' "$tmp/netpol.yaml" | grep -q 'port: 80' || \
  fail "NetworkPolicy harbor egress does not allow TCP 80"
grep -A9 'kubernetes.io/metadata.name: harbor' "$tmp/netpol.yaml" | grep -q 'port: 443' || \
  fail "NetworkPolicy harbor egress does not allow TCP 443"
grep -A9 'kubernetes.io/metadata.name: harbor' "$tmp/netpol.yaml" | grep -q 'port: 8080' || \
  fail "NetworkPolicy harbor egress does not allow Harbor core targetPort TCP 8080"
grep -A9 'kubernetes.io/metadata.name: harbor' "$tmp/netpol.yaml" | grep -q 'port: 5000' || \
  fail "NetworkPolicy harbor egress does not allow Harbor registry targetPort TCP 5000"
grep -A3 'cidr: 0.0.0.0/0' "$tmp/netpol.yaml" | grep -q 'port: 443' || \
  fail "NetworkPolicy external Harbor egress does not allow TCP 443"
grep -q 'kind: Ingress' "$tmp/proxy.yaml" || fail "proxy Ingress is missing"
grep -q 'host: "proxy.example.com"' "$tmp/proxy.yaml" || fail "proxy Ingress host is missing"
grep -q 'path: /proxy' "$tmp/proxy.yaml" || fail "proxy Ingress path is missing"
grep -q 'secretName: poligon-harbor-tls' "$tmp/proxy.yaml" || fail "proxy Ingress TLS Secret is missing"
grep -q 'base_path: "/proxy"' "$tmp/proxy.yaml" || fail "proxy config base_path is missing"
grep -q 'address: "http://c1-registry-proxy.dragonfly-system.svc:8080/api/convert"' "$tmp/proxy.yaml" || \
  fail "proxy config webhook address is missing"
grep -q 'ip: "10.43.0.10"' "$tmp/proxy.yaml" || fail "proxy hostAliases IP is missing"
grep -q -- '- "harbor.koldun.negash.ru"' "$tmp/proxy.yaml" || fail "proxy hostAliases hostname is missing"
grep -q 'command:' "$tmp/proxy.yaml" || fail "proxy command is missing"
grep -q '/registry-proxy' "$tmp/proxy.yaml" || fail "proxy command does not use /registry-proxy"
grep -q 'containerPort: 8080' "$tmp/proxy.yaml" || fail "proxy service port is missing"
grep -q 'runAsUser: 65532' "$tmp/proxy.yaml" || fail "proxy is not UID 65532"
grep -q 'readOnlyRootFilesystem: true' "$tmp/proxy.yaml" || fail "proxy rootfs is not read-only"
grep -q 'startupProbe:' "$tmp/proxy.yaml" || fail "proxy startupProbe is missing"
grep -q 'failureThreshold: 30' "$tmp/proxy.yaml" || fail "proxy startup failureThreshold is missing"
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
