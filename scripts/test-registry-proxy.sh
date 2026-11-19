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
  --set ingress.path=/proxy --set ingress.tlsSecret=demo-harbor-tls \
  --set proxy.basePath=/proxy \
  --set webhook.address=http://c1-registry-proxy.dragonfly-system.svc:8080/api/convert \
  --set proxy.hostAliases[0].ip=203.0.113.10 \
  --set proxy.hostAliases[0].hostnames[0]=harbor.example.test >"$tmp/proxy.yaml"

helm template registry-proxy "$chart" --namespace dragonfly-system \
  --set fullnameOverride=demo --show-only templates/deployment.yaml >"$tmp/fullname.yaml"
grep -q 'name: demo-registry-proxy' "$tmp/fullname.yaml" || \
  fail "fullnameOverride Deployment name is missing"
grep -q 'app.kubernetes.io/instance: demo-registry-proxy' "$tmp/fullname.yaml" || \
  fail "fullnameOverride selector label is missing"

helm template registry-proxy "$chart" --namespace dragonfly-system \
  --show-only templates/networkpolicy.yaml >"$tmp/netpol.yaml"

grep -q 'kind: Deployment' "$tmp/proxy.yaml" || fail "proxy Deployment is missing"
grep -q 'kind: Service' "$tmp/proxy.yaml" || fail "proxy Service is missing"
grep -q 'kind: NetworkPolicy' "$tmp/proxy.yaml" || fail "proxy NetworkPolicy is missing"
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
grep -q 'secretName: demo-harbor-tls' "$tmp/proxy.yaml" || fail "proxy Ingress TLS Secret is missing"
grep -q 'base_path: "/proxy"' "$tmp/proxy.yaml" || fail "proxy config base_path is missing"
helm template registry-proxy "$chart" --namespace dragonfly-system \
  --set ingress.enabled=true --set ingress.host=proxy.example.com \
  --show-only templates/configmap.yaml | grep -q 'base_path: ""' || \
  fail "proxy default empty base_path is missing"
helm template registry-proxy "$chart" --namespace dragonfly-system \
  --set ingress.enabled=true --set ingress.host=proxy.example.com \
  --show-only templates/ingress.yaml | grep -q 'path: /$' || \
  fail "proxy default root Ingress path is missing"
if grep -q 'kind: Certificate' "$tmp/proxy.yaml"; then
  fail "proxy Certificate is rendered by default"
fi
helm template registry-proxy "$chart" --namespace dragonfly-system \
  --set ingress.enabled=true --set ingress.host=proxy.example.com \
  --set ingress.tlsSecret=demo-proxy-tls --set certificate.enabled=true \
  --show-only templates/certificate.yaml >"$tmp/certificate.yaml"
grep -q 'kind: Certificate' "$tmp/certificate.yaml" || fail "proxy Certificate is missing"
grep -q 'secretName: "demo-proxy-tls"' "$tmp/certificate.yaml" || fail "Certificate secretName is missing"
grep -q -- '- "proxy.example.com"' "$tmp/certificate.yaml" || fail "Certificate dnsNames is missing"
grep -q 'name: "letsencrypt-prod"' "$tmp/certificate.yaml" || fail "Certificate issuer name is missing"
grep -q 'kind: "ClusterIssuer"' "$tmp/certificate.yaml" || fail "Certificate issuer kind is missing"
grep -q 'address: "http://c1-registry-proxy.dragonfly-system.svc:8080/api/convert"' "$tmp/proxy.yaml" || \
  fail "proxy config webhook address is missing"
grep -q 'ip: "203.0.113.10"' "$tmp/proxy.yaml" || fail "proxy hostAliases IP is missing"
grep -q -- '- "harbor.example.test"' "$tmp/proxy.yaml" || fail "proxy hostAliases hostname is missing"
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
if grep -q 'name: POD_NAMESPACE' "$tmp/proxy.yaml"; then
  fail "proxy must not need Kubernetes API access for the robot Secret"
fi
grep -q 'optional: true' "$tmp/proxy.yaml" || fail "proxy robot/webhook Secrets must be optional"
grep -q '/usr/local/bin/nydus-image' "$tmp/proxy.yaml" || fail "conversion nydus image path is missing"
grep -q -- '- "*"' "$tmp/proxy.yaml" || fail "proxy allow-any allowlist is missing"

# Robot auth data is provisioned by the operator and copied by Fleet; the
# proxy only reads the mounted Secret and keeps get as its sole API verb.
helm template c1-registry-proxy "$chart" --namespace dragonfly-system \
  --set harbor.robotSecret=c1-registry-proxy-robot \
  --set webhook.secretName=c1-registry-proxy-webhook >"$tmp/named.yaml"
grep -q 'secretName: c1-registry-proxy-robot' "$tmp/named.yaml" || \
  fail "proxy robot Secret mount does not use the configured name"
grep -q 'name: c1-registry-proxy-robot-secret' "$tmp/named.yaml" || \
  fail "proxy robot Secret Role name is missing"
grep -q 'resourceNames: \["c1-registry-proxy-robot"\]' "$tmp/named.yaml" || \
  fail "RBAC resourceNames does not scope the robot Secret"
grep -q 'verbs: \["get"\]' "$tmp/named.yaml" || \
  fail "RBAC does not grant get on the robot Secret"
if grep -Eq 'verbs: .*"(create|update|patch|delete)' "$tmp/named.yaml"; then
  fail "RBAC must not grant create/update/patch/delete on secrets"
fi
if grep -q 'resourceNames: .*"\*"' "$tmp/named.yaml"; then
  fail "RBAC must not use a wildcard resourceNames entry"
fi
if grep -q 'robot_secret:' "$tmp/named.yaml"; then
  fail "proxy config must not carry the robot Secret name"
fi

# Optional client Basic auth: render config plus the mounted Secret only when
# enabled; the default remains network-only with no extra volume.
auth_ref="proxy-auth-$(date +%s%N)"
helm template registry-proxy "$chart" --namespace dragonfly-system \
  --set auth.mode=basic --set auth.username=d7y-proxy \
  --set "auth.passwordSecret=$auth_ref" \
  --show-only templates/configmap.yaml >"$tmp/auth-config.yaml"
grep -q 'mode: "basic"' "$tmp/auth-config.yaml" || fail "basic auth mode is missing"
grep -q 'username: "d7y-proxy"' "$tmp/auth-config.yaml" || fail "basic auth username is missing"
grep -q 'password_file: /etc/registry-proxy/secrets/auth/password' "$tmp/auth-config.yaml" || \
  fail "basic auth password_file is missing"

helm template registry-proxy "$chart" --namespace dragonfly-system \
  --set auth.mode=basic --set auth.username=d7y-proxy \
  --set "auth.passwordSecret=$auth_ref" \
  --show-only templates/deployment.yaml >"$tmp/auth-deployment.yaml"
grep -q 'name: basic-auth' "$tmp/auth-deployment.yaml" || fail "basic auth volume is missing"
grep -q "secretName: \"$auth_ref\"" "$tmp/auth-deployment.yaml" || fail "basic auth Secret mount is missing"
grep -q 'mountPath: /etc/registry-proxy/secrets/auth' "$tmp/auth-deployment.yaml" || \
  fail "basic auth mount path is missing"
grep -q 'key: password' "$tmp/auth-deployment.yaml" || fail "basic auth Secret key is missing"
grep -q 'mode: "network-only"' "$tmp/proxy.yaml" || fail "default auth mode is not network-only"
if grep -q 'password_file:' "$tmp/proxy.yaml"; then fail "default render carries a password_file"; fi
if grep -q 'name: basic-auth' "$tmp/proxy.yaml"; then fail "default render mounts basic auth"; fi
if helm template registry-proxy "$chart" --namespace dragonfly-system \
  --set auth.mode=basic --set auth.username=d7y-proxy >/dev/null 2>&1; then
  fail "basic auth without auth.passwordSecret must fail"
fi

if grep -q 'kind: PersistentVolumeClaim' "$tmp/proxy.yaml"; then
  fail "registry-proxy must not render a PVC"
fi
if grep -q 'kind: DaemonSet' "$tmp/proxy.yaml"; then
  fail "registry-proxy must not render a DaemonSet"
fi

helm template c1-containerd-mirror "$root/charts/containerd-mirror" >"$tmp/mirror.yaml"
grep -q '^  _default: |' "$tmp/mirror.yaml" || fail "containerd-mirror _default hosts file is missing"
grep -q '^  docker.io: |' "$tmp/mirror.yaml" || fail "containerd-mirror docker.io hosts file is missing"
grep -q '^  index.docker.io: |' "$tmp/mirror.yaml" || fail "containerd-mirror index.docker.io hosts file is missing"

_default_block="$(grep -A2 '^  _default: |' "$tmp/mirror.yaml")"
echo "$_default_block" | grep -q 'server = "http://127.0.0.1:4001"' ||   fail "containerd-mirror _default does not point at dfdaemon"

for key in docker.io index.docker.io; do
  block="$(grep -A7 "^  ${key}: |" "$tmp/mirror.yaml")"
  echo "$block" | grep -q 'server = "https://registry-1.docker.io"' ||     fail "containerd-mirror ${key} server is not registry-1.docker.io"
  echo "$block" | grep -q '\[host."http://127.0.0.1:4001"\]' ||     fail "containerd-mirror ${key} dfdaemon host is missing"
  echo "$block" | grep -q 'X-Dragonfly-Registry = \["https://registry-1.docker.io"\]' ||     fail "containerd-mirror ${key} header is not registry-1.docker.io"
  echo "$block" | grep -q '\[host."https://registry-1.docker.io"\]' ||     fail "containerd-mirror ${key} registry-1 fallback is missing"
done

if [ "$(grep -cE '^  [A-Za-z0-9._-]+: \|$' "$tmp/mirror.yaml")" -ne 3 ]; then
  fail "containerd-mirror must render exactly _default/docker.io/index.docker.io"
fi
if grep -qE '^  (registry\.gitlab\.com|ghcr\.io|quay\.io):' "$tmp/mirror.yaml"; then
  fail "containerd-mirror must not write other per-registry files"
fi
grep -q 'mkdir -p /host-certs/_default /host-certs/docker.io /host-certs/index.docker.io' "$tmp/mirror.yaml" ||   fail "containerd-mirror DaemonSet dirs are incomplete"
grep -q 'cp /config/docker.io /host-certs/docker.io/hosts.toml' "$tmp/mirror.yaml" ||   fail "containerd-mirror DaemonSet does not copy docker.io"
grep -q 'cp /config/index.docker.io /host-certs/index.docker.io/hosts.toml' "$tmp/mirror.yaml" ||   fail "containerd-mirror DaemonSet does not copy index.docker.io"
if grep -q 'rm -rf /host-certs/docker.io' "$tmp/mirror.yaml"; then
  fail "containerd-mirror must not delete the Docker Hub aliases"
fi
if grep -q 'kind: Service' "$tmp/mirror.yaml" || grep -q 'kind: Deployment' "$tmp/mirror.yaml"; then
  fail "containerd-mirror must not render a proxy Service/Deployment"
fi

echo "registry-proxy render checks passed"
