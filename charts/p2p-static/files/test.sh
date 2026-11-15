#!/usr/bin/env bash
set -Eeuo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL="${HERE}/p2p-install.sh"

if [ ! -f "$INSTALL" ]; then
  echo "p2p-static self-test: missing ${INSTALL}" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

payload() {
  local ca cert key env host
  host="${1:-scheduler.example.com}"
  ca="$(printf 'dummy-ca\n' | base64 | tr -d '\n')"
  cert="$(printf 'dummy-cert\n' | base64 | tr -d '\n')"
  key="$(printf 'dummy-key\n' | base64 | tr -d '\n')"
  env="$(printf 'MANAGER_GRPC=https://manager-grpc.example.com:65003\nSTATIC_URL=http://static.example.com\nSCHEDULER_HOST=%s\nSCHEDULER_CLUSTER_ID=2\nCA_CRT_B64=%s\nCLIENT_CRT_B64=%s\nCLIENT_KEY_B64=%s\n' "$host" "$ca" "$cert" "$key")"
  printf '%s' "$env" | base64 | tr -d '\n' | tr '+/' '-_'
}

assert_file() {
  if [ ! -f "$1" ]; then
    echo "p2p-static self-test: missing $1" >&2
    exit 1
  fi
}

assert_contains() {
  if ! grep -Fq "$2" "$1"; then
    echo "p2p-static self-test: $1 does not contain '$2'" >&2
    exit 1
  fi
}

assert_not_contains() {
  if grep -Fq "$2" "$1"; then
    echo "p2p-static self-test: $1 unexpectedly contains '$2'" >&2
    exit 1
  fi
}

assert_count() {
  local file pattern expected actual
  file="$1"
  pattern="$2"
  expected="$3"
  actual="$(grep -Fc "$pattern" "$file" || true)"
  if [ "$actual" -ne "$expected" ]; then
    echo "p2p-static self-test: $file has $actual occurrences of '$pattern', want $expected" >&2
    exit 1
  fi
}

CFG="$(payload)"
ROOT_ONE="${WORK}/root-v1"

P2P_ROOT="$ROOT_ONE" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  bash "$INSTALL" "$CFG" > "${WORK}/run1.log" 2>&1

assert_file "${ROOT_ONE}/etc/dragonfly/certs/ca.crt"
assert_file "${ROOT_ONE}/etc/dragonfly/certs/tls.crt"
assert_file "${ROOT_ONE}/etc/dragonfly/certs/tls.key"
assert_file "${ROOT_ONE}/etc/dragonfly/dfget.yaml"
assert_file "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml"
assert_file "${ROOT_ONE}/etc/nydus/nydusd-config.json"
assert_file "${ROOT_ONE}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl"
assert_file "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap.service"
assert_file "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service"

assert_contains "${ROOT_ONE}/etc/dragonfly/certs/ca.crt" 'dummy-ca'
assert_contains "${ROOT_ONE}/etc/dragonfly/certs/tls.crt" 'dummy-cert'
assert_contains "${ROOT_ONE}/etc/dragonfly/certs/tls.key" 'dummy-key'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'schedulerClusterID: 2'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'manager:'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'manager-grpc.example.com'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'tlsVerify: true'
assert_contains "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml" 'addr: "scheduler.example.com:443"'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'tlsPolicy: "force"'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'autoIssueCert: false'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" 'http://127.0.0.1:4001'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"fallback": true'
assert_not_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" 'enable_oci_tar'
assert_contains "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap.service" 'Before=rke2-server.service rke2-agent.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'Before=rke2-server.service rke2-agent.service'

V1_TEMPLATE="${ROOT_ONE}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl"
assert_contains "$V1_TEMPLATE" 'snapshotter = "overlayfs"'
assert_contains "$V1_TEMPLATE" 'runtimes.nydus'
assert_contains "$V1_TEMPLATE" 'snapshotter = "nydus"'
assert_contains "$V1_TEMPLATE" 'SystemdCgroup = true'
assert_contains "$V1_TEMPLATE" '[proxy_plugins.nydus]'
assert_count "$V1_TEMPLATE" 'snapshotter = "nydus"' 1

if [ "$(stat -c '%a' "${ROOT_ONE}/etc/dragonfly/certs/tls.key")" != "600" ]; then
  echo "p2p-static self-test: tls.key is not mode 0600" >&2
  exit 1
fi

if grep -Fq 'dummy-key' "${WORK}/run1.log" || grep -Fq 'dummy-cert' "${WORK}/run1.log" || grep -Fq 'dummy-ca' "${WORK}/run1.log"; then
  echo "p2p-static self-test: certificate material leaked to logs" >&2
  exit 1
fi

if grep -Eq 'parse_json|python3|awk|jq' "$INSTALL"; then
  echo "p2p-static self-test: install script still references JSON/python/awk/jq" >&2
  exit 1
fi
assert_not_contains "$INSTALL" 'machineGlobalConfig'
assert_not_contains "$INSTALL" 'enable_oci_tar'
assert_contains "$INSTALL" 'fallback_direct'
assert_contains "$INSTALL" 'SCHEDULER_PORT=443'

BEFORE="$(sha256sum "${ROOT_ONE}/etc/dragonfly/dfget.yaml" | cut -d' ' -f1)"
P2P_ROOT="$ROOT_ONE" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  bash "$INSTALL" "$CFG" > "${WORK}/run2.log" 2>&1
AFTER="$(sha256sum "${ROOT_ONE}/etc/dragonfly/dfget.yaml" | cut -d' ' -f1)"
if [ "$BEFORE" != "$AFTER" ]; then
  echo "p2p-static self-test: second run changed dfget.yaml" >&2
  exit 1
fi

ROOT_TWO="${WORK}/root-v2"
P2P_ROOT="$ROOT_TWO" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=2 \
  bash "$INSTALL" "$CFG" > "${WORK}/run3.log" 2>&1
V3_TEMPLATE="${ROOT_TWO}/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.tmpl"
assert_file "$V3_TEMPLATE"
assert_contains "$V3_TEMPLATE" "snapshotter = 'overlayfs'"
assert_contains "$V3_TEMPLATE" 'runtime_platforms.nydus'
assert_contains "$V3_TEMPLATE" "snapshotter = 'nydus'"
assert_contains "$V3_TEMPLATE" 'SystemdCgroup = true'
assert_count "$V3_TEMPLATE" "snapshotter = 'nydus'" 2

ROOT_FALLBACK="${WORK}/root-fallback"
FALLBACK_CFG="$(payload scheduler.unreachable.invalid)"
P2P_ROOT="$ROOT_FALLBACK" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 CONTAINERD_MAJOR=1 \
  P2P_PREFLIGHT_TIMEOUT=1 bash "$INSTALL" "$FALLBACK_CFG" > "${WORK}/fallback.log" 2>&1
FALLBACK_TEMPLATE="${ROOT_FALLBACK}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl"
assert_file "$FALLBACK_TEMPLATE"
assert_contains "$FALLBACK_TEMPLATE" 'snapshotter = "overlayfs"'
assert_not_contains "$FALLBACK_TEMPLATE" 'nydus'
if [ -f "${ROOT_FALLBACK}/etc/dragonfly/dfget.yaml" ]; then
  echo "p2p-static self-test: fallback wrote dfget.yaml" >&2
  exit 1
fi
if [ -f "${ROOT_FALLBACK}/etc/systemd/system/dfdaemon-bootstrap.service" ]; then
  echo "p2p-static self-test: fallback installed dfdaemon service" >&2
  exit 1
fi
if grep -Fq 'dummy-key' "${WORK}/fallback.log" || grep -Fq 'dummy-cert' "${WORK}/fallback.log"; then
  echo "p2p-static self-test: fallback log leaked certificate material" >&2
  exit 1
fi
assert_contains "${WORK}/fallback.log" 'direct registry'

ROOT_FALLBACK2="${WORK}/root-fallback-v2"
P2P_ROOT="$ROOT_FALLBACK2" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 CONTAINERD_MAJOR=2 \
  P2P_PREFLIGHT_TIMEOUT=1 bash "$INSTALL" "$FALLBACK_CFG" > "${WORK}/fallback2.log" 2>&1
FALLBACK_V3="${ROOT_FALLBACK2}/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.tmpl"
assert_file "$FALLBACK_V3"
assert_contains "$FALLBACK_V3" "snapshotter = 'overlayfs'"
assert_not_contains "$FALLBACK_V3" 'nydus'

echo "p2p-static self-test: OK"
