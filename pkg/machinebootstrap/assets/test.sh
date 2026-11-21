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

RUNNER="${WORK}/run-install"
cat > "$RUNNER" <<'RUNNER_EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
install="$1"
cfg="$2"
shift 2
set -a
eval "$(printf '%s' "$cfg" | tr '_-' '/+' | base64 -d)"
exec bash "$install" "$@"
RUNNER_EOF
chmod +x "$RUNNER"

payload() {
  local ca cert key env host mirror ips
  host="${1:-scheduler.example.test}"
  mirror="${2:-}"
  ips="${3-203.0.113.10,203.0.113.11}"
  ca="$(printf 'dummy-ca\n' | base64 | tr -d '\n')"
  cert="$(printf 'dummy-cert\n' | base64 | tr -d '\n')"
  key="$(printf 'dummy-key\n' | base64 | tr -d '\n')"
  env="$(printf 'MANAGER_GRPC=https://manager-grpc.example.test:443\nSTATIC_URL=http://static.example.test\nSCHEDULER_HOST=%s\nSCHEDULER_CLUSTER_ID=2\nSCHEDULER_IPS=%s\nCA_CRT_B64=%s\nCLIENT_CRT_B64=%s\nCLIENT_KEY_B64=%s\n' "$host" "$ips" "$ca" "$cert" "$key")"
  if [ -n "$mirror" ]; then
    env="${env}"$'\n'"NYDUS_MIRROR_URL=${mirror}"
  fi
  printf '%s' "$env" | base64 | tr -d '\n' | tr '+/' '-_'
}

assert_file() {
  if [ ! -f "$1" ]; then
    echo "p2p-static self-test: missing $1" >&2
    exit 1
  fi
}

assert_dir() {
  if [ ! -d "$1" ]; then
    echo "p2p-static self-test: missing directory $1" >&2
    exit 1
  fi
}

assert_contains() {
  if ! grep -Fq -- "$2" "$1"; then
    echo "p2p-static self-test: $1 does not contain '$2'" >&2
    exit 1
  fi
}

assert_not_contains() {
  if grep -Fq -- "$2" "$1"; then
    echo "p2p-static self-test: $1 unexpectedly contains '$2'" >&2
    exit 1
  fi
}

assert_count() {
  local file pattern expected actual
  file="$1"
  pattern="$2"
  expected="$3"
  actual="$(grep -Fc -- "$pattern" "$file" || true)"
  if [ "$actual" -ne "$expected" ]; then
    echo "p2p-static self-test: $file has $actual occurrences of '$pattern', want $expected" >&2
    exit 1
  fi
}

CFG="$(payload)"
ROOT_ONE="${WORK}/root-v1"
SELECTOR="${HERE}/nydus-fs-select.sh"
# Stock Ubuntu 24.04 GA/HWE has both on-demand options disabled. Pin the base
# staging run to =n so the selector deterministically chooses fusedev.
N_KERNEL_CONFIG="${WORK}/kernel-config-n"
GOOD_KERNEL_CONFIG="${WORK}/kernel-config-good"
printf 'CONFIG_EROFS_FS_ONDEMAND=n\nCONFIG_CACHEFILES_ONDEMAND=n\n' > "$N_KERNEL_CONFIG"
printf 'CONFIG_EROFS_FS_ONDEMAND=y\nCONFIG_CACHEFILES_ONDEMAND=y\n' > "$GOOD_KERNEL_CONFIG"
CACHEFILES_DEVICE="${WORK}/dev-cachefiles"
rm -f "$CACHEFILES_DEVICE"

P2P_ROOT="$ROOT_ONE" KERNEL_CONFIG_FILE="$N_KERNEL_CONFIG" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/run1.log" 2>&1

assert_file "${ROOT_ONE}/etc/dragonfly/certs/ca.crt"
assert_file "${ROOT_ONE}/etc/dragonfly/certs/tls.crt"
assert_file "${ROOT_ONE}/etc/dragonfly/certs/tls.key"
assert_file "${ROOT_ONE}/etc/dragonfly/dfget.yaml"
assert_file "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml"
assert_file "${ROOT_ONE}/etc/nydus/config.toml"
assert_file "${ROOT_ONE}/etc/nydus/nydusd-config.json"
assert_file "${ROOT_ONE}/etc/nydus/nydusd-config.fscache.json"
assert_file "${ROOT_ONE}/etc/modules-load.d/nydus.conf"
assert_dir "${ROOT_ONE}/var/lib/containerd-nydus/cache"
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
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'manager-grpc.example.test'
assert_contains "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml" 'addrs: ["203.0.113.10:443", "203.0.113.11:443"]'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml" '.dragonfly-system.svc'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'scheduler:'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" '  caCert: "/etc/dragonfly/certs/ca.crt"'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" '  cert: "/etc/dragonfly/certs/tls.crt"'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" '  key: "/etc/dragonfly/certs/tls.key"'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'tls:'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'upload:'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'client:'
assert_file "${ROOT_ONE}/usr/local/bin/nydus-fs-select"
if [ "$(stat -c '%a' "${ROOT_ONE}/usr/local/bin/nydus-fs-select")" != "755" ]; then
  echo "p2p-static self-test: nydus-fs-select is not mode 0755" >&2
  exit 1
fi
if ! cmp -s "$SELECTOR" "${ROOT_ONE}/usr/local/bin/nydus-fs-select"; then
  echo "p2p-static self-test: installed nydus-fs-select differs from the canonical chart file" >&2
  exit 1
fi
bash -n "$SELECTOR"
# Stock =n selects fusedev, but both nydusd configs stay installed.
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'version = 1'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'root = "/var/lib/containerd-nydus"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'address = "/run/containerd-nydus/containerd-nydus-grpc.sock"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'daemon_mode = "multiple"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'fs_driver = "fusedev"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'nydusd_config = "/etc/nydus/nydusd-config.json"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'nydusd_path = "/usr/local/bin/nydusd"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'nydusimage_path = "/usr/local/bin/nydus-image"'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'enable_referrer_detect = true'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'enable_index_detect = true'
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'containerd/certs.d'
assert_contains "${WORK}/run1.log" 'selected fs_driver=fusedev'
assert_contains "${WORK}/run1.log" "CONFIG_EROFS_FS_ONDEMAND is 'n'"
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" 'fscache'
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" 'threads_number'
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" 'enable_kubeconfig_keychain'
cat > "${WORK}/expected-fusedev.toml" <<'TOML'
version = 1
root = "/var/lib/containerd-nydus"
address = "/run/containerd-nydus/containerd-nydus-grpc.sock"
daemon_mode = "multiple"

# The patched snapshotter reads containerd's certs.d directly
# (/var/lib/rancher/rke2/agent/etc/containerd/certs.d, then
# /etc/containerd/certs.d), so it honors the same hosts.toml mirrors and
# headers that containerd used for the pull. No local MITM proxy or CA is installed.
[daemon]
  nydusd_config = "/etc/nydus/nydusd-config.json"
  nydusd_path = "/usr/local/bin/nydusd"
  nydusimage_path = "/usr/local/bin/nydus-image"
  fs_driver = "fusedev"
  recover_policy = "failover"

# Experimental image auto-detection. These are nydus-snapshotter config.toml
# options under [experimental], not nydusd JSON fields; tarfs stays disabled.
[experimental]
  enable_referrer_detect = true
  enable_index_detect = true
TOML
if ! cmp -s "${WORK}/expected-fusedev.toml" "${ROOT_ONE}/etc/nydus/config.toml"; then
  echo "p2p-static self-test: fusedev config.toml is not the exact expected shape" >&2
  exit 1
fi
# modules-load.d is declarative: it is written even when this staging run
# skips module loading (SKIP_MODULES=1).
if ! printf 'erofs\ncachefiles\n' | cmp -s - "${ROOT_ONE}/etc/modules-load.d/nydus.conf"; then
  echo "p2p-static self-test: modules-load.d/nydus.conf is not the expected erofs/cachefiles list" >&2
  exit 1
fi
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" 'enable_tarfs'
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" 'mirrors_config'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"type": "registry"'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"url": "http://127.0.0.1:4001"'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"fallback": true'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"use_http": true'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"connect_timeout": 5'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"type": "blobcache"'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"fs_prefetch": {'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"threads_count": 8'
assert_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" '"prefetch_all": true'
assert_not_contains "${ROOT_ONE}/etc/nydus/nydusd-config.json" 'enable_oci_tar'

CANONICAL_FSCACHE="${HERE}/nydusd-config.fscache.json"
assert_file "$CANONICAL_FSCACHE"
assert_count "$CANONICAL_FSCACHE" '__NYDUS_CLUSTER_ID__' 2
assert_contains "$CANONICAL_FSCACHE" '"type": "bootstrap"'
assert_contains "$CANONICAL_FSCACHE" '"type": "fscache"'
assert_contains "$CANONICAL_FSCACHE" '"work_dir": "/var/lib/containerd-nydus/cache"'
assert_contains "$CANONICAL_FSCACHE" '"url": "http://127.0.0.1:4001"'
assert_contains "$CANONICAL_FSCACHE" '"ping_url": "http://127.0.0.1:4003/healthy"'
assert_not_contains "$CANONICAL_FSCACHE" 'k3s-gpu-erofs'
assert_not_contains "$CANONICAL_FSCACHE" '"type": "blobcache"'
FSCACHE_JSON="${ROOT_ONE}/etc/nydus/nydusd-config.fscache.json"
assert_contains "$FSCACHE_JSON" '"type": "bootstrap"'
assert_contains "$FSCACHE_JSON" '"id": "2"'
assert_contains "$FSCACHE_JSON" '"domain_id": "2"'
assert_contains "$FSCACHE_JSON" '"version": 2'
assert_contains "$FSCACHE_JSON" '"scheme": "https"'
assert_contains "$FSCACHE_JSON" '"skip_verify": false'
assert_contains "$FSCACHE_JSON" '"timeout": 10'
assert_contains "$FSCACHE_JSON" '"retry_limit": 4'
assert_contains "$FSCACHE_JSON" '"url": "http://127.0.0.1:4001"'
assert_contains "$FSCACHE_JSON" '"ping_url": "http://127.0.0.1:4003/healthy"'
assert_contains "$FSCACHE_JSON" '"fallback": true'
assert_contains "$FSCACHE_JSON" '"check_interval": 5'
assert_contains "$FSCACHE_JSON" '"type": "fscache"'
assert_contains "$FSCACHE_JSON" '"work_dir": "/var/lib/containerd-nydus/cache"'
assert_contains "$FSCACHE_JSON" '"metadata_path": "/var/lib/containerd-nydus/cache"'
assert_not_contains "$FSCACHE_JSON" 'k3s-gpu-erofs'
assert_not_contains "$FSCACHE_JSON" '"type": "blobcache"'
# The generated bootstrap must stay byte-identical to the canonical chart
# template, with only the cluster id substituted.
sed 's/__NYDUS_CLUSTER_ID__/2/g' "$CANONICAL_FSCACHE" > "${WORK}/nydusd-config.fscache.expected.json"
if ! cmp -s "${WORK}/nydusd-config.fscache.expected.json" "$FSCACHE_JSON"; then
  echo "p2p-static self-test: nydusd fscache config does not match the canonical template" >&2
  exit 1
fi
assert_contains "${ROOT_ONE}/etc/nydus/updater.env" 'NYDUS_VERSION=v2.4.5'

ROOT_K3S="${WORK}/root-k3s"
mkdir -p "${ROOT_K3S}/var/lib/rancher/k3s"
P2P_ROOT="$ROOT_K3S" KERNEL_CONFIG_FILE="$N_KERNEL_CONFIG" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/k3s.log" 2>&1
assert_file "${ROOT_K3S}/var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl"
assert_contains "${ROOT_K3S}/etc/systemd/system/dfdaemon-bootstrap.service" 'Before=k3s.service'
assert_not_contains "${ROOT_K3S}/etc/systemd/system/dfdaemon-bootstrap.service" 'rke2-server.service'
assert_contains "${ROOT_K3S}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'Before=k3s.service'
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
assert_not_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'containerd-nydus-grpc'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'systemctl restart nydus-snapshotter-bootstrap.service'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" '/usr/local/bin/nydus-fs-select'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'config_before'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-updater.timer" 'OnUnitActiveSec=24h'
assert_contains "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap.service" 'Before=rke2-server.service rke2-agent.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'Before=rke2-server.service rke2-agent.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'ExecStartPre=/usr/local/bin/nydus-fs-select'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'ExecStart=/usr/local/bin/containerd-nydus-grpc --config /etc/nydus/config.toml --nydusd /usr/local/bin/nydusd'
assert_not_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" '--nydusd-config'
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
assert_contains "$INSTALL" 'NYDUS_VERSION="${NYDUS_VERSION:-v2.4.5}"'
assert_contains "$INSTALL" 'NYDUS_SNAPSHOTTER_REPO="${NYDUS_SNAPSHOTTER_REPO:-gorizond/nydus-snapshotter}"'
assert_contains "$INSTALL" 'NYDUS_SNAPSHOTTER_VERSION="${NYDUS_SNAPSHOTTER_VERSION:-v0.16.0-p2p.2}"'
assert_contains "$INSTALL" 'NYDUS_SNAPSHOTTER_ASSET="${NYDUS_SNAPSHOTTER_ASSET:-nydus-snapshotter-linux-amd64}"'
assert_contains "$INSTALL" "'%s/download/%s/%s'"
assert_contains "$INSTALL" "'%s/latest/download/%s'"
assert_contains "$INSTALL" 'curl -fL'
assert_contains "$INSTALL" 'sha256sum'
assert_contains "$INSTALL" 'nydus-overlayfs'
assert_contains "$INSTALL" '90-nydus.toml'
assert_contains "$INSTALL" 'SMART_PROXY_URL'

BEFORE="$(sha256sum "${ROOT_ONE}/etc/dragonfly/dfget.yaml" | cut -d' ' -f1)"
P2P_ROOT="$ROOT_ONE" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/run2.log" 2>&1
AFTER="$(sha256sum "${ROOT_ONE}/etc/dragonfly/dfget.yaml" | cut -d' ' -f1)"
if [ "$BEFORE" != "$AFTER" ]; then
  echo "p2p-static self-test: second run changed dfget.yaml" >&2
  exit 1
fi

ROOT_TWO="${WORK}/root-v2"
P2P_ROOT="$ROOT_TWO" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=2 \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/run3.log" 2>&1
V3_DROPIN="${ROOT_TWO}/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.d/90-nydus.toml"
assert_file "$V3_DROPIN"
assert_contains "$V3_DROPIN" "snapshotter = 'nydus'"
assert_contains "$V3_DROPIN" 'use_local_image_pull = false'
assert_contains "$V3_DROPIN" 'disable_snapshot_annotations = false'
assert_contains "$V3_DROPIN" 'discard_unpacked_layers = false'
assert_contains "$V3_DROPIN" '[proxy_plugins.nydus]'
assert_not_contains "$V3_DROPIN" 'runtimes.nydus'
if [ -f "${ROOT_TWO}/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.tmpl" ]; then
  echo "p2p-static self-test: v2 install must not overwrite the RKE2 config-v3 template" >&2
  exit 1
fi

ROOT_MIRROR="${WORK}/root-mirror"
MIRROR_CFG="$(payload scheduler.example.test https://proxy.example.test/docker.io/)"
P2P_ROOT="$ROOT_MIRROR" KERNEL_CONFIG_FILE="$N_KERNEL_CONFIG" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$MIRROR_CFG" > "${WORK}/mirror.log" 2>&1
for mirror_host in docker.io index.docker.io _default; do
  MIRROR_FILE="${ROOT_MIRROR}/etc/nydus/mirrors/${mirror_host}/hosts.toml"
  assert_file "$MIRROR_FILE"
  assert_contains "$MIRROR_FILE" 'server = "https://proxy.example.test/docker.io"'
  assert_contains "$MIRROR_FILE" '[host."https://proxy.example.test/docker.io"]'
  assert_count "$MIRROR_FILE" 'server =' 1
  assert_not_contains "$MIRROR_FILE" 'dummy-'
done
assert_file "${ROOT_MIRROR}/etc/nydus/mirrors.enabled"
assert_contains "${ROOT_MIRROR}/etc/nydus/config.toml" '[remote.mirrors_config]'
assert_contains "${ROOT_MIRROR}/etc/nydus/config.toml" 'dir = "/etc/nydus/mirrors"'
assert_contains "${ROOT_MIRROR}/etc/nydus/config.toml" 'enable_index_detect = true'
assert_contains "${ROOT_MIRROR}/etc/nydus/config.toml" 'containerd/certs.d'
assert_not_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '127.0.0.1:4001'
assert_not_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '"proxy"'
assert_not_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" 'use_http'
assert_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '"type": "registry"'
assert_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '"type": "blobcache"'
assert_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '"prefetch_all": true'
assert_not_contains "${WORK}/mirror.log" 'dummy-key'

ROOT_BAD_MIRROR="${WORK}/root-bad-mirror"
if P2P_ROOT="$ROOT_BAD_MIRROR" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$(payload scheduler.example.test ftp://proxy.example.test/docker.io)" > "${WORK}/bad-mirror.log" 2>&1; then
  echo "p2p-static self-test: invalid nydus mirror URL was accepted" >&2
  exit 1
fi
assert_contains "${WORK}/bad-mirror.log" 'nydus_mirror_url must use http:// or https://'

ROOT_FALLBACK="${WORK}/root-fallback"
FALLBACK_CFG="$(payload scheduler.unreachable.invalid)"
P2P_ROOT="$ROOT_FALLBACK" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 CONTAINERD_MAJOR=1 \
  P2P_PREFLIGHT_TIMEOUT=1 "$RUNNER" "$INSTALL" "$FALLBACK_CFG" > "${WORK}/fallback.log" 2>&1
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
  P2P_PREFLIGHT_TIMEOUT=1 "$RUNNER" "$INSTALL" "$FALLBACK_CFG" > "${WORK}/fallback2.log" 2>&1
if [ -f "$FALLBACK_DROPIN" ]; then
  echo "p2p-static self-test: v2 fallback did not remove the nydus drop-in" >&2
  exit 1
fi

FAKE_BIN="${WORK}/fake-curl-bin"
CURL_ASSET_DIR="${WORK}/curl-assets"
CURL_LOG="${WORK}/curl.log"
mkdir -p "$FAKE_BIN" "$CURL_ASSET_DIR"
: > "$CURL_LOG"
cat > "${FAKE_BIN}/curl" <<'FAKECURL'
#!/usr/bin/env bash
set -Eeuo pipefail
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    --retry|--connect-timeout|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [ -z "$out" ] || [ -z "$url" ]; then
  echo "fake curl: missing -o or URL" >&2
  exit 2
fi
printf '%s\n' "$url" >> "${CURL_LOG:?}"
asset="${url##*/}"
if [ -n "${FAKE_CURL_FAIL_ASSET:-}" ] && [ "$asset" = "$FAKE_CURL_FAIL_ASSET" ]; then
  exit 22
fi
case "$asset" in
  *.sha256)
    target="${asset%.sha256}"
    source="${CURL_ASSET_DIR:?}/${target}"
    if [ ! -f "$source" ]; then
      echo "fake curl: missing asset ${target}" >&2
      exit 22
    fi
    hash="$(sha256sum "$source")"
    hash="${hash%% *}"
    printf '%s  %s\n' "$hash" "$target" > "$out"
    ;;
  *)
    printf '#!/bin/sh\nexit 0\n' > "$out"
    cp "$out" "${CURL_ASSET_DIR:?}/${asset}"
    ;;
esac
FAKECURL
chmod 0755 "${FAKE_BIN}/curl"

ROOT_RELEASE="${WORK}/root-release"
P2P_ROOT="$ROOT_RELEASE" SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_SNAPSHOTTER_VERSION=v9.9.9 NYDUS_SNAPSHOTTER_REPO=example-org/example-repo \
  PATH="${FAKE_BIN}:${PATH}" CURL_LOG="$CURL_LOG" CURL_ASSET_DIR="$CURL_ASSET_DIR" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/release.log" 2>&1

assert_file "${ROOT_RELEASE}/usr/local/bin/containerd-nydus-grpc"
if [ "$(stat -c '%a' "${ROOT_RELEASE}/usr/local/bin/containerd-nydus-grpc")" != "755" ]; then
  echo "p2p-static self-test: snapshotter is not mode 0755" >&2
  exit 1
fi
assert_contains "$CURL_LOG" 'https://github.com/example-org/example-repo/releases/download/v9.9.9/nydus-snapshotter-linux-amd64'
assert_contains "$CURL_LOG" 'https://github.com/example-org/example-repo/releases/download/v9.9.9/nydus-snapshotter-linux-amd64.sha256'

ROOT_NO_ASSET="${WORK}/root-no-asset"
P2P_ROOT="$ROOT_NO_ASSET" SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_SNAPSHOTTER_VERSION=v9.9.9 NYDUS_SNAPSHOTTER_REPO=example-org/example-repo \
  FAKE_CURL_FAIL_ASSET=nydus-snapshotter-linux-amd64 \
  PATH="${FAKE_BIN}:${PATH}" CURL_LOG="${WORK}/curl-fail.log" CURL_ASSET_DIR="$CURL_ASSET_DIR" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/no-asset.log" 2>&1
assert_contains "${WORK}/no-asset.log" 'nydus-snapshotter release asset'
assert_contains "${WORK}/no-asset.log" 'is unavailable'
if [ -f "${ROOT_NO_ASSET}/usr/local/bin/containerd-nydus-grpc" ]; then
  echo "p2p-static self-test: missing release asset installed a snapshotter" >&2
  exit 1
fi
assert_file "${ROOT_NO_ASSET}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl"
assert_contains "${ROOT_NO_ASSET}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl" 'snapshotter = "overlayfs"'

MODPROBE_OK="${WORK}/modprobe-ok"
SYSTEMCTL_FAKE="${WORK}/systemctl"
SYSTEMCTL_LOG="${WORK}/systemctl.log"
mkdir -p "${WORK}/selector-bin"
cat > "${WORK}/selector-bin/pgrep" <<'FAKEPGREP'
#!/bin/sh
exit 1
FAKEPGREP
cat > "$MODPROBE_OK" <<'FAKEMODPROBE'
#!/usr/bin/env bash
printf '%s\n' "$1" >> "${MODPROBE_CALLS:?}"
if [ "$1" = "cachefiles" ] && [ "${FAKE_CACHEFILES_CREATE:-0}" = "1" ]; then
  : > "${CACHEFILES_DEVICE:?}"
fi
FAKEMODPROBE
cat > "$SYSTEMCTL_FAKE" <<'FAKESYSTEMCTL'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG:?}"
case "$1" in
  is-active) [ "${FAKE_SYSTEMCTL_ACTIVE:-0}" = "1" ] ;;
  *) : ;;
esac
FAKESYSTEMCTL
chmod 0755 "${WORK}/selector-bin/pgrep" "$MODPROBE_OK" "$SYSTEMCTL_FAKE"

# fscache integration: good kernel, modules and device; the installer runs the
# selector once so config.toml exists before the unit starts.
ROOT_INSTALL_FSCACHE="${WORK}/root-install-fscache"
rm -f "$CACHEFILES_DEVICE"
MODPROBE_CALLS="${WORK}/modprobe-install.log"
: > "$MODPROBE_CALLS"
P2P_ROOT="$ROOT_INSTALL_FSCACHE" KERNEL_CONFIG_FILE="$GOOD_KERNEL_CONFIG" \
  MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="$MODPROBE_CALLS" FAKE_CACHEFILES_CREATE=1 \
  CACHEFILES_DEVICE="$CACHEFILES_DEVICE" \
  PATH="${WORK}/selector-bin:${PATH}" \
  SKIP_DOWNLOAD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/install-fscache.log" 2>&1
assert_file "$CACHEFILES_DEVICE"
assert_contains "$MODPROBE_CALLS" 'erofs'
assert_contains "$MODPROBE_CALLS" 'fscache'
assert_contains "$MODPROBE_CALLS" 'cachefiles'
assert_contains "${WORK}/install-fscache.log" 'selected fs_driver=fscache'
assert_contains "${WORK}/install-fscache.log" 'fs_version=6'
assert_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/config.toml" 'daemon_mode = "shared"'
assert_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/config.toml" 'fs_driver = "fscache"'
assert_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/config.toml" 'nydusd_config = "/etc/nydus/nydusd-config.fscache.json"'
assert_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/config.toml" 'threads_number = 8'
assert_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/config.toml" 'skip_ssl_verify = false'
assert_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/config.toml" 'enable_kubeconfig_keychain = true'
assert_not_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/config.toml" 'enable_referrer_detect'
assert_not_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/config.toml" 'mirrors_config'
assert_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/nydusd-config.fscache.json" '"id": "2"'
assert_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/nydusd-config.fscache.json" '"domain_id": "2"'
assert_not_contains "${ROOT_INSTALL_FSCACHE}/etc/nydus/nydusd-config.fscache.json" 'k3s-gpu-erofs'
assert_file "${ROOT_INSTALL_FSCACHE}/etc/modules-load.d/nydus.conf"

# A mirror URL is installed for both modes; fscache keeps the dfdaemon proxy
# and logs the warning, while fusedev uses [remote.mirrors_config].
ROOT_MIRROR_FSCACHE="${WORK}/root-mirror-fscache"
rm -f "$CACHEFILES_DEVICE"
: > "${WORK}/modprobe-mirror-fscache.log"
P2P_ROOT="$ROOT_MIRROR_FSCACHE" KERNEL_CONFIG_FILE="$GOOD_KERNEL_CONFIG" \
  MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-mirror-fscache.log" FAKE_CACHEFILES_CREATE=1 \
  CACHEFILES_DEVICE="$CACHEFILES_DEVICE" PATH="${WORK}/selector-bin:${PATH}" \
  SKIP_DOWNLOAD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$MIRROR_CFG" > "${WORK}/mirror-fscache.log" 2>&1
assert_file "${ROOT_MIRROR_FSCACHE}/etc/nydus/mirrors.enabled"
assert_contains "${WORK}/mirror-fscache.log" 'only applied with fs_driver=fusedev'
assert_contains "${ROOT_MIRROR_FSCACHE}/etc/nydus/config.toml" 'fs_driver = "fscache"'
assert_not_contains "${ROOT_MIRROR_FSCACHE}/etc/nydus/config.toml" 'mirrors_config'
assert_not_contains "${ROOT_MIRROR_FSCACHE}/etc/nydus/nydusd-config.fscache.json" 'proxy.example.test'

# --- nydus-fs-select direct tests -------------------------------------------

new_selector_root() {
  mkdir -p "$1/etc/nydus"
  printf '{}\n' > "$1/etc/nydus/nydusd-config.fscache.json"
  printf '{}\n' > "$1/etc/nydus/nydusd-config.json"
}

# Stock =n and =m must select fusedev; the kernel check runs before modprobe.
SEL_N="${WORK}/sel-n"
new_selector_root "$SEL_N"
: > "${WORK}/modprobe-sel-n.log"
P2P_ROOT="$SEL_N" KERNEL_CONFIG_FILE="$N_KERNEL_CONFIG" \
  PATH="${WORK}/selector-bin:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-n.log" \
  CACHEFILES_DEVICE="$CACHEFILES_DEVICE" SKIP_MODULES=1 SKIP_SYSTEMD=1 \
  bash "$SELECTOR" > "${WORK}/sel-n.log" 2>&1
assert_contains "${WORK}/sel-n.log" 'selected fs_driver=fusedev'
assert_contains "${WORK}/sel-n.log" "CONFIG_EROFS_FS_ONDEMAND is 'n'"
assert_contains "${SEL_N}/etc/nydus/config.toml" 'fs_driver = "fusedev"'

SEL_M="${WORK}/sel-m"
new_selector_root "$SEL_M"
MOD_KERNEL_CONFIG="${WORK}/kernel-config-module"
printf 'CONFIG_EROFS_FS_ONDEMAND=m\nCONFIG_CACHEFILES_ONDEMAND=y\n' > "$MOD_KERNEL_CONFIG"
P2P_ROOT="$SEL_M" KERNEL_CONFIG_FILE="$MOD_KERNEL_CONFIG" \
  PATH="${WORK}/selector-bin:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-m.log" \
  CACHEFILES_DEVICE="$CACHEFILES_DEVICE" SKIP_MODULES=1 SKIP_SYSTEMD=1 \
  bash "$SELECTOR" > "${WORK}/sel-m.log" 2>&1
assert_contains "${WORK}/sel-m.log" 'selected fs_driver=fusedev'
assert_contains "${WORK}/sel-m.log" "CONFIG_EROFS_FS_ONDEMAND is 'm'"
assert_contains "${SEL_M}/etc/nydus/config.toml" 'fs_driver = "fusedev"'

SEL_MISSING="${WORK}/sel-missing"
new_selector_root "$SEL_MISSING"
P2P_ROOT="$SEL_MISSING" KERNEL_CONFIG_FILE="${WORK}/does-not-exist" \
  PATH="${WORK}/selector-bin:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-missing.log" \
  CACHEFILES_DEVICE="$CACHEFILES_DEVICE" SKIP_MODULES=1 SKIP_SYSTEMD=1 \
  bash "$SELECTOR" > "${WORK}/sel-missing.log" 2>&1
assert_contains "${WORK}/sel-missing.log" 'selected fs_driver=fusedev'
assert_contains "${WORK}/sel-missing.log" 'is not readable'
assert_contains "${SEL_MISSING}/etc/nydus/config.toml" 'fs_driver = "fusedev"'

# Both =y plus a device and no cachefilesd selects fscache and writes the
# exact expected config.toml.
SEL_Y="${WORK}/sel-y"
new_selector_root "$SEL_Y"
rm -f "$CACHEFILES_DEVICE"
: > "${WORK}/modprobe-sel-y.log"
P2P_ROOT="$SEL_Y" KERNEL_CONFIG_FILE="$GOOD_KERNEL_CONFIG" \
  PATH="${WORK}/selector-bin:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-y.log" \
  FAKE_CACHEFILES_CREATE=1 CACHEFILES_DEVICE="$CACHEFILES_DEVICE" \
  bash "$SELECTOR" > "${WORK}/sel-y.log" 2>&1
assert_file "$CACHEFILES_DEVICE"
assert_contains "${WORK}/modprobe-sel-y.log" 'erofs'
assert_contains "${WORK}/modprobe-sel-y.log" 'fscache'
assert_contains "${WORK}/modprobe-sel-y.log" 'cachefiles'
assert_contains "${WORK}/sel-y.log" 'selected fs_driver=fscache'
cat > "${WORK}/expected-fscache.toml" <<'TOML'
version = 1
root = "/var/lib/containerd-nydus"
address = "/run/containerd-nydus/containerd-nydus-grpc.sock"
daemon_mode = "shared"

[daemon]
  nydusd_path = "/usr/local/bin/nydusd"
  nydusimage_path = "/usr/local/bin/nydus-image"
  nydusd_config = "/etc/nydus/nydusd-config.fscache.json"
  fs_driver = "fscache"
  threads_number = 8

[remote]
  skip_ssl_verify = false

[remote.auth]
  enable_kubeconfig_keychain = true
TOML
if ! cmp -s "${WORK}/expected-fscache.toml" "${SEL_Y}/etc/nydus/config.toml"; then
  echo "p2p-static self-test: fscache config.toml is not the exact expected shape" >&2
  exit 1
fi
before_hash="$(sha256sum "${SEL_Y}/etc/nydus/config.toml" | cut -d' ' -f1)"
P2P_ROOT="$SEL_Y" KERNEL_CONFIG_FILE="$GOOD_KERNEL_CONFIG" \
  PATH="${WORK}/selector-bin:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-y2.log" \
  FAKE_CACHEFILES_CREATE=1 CACHEFILES_DEVICE="$CACHEFILES_DEVICE" \
  bash "$SELECTOR" > "${WORK}/sel-y2.log" 2>&1
after_hash="$(sha256sum "${SEL_Y}/etc/nydus/config.toml" | cut -d' ' -f1)"
if [ "$before_hash" != "$after_hash" ]; then
  echo "p2p-static self-test: repeated selector run changed config.toml" >&2
  exit 1
fi

# cachefilesd active after stop/mask would race nydusd for /dev/cachefiles.
SEL_CD="${WORK}/sel-cachefilesd"
new_selector_root "$SEL_CD"
mkdir -p "${SEL_CD}/etc/systemd/system"
printf '[Unit]\nDescription=test cachefilesd unit\n' > "${SEL_CD}/etc/systemd/system/cachefilesd.service"
rm -f "$CACHEFILES_DEVICE"
: > "${WORK}/systemctl-sel.log"
P2P_ROOT="$SEL_CD" KERNEL_CONFIG_FILE="$GOOD_KERNEL_CONFIG" \
  PATH="${WORK}/selector-bin:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-cd.log" \
  FAKE_CACHEFILES_CREATE=1 CACHEFILES_DEVICE="$CACHEFILES_DEVICE" \
  SYSTEMCTL="$SYSTEMCTL_FAKE" SYSTEMCTL_LOG="${WORK}/systemctl-sel.log" FAKE_SYSTEMCTL_ACTIVE=1 \
  bash "$SELECTOR" > "${WORK}/sel-cd.log" 2>&1
assert_contains "${WORK}/sel-cd.log" 'selected fs_driver=fusedev'
assert_contains "${WORK}/sel-cd.log" 'cachefilesd.service is active'
assert_contains "${WORK}/systemctl-sel.log" 'stop cachefilesd.service'
assert_contains "${WORK}/systemctl-sel.log" 'disable cachefilesd.service'
assert_contains "${WORK}/systemctl-sel.log" 'mask cachefilesd.service'
assert_contains "${WORK}/systemctl-sel.log" 'is-active --quiet cachefilesd.service'
assert_contains "${SEL_CD}/etc/nydus/config.toml" 'fs_driver = "fusedev"'

# A cachefilesd process started outside systemd still owns the device.
SEL_PROC="${WORK}/sel-proc"
new_selector_root "$SEL_PROC"
mkdir -p "${WORK}/selector-bin-running"
printf '#!/bin/sh\nexit 0\n' > "${WORK}/selector-bin-running/pgrep"
chmod 0755 "${WORK}/selector-bin-running/pgrep"
rm -f "$CACHEFILES_DEVICE"
P2P_ROOT="$SEL_PROC" KERNEL_CONFIG_FILE="$GOOD_KERNEL_CONFIG" \
  PATH="${WORK}/selector-bin-running:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-proc.log" \
  FAKE_CACHEFILES_CREATE=1 CACHEFILES_DEVICE="$CACHEFILES_DEVICE" \
  bash "$SELECTOR" > "${WORK}/sel-proc.log" 2>&1
assert_contains "${WORK}/sel-proc.log" 'selected fs_driver=fusedev'
assert_contains "${WORK}/sel-proc.log" 'cachefilesd is running'
assert_contains "${SEL_PROC}/etc/nydus/config.toml" 'fs_driver = "fusedev"'

SEL_NODEV="${WORK}/sel-nodev"
new_selector_root "$SEL_NODEV"
MISSING_DEVICE="${WORK}/missing-device"
rm -f "$MISSING_DEVICE"
P2P_ROOT="$SEL_NODEV" KERNEL_CONFIG_FILE="$GOOD_KERNEL_CONFIG" \
  PATH="${WORK}/selector-bin:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-nodev.log" \
  FAKE_CACHEFILES_CREATE=0 CACHEFILES_DEVICE="$MISSING_DEVICE" \
  bash "$SELECTOR" > "${WORK}/sel-nodev.log" 2>&1
assert_contains "${WORK}/sel-nodev.log" 'selected fs_driver=fusedev'
assert_contains "${WORK}/sel-nodev.log" 'is missing after loading cachefiles'
assert_contains "${SEL_NODEV}/etc/nydus/config.toml" 'fs_driver = "fusedev"'

SEL_NOJSON="${WORK}/sel-nojson"
new_selector_root "$SEL_NOJSON"
rm -f "${SEL_NOJSON}/etc/nydus/nydusd-config.fscache.json"
rm -f "$CACHEFILES_DEVICE"
P2P_ROOT="$SEL_NOJSON" KERNEL_CONFIG_FILE="$GOOD_KERNEL_CONFIG" \
  PATH="${WORK}/selector-bin:${PATH}" MODPROBE="$MODPROBE_OK" MODPROBE_CALLS="${WORK}/modprobe-sel-nojson.log" \
  FAKE_CACHEFILES_CREATE=1 CACHEFILES_DEVICE="$CACHEFILES_DEVICE" \
  bash "$SELECTOR" > "${WORK}/sel-nojson.log" 2>&1
assert_contains "${WORK}/sel-nojson.log" 'selected fs_driver=fusedev'
assert_contains "${WORK}/sel-nojson.log" 'is not installed'
assert_contains "${SEL_NOJSON}/etc/nydus/config.toml" 'fs_driver = "fusedev"'

# The selector and the installer must not embed real hosts or credentials.
assert_not_contains "$SELECTOR" 'k3s-gpu-erofs'
assert_not_contains "$SELECTOR" 'harbor'
assert_not_contains "$SELECTOR" 'teletype'
assert_not_contains "$SELECTOR" 'selectel'
if grep -Eq 'https?://' "$SELECTOR"; then
  echo "p2p-static self-test: nydus-fs-select embeds a hardcoded URL" >&2
  exit 1
fi
if grep -Eq 'SECRET|PASSWORD|TOKEN|KEY_B64' "$SELECTOR"; then
  echo "p2p-static self-test: nydus-fs-select references credential material" >&2
  exit 1
fi
if grep -Fq 'dummy-key' "${WORK}/sel-y.log" || grep -Fq 'dummy-cert' "${WORK}/sel-y.log"; then
  echo "p2p-static self-test: selector log leaked certificate material" >&2
  exit 1
fi

README="${HERE}/../README.md"
assert_file "$README"
assert_contains "$README" 'CONFIG_EROFS_FS_ONDEMAND=y'
assert_contains "$README" 'CONFIG_CACHEFILES_ONDEMAND=y'
assert_contains "$README" 'modules-load.d/nydus.conf'
assert_contains "$README" 'fs_version=6'
assert_contains "$README" 'CONFIG_EROFS_FS_ONDEMAND=n'
assert_contains "$README" 'custom or signed kernel'
assert_not_contains "$README" 'k3s-gpu-erofs'

# Empty scheduler IPs: install configs but do not write the dfdaemon start marker.
ROOT_NO_IPS="${WORK}/root-no-ips"
NO_IPS_CFG="$(payload scheduler.example.test "" "")"
P2P_ROOT="$ROOT_NO_IPS" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$NO_IPS_CFG" > "${WORK}/no-ips.log" 2>&1
assert_contains "${ROOT_NO_IPS}/etc/dragonfly/dynconfig.yaml" 'addrs: []'
assert_not_contains "${WORK}/no-ips.log" 'direct-registry'
if [ -e "${ROOT_NO_IPS}/etc/dragonfly/scheduler-ips" ]; then
  echo "p2p-static self-test: empty scheduler IPs wrote the dfdaemon marker" >&2
  exit 1
fi
assert_contains "$INSTALL" 'ConditionPathExists=/etc/dragonfly/scheduler-ips'
assert_contains "$INSTALL" 'if [ -n "${SCHEDULER_IPS:-}" ]; then'

# P2P_DISTRO wins over the filesystem when no k3s directory exists.
ROOT_DISTRO="${WORK}/root-distro"
DISTRO_CFG="$(payload scheduler.example.test "" "")"
P2P_ROOT="$ROOT_DISTRO" P2P_DISTRO=k3s SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$DISTRO_CFG" > "${WORK}/distro.log" 2>&1
assert_contains "${ROOT_DISTRO}/etc/systemd/system/dfdaemon-bootstrap.service" 'Before=k3s.service'

if command -v shellcheck >/dev/null 2>&1; then
  shellcheck "$INSTALL" "$0" || {
    echo "p2p-static self-test: shellcheck failed" >&2
    exit 1
  }
else
  echo "p2p-static self-test: shellcheck not installed; skipping"
fi

echo "p2p-static self-test: OK"
