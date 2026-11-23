#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

repo="${DRAGONFLY_CHART_REPO:-https://gorizond.github.io/dragonflyoss-helm-charts}"
version="${DRAGONFLY_CHART_VERSION:-1.10.1-patch.2}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# Pull the fork chart with its packaged subcharts and verify the pinned version.
helm pull dragonfly --repo "$repo" --version "$version" -d "$tmp" >/dev/null
chart=$(find "$tmp" -maxdepth 1 -name 'dragonfly-*.tgz' -print -quit)
[ -n "$chart" ] || fail "fork chart $version was not pulled from $repo"
helm show chart "$chart" | grep -q "^version: $version$" || fail "pulled chart is not version $version"

cat >"$tmp/manager-values.yaml" <<'YAML'
fullnameOverride: c1
manager:
  enable: true
  config:
    server:
      grpc:
        tls:
          caCert: /etc/dragonfly/certs/ca.crt
          cert: /etc/dragonfly/certs/tls.crt
          key: /etc/dragonfly/certs/tls.key
      rest:
        tls:
          cert: /etc/dragonfly/certs/tls.crt
          key: /etc/dragonfly/certs/tls.key
mysql:
  enable: true
  primary:
    persistence:
      enabled: false
redis:
  enable: false
externalRedis:
  addrs:
    - c1-keydb.dragonfly-system.svc.cluster.local:6379
scheduler:
  enable: false
seedClient:
  enable: false
client:
  enable: false
startupProbe:
  enable: true
YAML

helm template c1-manager "$chart" --namespace dragonfly-system -f "$tmp/manager-values.yaml" >"$tmp/manager.yaml"

# manager.yaml data must be: server.grpc.tls.{caCert,cert,key}; no server.tls.
if ! awk '
  /manager.yaml: \|-/ { inblock = 1; next }
  inblock && /^    auth:/ { inblock = 0 }
  inblock {
    if ($0 ~ /^      grpc:/) grpc = 1
    if (grpc && $0 ~ /^        tls:/) nested = 1
    if (grpc && $0 ~ /^      workHome:/) grpc = 0
    if ($0 ~ /^      tls:/) wrong = 1
  }
  END { exit (nested && !wrong) ? 0 : 1 }
' "$tmp/manager.yaml"; then
  fail "manager manager.yaml does not nest tls under server.grpc"
fi
for flag in "-tls-ca-cert=/etc/dragonfly/certs/ca.crt" "-tls-client-cert=/etc/dragonfly/certs/tls.crt" "-tls-client-key=/etc/dragonfly/certs/tls.key"; do
  count=$(grep -c "command: \[\"/bin/grpc_health_probe\", \"-addr=:65003\".*$flag" "$tmp/manager.yaml" || true)
  [ "$count" -eq 3 ] || fail "manager probes with $flag = $count, want 3"
done

cat >"$tmp/control-values.yaml" <<'YAML'
fullnameOverride: c1-control
manager:
  enable: false
mysql:
  enable: false
redis:
  enable: false
externalRedis:
  addrs:
    - c1-keydb.dragonfly-system.svc.cluster.local:6379
externalManager:
  host: manager-grpc.example.com
  grpcPort: 443
scheduler:
  enable: true
  config:
    manager:
      schedulerClusterID: 42
      tls:
        caCert: /etc/dragonfly/certs/ca.crt
        cert: /etc/dragonfly/certs/tls.crt
        key: /etc/dragonfly/certs/tls.key
    server:
      advertiseIP: 203.0.113.10
      tls:
        caCert: /etc/dragonfly/certs/ca.crt
        cert: /etc/dragonfly/certs/tls.crt
        key: /etc/dragonfly/certs/tls.key
    seedPeer:
      tls:
        caCert: /etc/dragonfly/certs/ca.crt
        cert: /etc/dragonfly/certs/tls.crt
        key: /etc/dragonfly/certs/tls.key
seedClient:
  enable: false
client:
  enable: false
startupProbe:
  enable: true
YAML

helm template c1-control "$chart" --namespace dragonfly-system -f "$tmp/control-values.yaml" >"$tmp/control.yaml"

for flag in "-tls-ca-cert=/etc/dragonfly/certs/ca.crt" "-tls-client-cert=/etc/dragonfly/certs/tls.crt" "-tls-client-key=/etc/dragonfly/certs/tls.key"; do
  count=$(grep -c "command: \[\"/bin/grpc_health_probe\", \"-addr=:8002\".*$flag" "$tmp/control.yaml" || true)
  [ "$count" -eq 3 ] || fail "scheduler probes with $flag = $count, want 3"
done

# Manager remains external-only: no in-cluster manager DNS fallback.
grep -q 'addr: manager-grpc.example.com:443' "$tmp/control.yaml" || fail "scheduler manager address is not the public external host"
if grep -q 'demo-manager.dragonfly-system.svc' "$tmp/control.yaml" || grep -q 'manager.dragonfly-system.svc' "$tmp/control.yaml"; then
  fail "scheduler render contains an internal manager DNS address"
fi

# The scheduler must keep its headless peer Service and add a non-headless
# client Service on the same gRPC port. The client ClusterIP is pinned because
# it is a P2P certificate IP SAN.
peer_svc=$(awk 'BEGIN{RS="---"} /kind: Service/ && /name: c1-control-scheduler[^-]/ {print}' "$tmp/control.yaml")
client_svc=$(awk 'BEGIN{RS="---"} /kind: Service/ && /name: c1-control-scheduler-client/ {print}' "$tmp/control.yaml")
[ -n "$peer_svc" ] || fail "headless scheduler peer Service missing"
[ -z "$client_svc" ] || fail "fork chart must not render a scheduler client Service"
echo "$peer_svc" | grep -q 'clusterIP: None' || fail "scheduler peer Service must stay headless"
echo "$peer_svc" | grep -q 'port: 8002' || fail "scheduler peer Service port missing"
echo "$peer_svc" | grep -q 'targetPort: 8002' || fail "scheduler peer Service targetPort missing"
grep -q 'advertiseIP: 203.0.113.10' "$tmp/control.yaml" || fail "scheduler advertiseIP missing"

echo "fork chart TLS render checks passed ($repo $version)"

# Data workloads must render the pilot-sized requests/limits pinned by the
# operator (the chart defaults request 8 CPU / 16Gi for seedClient).
cat >"$tmp/data-values.yaml" <<'YAML'
fullnameOverride: c1-data
manager:
  enable: false
mysql:
  enable: false
redis:
  enable: false
externalRedis:
  addrs:
    - c1-keydb.dragonfly-system.svc.cluster.local:6379
scheduler:
  enable: false
seedClient:
  enable: true
  hostNetwork: true
  replicas: 3
  persistence:
    enable: false
  resources:
    requests:
      cpu: 150m
      memory: 256Mi
    limits:
      cpu: "4"
      memory: 8Gi
client:
  enable: true
  hostNetwork: true
  dfinit:
    enable: false
  priorityClassName: system-node-critical
  resources:
    requests:
      cpu: 250m
      memory: 512Mi
    limits:
      cpu: "2"
      memory: 4Gi
YAML

helm template c1-data "$chart" --namespace dragonfly-system -f "$tmp/data-values.yaml" >"$tmp/data.yaml"
client_doc=$(awk 'BEGIN{RS="---"} /kind: DaemonSet/ && /name: c1-data-client/ {print}' "$tmp/data.yaml")
seed_doc=$(awk 'BEGIN{RS="---"} /kind: StatefulSet/ && /name: c1-data-seed-client/ {print}' "$tmp/data.yaml")

assert_workload_resources() {
  local doc="$1" requests_cpu="$2" requests_memory="$3" limits_cpu="$4" limits_memory="$5" label="$6"
  [ -n "$doc" ] || fail "$label was not rendered"
  echo "$doc" | grep -A2 'requests:' | grep -q "cpu: $requests_cpu" || fail "$label requests.cpu missing"
  echo "$doc" | grep -A2 'requests:' | grep -q "memory: $requests_memory" || fail "$label requests.memory missing"
  echo "$doc" | grep -A2 'limits:' | grep -q "cpu: $limits_cpu" || fail "$label limits.cpu missing"
  echo "$doc" | grep -A2 'limits:' | grep -q "memory: $limits_memory" || fail "$label limits.memory missing"
}

assert_workload_resources "$seed_doc" '150m' '256Mi' '"4"' '8Gi' "seedClient"
assert_workload_resources "$client_doc" '250m' '512Mi' '"2"' '4Gi' "client"

echo "fork chart data resource render checks passed ($repo $version)"

# Extras: manager gRPC is a Traefik exact-SNI TCP passthrough, REST stays LE.
extras="$root/charts/dragonfly-extras"
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.grpc.port=65003 \
  --set ingress.rest.enabled=true \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=demo-manager-public-tls \
  --api-versions traefik.io/v1alpha1/IngressRoute \
  --api-versions traefik.io/v1alpha1/IngressRouteTCP \
  --api-versions traefik.io/v1alpha1/ServersTransport >"$tmp/extras.yaml"
manager_tcp=$(awk 'BEGIN{RS="---"} /kind: IngressRouteTCP/ && /name: e-manager-grpc/ {print}' "$tmp/extras.yaml")
[ -n "$manager_tcp" ] || fail "manager gRPC TCP route missing"
echo "$manager_tcp" | grep -Fq 'HostSNI(`grpc.example.com`)' || fail "manager gRPC exact HostSNI missing"
echo "$manager_tcp" | grep -q 'name: "c1-manager"' || fail "manager gRPC backend missing"
echo "$manager_tcp" | grep -q 'port: 65003' || fail "manager gRPC backend port must be 65003"
echo "$manager_tcp" | grep -q 'passthrough: true' || fail "manager gRPC passthrough missing"
if grep -q 'kind: TLSOption' "$tmp/extras.yaml"; then fail "manager gRPC must not render a TLSOption"; fi
if awk 'BEGIN{RS="---"} /kind: ServersTransport/ && /name: e-manager-grpc/ {found=1} END{exit found?0:1}' "$tmp/extras.yaml"; then
  fail "manager gRPC must not render a ServersTransport"
fi
if grep -Fq 'Host(`grpc.example.com`)' "$tmp/extras.yaml"; then fail "HTTP router competes with manager gRPC TCP SNI"; fi
if grep -q 'dragonfly-system.svc' "$tmp/extras.yaml"; then fail "extras render contains an internal manager DNS address"; fi
if grep -q 'name: e-scheduler' "$tmp/extras.yaml"; then fail "extras chart still renders a scheduler TCP route"; fi
grep -q 'serversTransport: "e-manager-rest"' "$tmp/extras.yaml" || fail "REST ServersTransport missing"
grep -q 'secretName: "demo-manager-public-tls"' "$tmp/extras.yaml" || fail "REST public TLS Secret missing"

echo "extras manager-gRPC TCP render checks passed"
