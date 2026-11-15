#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/charts/dragonfly"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

common=(
  --set manager.enable=true
  --set scheduler.enable=true
  --set seedClient.enable=false
  --set client.enable=false
  --set injector.enable=false
  --set redis.enable=false
  --set mysql.enable=false
  --set postgres.enable=false
  --set startupProbe.enable=true
)
tls=(
  --set manager.config.server.grpc.tls.caCert=/etc/dragonfly/certs/ca.crt
  --set manager.config.server.grpc.tls.cert=/etc/dragonfly/certs/tls.crt
  --set manager.config.server.grpc.tls.key=/etc/dragonfly/certs/tls.key
  --set scheduler.config.server.tls.caCert=/etc/dragonfly/certs/ca.crt
  --set scheduler.config.server.tls.cert=/etc/dragonfly/certs/tls.crt
  --set scheduler.config.server.tls.key=/etc/dragonfly/certs/tls.key
)

helm template t "$chart" "${common[@]}" >"$tmp/plain.yaml"
helm template t "$chart" "${common[@]}" "${tls[@]}" >"$tmp/tls.yaml"

# Patch 1: grpc.tls must be nested under server.grpc in the manager config.
if ! awk '
  /^      grpc:$/ { ingrpc = 1; next }
  ingrpc && /^      [a-zA-Z]/ { ingrpc = 0 }
  ingrpc && /^        tls:$/ { found = 1 }
  END { exit !found }
' "$tmp/tls.yaml"; then
  fail "manager grpc.tls is not nested under server.grpc"
fi
grep -q '^          caCert: /etc/dragonfly/certs/ca.crt$' "$tmp/tls.yaml" || fail "manager grpc tls caCert missing"
grep -q '^          cert: /etc/dragonfly/certs/tls.crt$' "$tmp/tls.yaml" || fail "manager grpc tls cert missing"
grep -q '^          key: /etc/dragonfly/certs/tls.key$' "$tmp/tls.yaml" || fail "manager grpc tls key missing"

# Patch 2/3: three manager and three scheduler probes gain TLS flags.
if grep -q -- '- -tls$' "$tmp/plain.yaml"; then
  fail "plaintext render must not add probe TLS flags"
fi
if [ "$(grep -c -- '- -tls$' "$tmp/tls.yaml")" -ne 6 ]; then
  fail "expected TLS flags on six probes"
fi
if [ "$(grep -c -- '- -tls-ca-cert=/etc/dragonfly/certs/ca.crt$' "$tmp/tls.yaml")" -ne 6 ]; then
  fail "expected TLS caCert flag on six probes"
fi
if [ "$(grep -c -- '- -tls-client-cert=/etc/dragonfly/certs/tls.crt$' "$tmp/tls.yaml")" -ne 6 ]; then
  fail "expected TLS client cert flag on six probes"
fi
if [ "$(grep -c -- '- -tls-client-key=/etc/dragonfly/certs/tls.key$' "$tmp/tls.yaml")" -ne 6 ]; then
  fail "expected TLS client key flag on six probes"
fi
grep -q -- '- -addr=:65003' "$tmp/tls.yaml" || fail "manager probe address missing"
grep -q -- '- -addr=:8002' "$tmp/tls.yaml" || fail "scheduler probe address missing"
if [ "$(grep -c 'grpc_health_probe' "$tmp/tls.yaml")" -ne 6 ]; then
  fail "expected six grpc_health_probe commands"
fi

echo "dragonfly TLS render checks passed"
