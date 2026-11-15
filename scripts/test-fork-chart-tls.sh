#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

repo="${DRAGONFLY_CHART_REPO:-https://gorizond.github.io/dragonflyoss-helm-charts}"
version="${DRAGONFLY_CHART_VERSION:-1.10.1-patch.1}"

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

echo "fork chart TLS render checks passed ($repo $version)"
