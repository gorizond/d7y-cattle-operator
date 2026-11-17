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
assert_file "${ROOT_ONE}/etc/nydus/config.toml"
assert_file "${ROOT_ONE}/etc/nydus/nydusd-config.json"
assert_file "${ROOT_ONE}/etc/nydus/updater.env"
assert_file "${ROOT_ONE}/usr/local/bin/nydus-updater"
assert_file "${ROOT_ONE}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl"
assert_file "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap.service"
assert_file "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service"
assert_file "${ROOT_ONE}/etc/systemd/system/nydus-updater.service"
assert_file "${ROOT_ONE}/etc/systemd/system/nydus-updater.timer"

assert_contains "${ROOT_ONE}/etc/dragonfly/certs/ca.crt" 'dummy-ca'
assert_contains "${ROOT_ONE}/etc/dragonfly/certs/tls.crt" 'dummy-cert'
assert_contains "${ROOT_ONE}/etc/dragonfly/certs/tls.key" 'dummy-key'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'schedulerClusterID: 2'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'manager:'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'manager-grpc.example.com'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'tlsVerify: true'
assert_contains "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml" 'addr: "scheduler.example.com:443"'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml" '.dragonfly-system.svc'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'tlsPolicy: "force"'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'autoIssueCert: false'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'caCert:'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'upload:'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'client:'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'daemon_mode = "multiple"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'fs_driver = "fusedev"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'nydusd_path = "/usr/local/bin/nydusd"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'nydusimage_path = "/usr/local/bin/nydus-image"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'enable_referrer_detect = true'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'enable_index_detect = true'
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" 'enable_tarfs'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"type": "registry"'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"url": "http://127.0.0.1:4001"'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"fallback": true'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"connect_timeout": 5'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"type": "blobcache"'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"fs_prefetch": {'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"threads_count": 8'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"prefetch_all": true'
assert_not_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" 'enable_oci_tar'
assert_contains "${ROOT_ONE}/etc/nydus/updater.env" 'NYDUS_VERSION=v0.16.0'
assert_file "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch"
assert_file "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap-boot.service"
assert_file "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap-switch.service"
assert_file "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap-switch.timer"
assert_contains "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap-boot.service" 'Wants=dfdaemon-bootstrap.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap-boot.service" 'Before=dfdaemon-bootstrap.service rke2-server.service rke2-agent.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap-switch.timer" 'OnUnitActiveSec=30s'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'pods --namespace "$NAMESPACE" --label app.kubernetes.io/name=client'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'systemctl disable --now "$UNIT"'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'systemctl enable --now "$UNIT"'
assert_not_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'nydus'
bash -n "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch"
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'nydus-overlayfs'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'systemctl restart nydus-snapshotter-bootstrap.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-updater.timer" 'OnUnitActiveSec=24h'
assert_contains "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap.service" 'Before=rke2-server.service rke2-agent.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'Before=rke2-server.service rke2-agent.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'ExecStart=/usr/local/bin/containerd-nydus-grpc --config /etc/nydus/config.toml --nydusd /usr/local/bin/nydusd --nydusd-config /etc/nydus/nydusd-config.json'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'ExecStartPost=/bin/sh -c'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'Restart=always'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'RuntimeDirectory=containerd-nydus'

V1_TEMPLATE="${ROOT_ONE}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl"
assert_contains "$V1_TEMPLATE" 'snapshotter = "nydus"'
assert_not_contains "$V1_TEMPLATE" 'runtimes.nydus'
assert_not_contains "$V1_TEMPLATE" 'runtime_platforms'
assert_contains "$V1_TEMPLATE" '[proxy_plugins.nydus]'
assert_count "$V1_TEMPLATE" 'snapshotter = "nydus"' 1

SWITCH="${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch"
mkdir -p "${WORK}/switch-bin" "${WORK}/switch-state"
cat > "${WORK}/switch-bin/systemctl" <<'FAKESYSTEMCTL'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG:?}"
case "$1" in
  is-enabled) [ -f "${SYSTEMCTL_ENABLED:?}" ] ;;
  is-active) [ -f "${SYSTEMCTL_ACTIVE:?}" ] ;;
  enable) touch "${SYSTEMCTL_ENABLED}" "${SYSTEMCTL_ACTIVE}" ;;
  disable) rm -f "${SYSTEMCTL_ENABLED}" "${SYSTEMCTL_ACTIVE}" ;;
  *) : ;;
esac
FAKESYSTEMCTL
cat > "${WORK}/switch-bin/crictl" <<'FAKECRICTL'
#!/bin/sh
case "$*" in
  *" pods "*) printf 'pod1\n' ;;
  *" ps "*) printf 'ctr1\n' ;;
esac
FAKECRICTL
chmod 0755 "${WORK}/switch-bin/systemctl" "${WORK}/switch-bin/crictl"

SWITCH_LOG="${WORK}/switch-state/systemctl.log"
SWITCH_ENABLED="${WORK}/switch-state/enabled"
SWITCH_ACTIVE="${WORK}/switch-state/active"
SWITCH_MARKER="${WORK}/switch-state/switched"
: > "$SWITCH_LOG"
rm -f "$SWITCH_ENABLED" "$SWITCH_ACTIVE" "$SWITCH_MARKER"
PATH="${WORK}/switch-bin:${PATH}" \
  SYSTEMCTL_LOG="$SWITCH_LOG" SYSTEMCTL_ENABLED="$SWITCH_ENABLED" SYSTEMCTL_ACTIVE="$SWITCH_ACTIVE" \
  BOOTSTRAP_SWITCH_MARKER="$SWITCH_MARKER" DFDAEMON_CLIENT_PORT=45999 CRICTL=/nonexistent \
  bash "$SWITCH"
assert_contains "$SWITCH_LOG" 'enable --now dfdaemon-bootstrap.service'
assert_file "$SWITCH_ENABLED"
assert_file "$SWITCH_ACTIVE"
if [ -f "$SWITCH_MARKER" ]; then
  echo "p2p-static self-test: switch marked switched while client is absent" >&2
  exit 1
fi

: > "$SWITCH_LOG"
touch "$SWITCH_ENABLED" "$SWITCH_ACTIVE"
PATH="${WORK}/switch-bin:${PATH}" \
  SYSTEMCTL_LOG="$SWITCH_LOG" SYSTEMCTL_ENABLED="$SWITCH_ENABLED" SYSTEMCTL_ACTIVE="$SWITCH_ACTIVE" \
  BOOTSTRAP_SWITCH_MARKER="$SWITCH_MARKER" DFDAEMON_CLIENT_PORT=45999 CRICTL="${WORK}/switch-bin/crictl" \
  bash "$SWITCH"
assert_contains "$SWITCH_LOG" 'disable --now dfdaemon-bootstrap.service'
assert_file "$SWITCH_MARKER"
if [ -f "$SWITCH_ENABLED" ] || [ -f "$SWITCH_ACTIVE" ]; then
  echo "p2p-static self-test: switch did not disable dfdaemon-bootstrap" >&2
  exit 1
fi

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
assert_contains "$INSTALL" 'NYDUS_VERSION="${NYDUS_VERSION:-v0.16.0}"'
assert_contains "$INSTALL" 'nydus-overlayfs'
assert_contains "$INSTALL" '90-nydus.toml'
assert_contains "$INSTALL" 'SMART_PROXY_URL'

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
V3_DROPIN="${ROOT_TWO}/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.d/90-nydus.toml"
assert_file "$V3_DROPIN"
assert_contains "$V3_DROPIN" "snapshotter = 'nydus'"
assert_contains "$V3_DROPIN" 'use_local_image_pull = true'
assert_contains "$V3_DROPIN" 'disable_snapshot_annotations = false'
assert_contains "$V3_DROPIN" 'discard_unpacked_layers = false'
assert_contains "$V3_DROPIN" '[proxy_plugins.nydus]'
assert_not_contains "$V3_DROPIN" 'runtimes.nydus'
if [ -f "${ROOT_TWO}/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.tmpl" ]; then
  echo "p2p-static self-test: v2 install must not overwrite the RKE2 config-v3 template" >&2
  exit 1
fi

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
FALLBACK_DROPIN="${ROOT_FALLBACK2}/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.d/90-nydus.toml"
mkdir -p "$(dirname "$FALLBACK_DROPIN")"
printf 'stale nydus drop-in\n' > "$FALLBACK_DROPIN"
P2P_ROOT="$ROOT_FALLBACK2" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 CONTAINERD_MAJOR=2 \
  P2P_PREFLIGHT_TIMEOUT=1 bash "$INSTALL" "$FALLBACK_CFG" > "${WORK}/fallback2.log" 2>&1
if [ -f "$FALLBACK_DROPIN" ]; then
  echo "p2p-static self-test: v2 fallback did not remove the nydus drop-in" >&2
  exit 1
fi

echo "p2p-static self-test: OK"
