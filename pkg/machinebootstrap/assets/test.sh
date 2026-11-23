#!/usr/bin/env bash
set -Eeuo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL="${HERE}/p2p-install.sh"
# Keep the staging tests on amd64; architecture tests below override this.
export TARGET_ARCH=amd64

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
  local ca cert key env mirror ips id
  mirror="${1:-}"
  ips="${2-203.0.113.10,203.0.113.11}"
  id="${3:-2}"
  ca="$(printf 'dummy-ca\n' | base64 | tr -d '\n')"
  cert="$(printf 'dummy-cert\n' | base64 | tr -d '\n')"
  key="$(printf 'dummy-key\n' | base64 | tr -d '\n')"
  env="$(printf 'MANAGER_GRPC=https://manager-grpc.example.test:443\nSTATIC_URL=http://static.example.test\nSCHEDULER_CLUSTER_ID=%s\nSCHEDULER_IPS=%s\nCA_CRT_B64=%s\nCLIENT_CRT_B64=%s\nCLIENT_KEY_B64=%s\n' "$id" "$ips" "$ca" "$cert" "$key")"
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
assert_contains "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml" 'addrs: ["203.0.113.10:8002", "203.0.113.11:8002"]'
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml" '.dragonfly-system.svc'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'scheduler:'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" '  caCert: "/etc/dragonfly/certs/ca.crt"'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" '  cert: "/etc/dragonfly/certs/tls.crt"'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" '  key: "/etc/dragonfly/certs/tls.key"'
for want in 'proxy:' 'blobs/sha256.*' 'manifests/sha256.*' 'schedulingPolicy: always'; do
  assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" "$want"
done
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
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" "reads containerd's certs.d directly"
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

# Upstream v0.16.0 reads mirrors only from its optional mirrors-config section
# (configured below); it does not implicitly consume containerd's certs.d.
# The installer writes /etc/nydus/mirrors when NYDUS_MIRROR_URL is set,
# otherwise nydusd uses the origin registry (or the dfdaemon proxy in the
# non-mirror config). No local MITM proxy or CA is installed.
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
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'pods --namespace "$NAMESPACE" --label component=client'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'systemctl disable --now "$UNIT"'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'systemctl enable --now "$UNIT"'
assert_not_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'nydus'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'command -v crictl'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" '/usr/local/bin/crictl'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" '/var/lib/rancher/k3s/data/*/bin/crictl'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'k3s crictl'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'in-cluster client pod present; host bootstrap disabled'
assert_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'in-cluster client is absent; host bootstrap re-enabled'
assert_not_contains "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch" 'pod_has_running_container'
bash -n "${ROOT_ONE}/usr/local/bin/dfdaemon-bootstrap-switch"

timer_starts="$(grep -Fc 'systemctl start nydus-updater.timer dfdaemon-bootstrap-switch.timer' "$INSTALL" || true)"
if [ "$timer_starts" -lt 3 ]; then
  echo "p2p-static self-test: switch timer start is missing from an IP branch or fallback" >&2
  exit 1
fi
if ! grep -A1 -F 'fallback_direct() {' "$INSTALL" | grep -Fq 'start_host_timers'; then
  echo "p2p-static self-test: fallback_direct does not start host timers" >&2
  exit 1
fi
timer_line="$(grep -nF 'systemctl start nydus-updater.timer dfdaemon-bootstrap-switch.timer' "$INSTALL" | head -1 | cut -d: -f1)"
restart_line="$(grep -nF 'systemctl restart dfdaemon-bootstrap.service' "$INSTALL" | head -1 | cut -d: -f1)"
if [ -z "$timer_line" ] || [ -z "$restart_line" ] || [ "$timer_line" -ge "$restart_line" ]; then
  echo "p2p-static self-test: switch timer start does not precede dfdaemon restart" >&2
  exit 1
fi
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'nydus-overlayfs'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'containerd-nydus-grpc'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'systemctl restart nydus-snapshotter-bootstrap.service'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" '/usr/local/bin/nydus-fs-select'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'config_before'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'CLIENT_VERSION'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'NYDUS_SNAPSHOTTER_BINARY_SHA256'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'nydus-snapshotter-${NYDUS_SNAPSHOTTER_VERSION}-linux-${HOST_ARCH}.tar.gz'
assert_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'checksum mismatch'
assert_contains "${ROOT_ONE}/etc/nydus/updater.env" 'NYDUS_GITHUB_REPO=dragonflyoss/nydus'
assert_contains "${ROOT_ONE}/etc/nydus/updater.env" 'NYDUS_GITHUB_SHA256=1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-updater.timer" 'OnUnitActiveSec=24h'

# --- nydus-updater updates client/nydus/snapshotter ------------------------
UPDATER_ROOT="${WORK}/updater"
UPDATER_BIN="${UPDATER_ROOT}/bin"
UPDATER_INSTALL="${UPDATER_ROOT}/install"
mkdir -p "$UPDATER_BIN" "$UPDATER_INSTALL" \
  "${UPDATER_ROOT}/client/dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu" \
  "${UPDATER_ROOT}/nydus/nydus-static" \
  "${UPDATER_ROOT}/snapshotter/bin"
for binary in dfdaemon dfget; do
  printf '#!/bin/sh\nexit 0\n' > "${UPDATER_ROOT}/client/dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu/$binary"
done
for binary in nydusd nydus-image nydus-overlayfs; do
  printf '#!/bin/sh\nexit 0\n' > "${UPDATER_ROOT}/nydus/nydus-static/$binary"
done
printf '#!/bin/sh\nexit 0\n' > "${UPDATER_ROOT}/snapshotter/bin/containerd-nydus-grpc"
tar -czf "${UPDATER_ROOT}/client.tgz" -C "${UPDATER_ROOT}/client" dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu
tar -czf "${UPDATER_ROOT}/nydus.tgz" -C "${UPDATER_ROOT}/nydus" nydus-static
tar -czf "${UPDATER_ROOT}/snapshotter.tgz" -C "${UPDATER_ROOT}/snapshotter" bin
UPDATER_CLIENT_SHA="$(sha256sum "${UPDATER_ROOT}/client.tgz" | cut -d' ' -f1)"
UPDATER_NYDUS_SHA="$(sha256sum "${UPDATER_ROOT}/nydus.tgz" | cut -d' ' -f1)"
UPDATER_SNAP_SHA="$(sha256sum "${UPDATER_ROOT}/snapshotter.tgz" | cut -d' ' -f1)"
UPDATER_SNAP_BIN_SHA="$(sha256sum "${UPDATER_ROOT}/snapshotter/bin/containerd-nydus-grpc" | cut -d' ' -f1)"
cat > "${UPDATER_BIN}/curl" <<'UPDATERCURL'
#!/usr/bin/env bash
set -Eeuo pipefail
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    --retry|--retry-delay|--connect-timeout|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [ -z "$out" ] || [ -z "$url" ]; then
  echo "fake updater curl: missing -o or URL" >&2
  exit 2
fi
if [ -n "${UPDATER_STATIC_DIR:-}" ] && [[ "$url" == http://static.example.test/* ]]; then
  rel="${url#http://static.example.test/}"
  if [ -f "${UPDATER_STATIC_DIR}/${rel}" ]; then
    cp "${UPDATER_STATIC_DIR}/${rel}" "$out"
    exit 0
  fi
  exit 22
fi
if [ "${FAKE_CURL_FAIL_GITHUB:-0}" = "1" ] && [[ "$url" == https://github.com/* ]]; then
  exit 22
fi
case "$url" in
  *.sha256*|*.sha256sum*) exit 22 ;;
  *dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu.tar.gz) cp "${UPDATER_CLIENT_TGZ:?}" "$out" ;;
  *nydus-static-v2.4.5-linux-amd64.tgz) cp "${UPDATER_NYDUS_TGZ:?}" "$out" ;;
  *nydus-snapshotter-v0.16.0-linux-amd64.tar.gz) cp "${UPDATER_SNAP_TGZ:?}" "$out" ;;
  *) echo "fake updater curl: unexpected URL ${url}" >&2; exit 22 ;;
esac
UPDATERCURL
cat > "${UPDATER_BIN}/systemctl" <<'UPDATERSYSTEMCTL'
#!/bin/sh
exit 0
UPDATERSYSTEMCTL
chmod 0755 "${UPDATER_BIN}/curl" "${UPDATER_BIN}/systemctl"
cat > "${UPDATER_ROOT}/updater.env" <<EOF
STATIC_URL=http://static.example.test
CLIENT_VERSION=v1.5.14
CLIENT_RELEASE_REPO=dragonflyoss/client
CLIENT_SHA256=${UPDATER_CLIENT_SHA}
NYDUS_VERSION=v2.4.5
NYDUS_RELEASE_REPO=dragonflyoss/nydus
NYDUS_GITHUB_REPO=dragonflyoss/nydus
NYDUS_SHA256=${UPDATER_NYDUS_SHA}
NYDUS_GITHUB_SHA256=${UPDATER_NYDUS_SHA}
NYDUS_SNAPSHOTTER_VERSION=v0.16.0
NYDUS_SNAPSHOTTER_REPO=containerd/nydus-snapshotter
NYDUS_SNAPSHOTTER_SHA256=${UPDATER_SNAP_SHA}
NYDUS_SNAPSHOTTER_BINARY_SHA256=${UPDATER_SNAP_BIN_SHA}
NYDUS_ALLOW_GITHUB=1
EOF
NYDUS_UPDATER_ENV="${UPDATER_ROOT}/updater.env" \
NYDUS_UPDATER_BIN_DIR="$UPDATER_INSTALL" \
NYDUS_UPDATER_SELECTOR="${UPDATER_ROOT}/no-selector" \
UPDATER_CLIENT_TGZ="${UPDATER_ROOT}/client.tgz" \
UPDATER_NYDUS_TGZ="${UPDATER_ROOT}/nydus.tgz" \
UPDATER_SNAP_TGZ="${UPDATER_ROOT}/snapshotter.tgz" \
PATH="${UPDATER_BIN}:${PATH}" \
  bash "${ROOT_ONE}/usr/local/bin/nydus-updater" > "${WORK}/updater.log" 2>&1
assert_contains "${WORK}/updater.log" 'updated client dfdaemon v1.5.14'
assert_contains "${WORK}/updater.log" 'updated nydus nydusd v2.4.5'
assert_contains "${WORK}/updater.log" 'updated nydus-snapshotter v0.16.0'
for binary in dfdaemon dfget nydusd nydus-image nydus-overlayfs containerd-nydus-grpc; do
  assert_file "${UPDATER_INSTALL}/${binary}"
done

# Static store first: versioned client binaries and nydus/snapshotter tarballs.
UPDATER_STATIC="${UPDATER_ROOT}/static-store"
mkdir -p "$UPDATER_STATIC/client/v1.5.14/amd64" \
  "$UPDATER_STATIC/nydus/v2.4.5/amd64" \
  "$UPDATER_STATIC/snapshotter/v0.16.0/amd64"
cp "${UPDATER_ROOT}/client/dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu/dfdaemon" "$UPDATER_STATIC/client/v1.5.14/amd64/dfdaemon"
cp "${UPDATER_ROOT}/client/dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu/dfget" "$UPDATER_STATIC/client/v1.5.14/amd64/dfget"
cp "${UPDATER_ROOT}/nydus.tgz" "$UPDATER_STATIC/nydus/v2.4.5/amd64/nydus-static.tgz"
cp "${UPDATER_ROOT}/snapshotter.tgz" "$UPDATER_STATIC/snapshotter/v0.16.0/amd64/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz"
STATIC_INSTALL="${UPDATER_ROOT}/install-static"
mkdir -p "$STATIC_INSTALL"
NYDUS_UPDATER_ENV="${UPDATER_ROOT}/updater.env" \
NYDUS_UPDATER_BIN_DIR="$STATIC_INSTALL" \
NYDUS_UPDATER_SELECTOR="${UPDATER_ROOT}/no-selector" \
UPDATER_STATIC_DIR="$UPDATER_STATIC" FAKE_CURL_FAIL_GITHUB=1 \
UPDATER_CLIENT_TGZ="${UPDATER_ROOT}/client.tgz" \
UPDATER_NYDUS_TGZ="${UPDATER_ROOT}/nydus.tgz" \
UPDATER_SNAP_TGZ="${UPDATER_ROOT}/snapshotter.tgz" \
PATH="${UPDATER_BIN}:${PATH}" \
  bash "${ROOT_ONE}/usr/local/bin/nydus-updater" > "${WORK}/updater-static.log" 2>&1
assert_contains "${WORK}/updater-static.log" 'using static store'
for binary in dfdaemon dfget nydusd nydus-image nydus-overlayfs containerd-nydus-grpc; do
  assert_file "${STATIC_INSTALL}/${binary}"
done

# Empty STATIC_URL skips the static store and uses GitHub.
EMPTY_STATIC_INSTALL="${UPDATER_ROOT}/install-empty-static"
mkdir -p "$EMPTY_STATIC_INSTALL"
sed 's|^STATIC_URL=.*|STATIC_URL=|' "${UPDATER_ROOT}/updater.env" > "${UPDATER_ROOT}/updater-empty-static.env"
NYDUS_UPDATER_ENV="${UPDATER_ROOT}/updater-empty-static.env" \
NYDUS_UPDATER_BIN_DIR="$EMPTY_STATIC_INSTALL" \
NYDUS_UPDATER_SELECTOR="${UPDATER_ROOT}/no-selector" \
UPDATER_CLIENT_TGZ="${UPDATER_ROOT}/client.tgz" \
UPDATER_NYDUS_TGZ="${UPDATER_ROOT}/nydus.tgz" \
UPDATER_SNAP_TGZ="${UPDATER_ROOT}/snapshotter.tgz" \
PATH="${UPDATER_BIN}:${PATH}" \
  bash "${ROOT_ONE}/usr/local/bin/nydus-updater" > "${WORK}/updater-empty-static.log" 2>&1
assert_contains "${WORK}/updater-empty-static.log" 'using GitHub release'
for binary in dfdaemon dfget nydusd containerd-nydus-grpc; do
  assert_file "${EMPTY_STATIC_INSTALL}/${binary}"
done

# A checksum mismatch must fail before the bad artifact is installed.

BAD_INSTALL="${UPDATER_ROOT}/install-bad"
mkdir -p "$BAD_INSTALL"
sed 's/^NYDUS_SNAPSHOTTER_SHA256=.*/NYDUS_SNAPSHOTTER_SHA256=deadbeef/' "${UPDATER_ROOT}/updater.env" > "${UPDATER_ROOT}/updater-bad.env"
if NYDUS_UPDATER_ENV="${UPDATER_ROOT}/updater-bad.env" \
  NYDUS_UPDATER_BIN_DIR="$BAD_INSTALL" \
  NYDUS_UPDATER_SELECTOR="${UPDATER_ROOT}/no-selector" \
  UPDATER_CLIENT_TGZ="${UPDATER_ROOT}/client.tgz" \
  UPDATER_NYDUS_TGZ="${UPDATER_ROOT}/nydus.tgz" \
  UPDATER_SNAP_TGZ="${UPDATER_ROOT}/snapshotter.tgz" \
  PATH="${UPDATER_BIN}:${PATH}" \
  bash "${ROOT_ONE}/usr/local/bin/nydus-updater" > "${WORK}/updater-bad.log" 2>&1; then
  echo "p2p-static self-test: updater accepted a bad checksum" >&2
  exit 1
fi
assert_contains "${WORK}/updater-bad.log" 'checksum mismatch'
if [ -e "${BAD_INSTALL}/containerd-nydus-grpc" ]; then
  echo "p2p-static self-test: updater installed a bad snapshotter after checksum mismatch" >&2
  exit 1
fi
# A wrong extracted-binary pin must fail even when the tarball checksum is good.
BAD_BIN_INSTALL="${UPDATER_ROOT}/install-bad-bin"
mkdir -p "$BAD_BIN_INSTALL"
sed 's/^NYDUS_SNAPSHOTTER_BINARY_SHA256=.*/NYDUS_SNAPSHOTTER_BINARY_SHA256=deadbeef/' "${UPDATER_ROOT}/updater.env" > "${UPDATER_ROOT}/updater-bad-bin.env"
if NYDUS_UPDATER_ENV="${UPDATER_ROOT}/updater-bad-bin.env" \
  NYDUS_UPDATER_BIN_DIR="$BAD_BIN_INSTALL" \
  NYDUS_UPDATER_SELECTOR="${UPDATER_ROOT}/no-selector" \
  UPDATER_CLIENT_TGZ="${UPDATER_ROOT}/client.tgz" \
  UPDATER_NYDUS_TGZ="${UPDATER_ROOT}/nydus.tgz" \
  UPDATER_SNAP_TGZ="${UPDATER_ROOT}/snapshotter.tgz" \
  PATH="${UPDATER_BIN}:${PATH}" \
  bash "${ROOT_ONE}/usr/local/bin/nydus-updater" > "${WORK}/updater-bad-bin.log" 2>&1; then
  echo "p2p-static self-test: updater accepted a bad binary checksum" >&2
  exit 1
fi
assert_contains "${WORK}/updater-bad-bin.log" 'checksum mismatch'
if [ -e "${BAD_BIN_INSTALL}/containerd-nydus-grpc" ]; then
  echo "p2p-static self-test: updater installed a snapshotter with a bad binary checksum" >&2
  exit 1
fi

assert_contains "${ROOT_ONE}/etc/systemd/system/dfdaemon-bootstrap.service" 'Before=rke2-server.service rke2-agent.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'Before=rke2-server.service rke2-agent.service'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'ExecStartPre=/usr/local/bin/nydus-fs-select'
assert_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'After=dfdaemon-bootstrap.service'
assert_not_contains "${ROOT_ONE}/etc/systemd/system/nydus-snapshotter-bootstrap.service" 'Wants=dfdaemon-bootstrap.service'
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
[ "${FAKE_CRICTL_EMPTY:-0}" = "1" ] && exit 0
case "$*" in
  *" pods "*) printf 'pod1\n' ;;
  *" ps "*) [ "${FAKE_CRICTL_NO_RUNNING:-0}" = "1" ] || printf 'ctr1\n' ;;
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
  BOOTSTRAP_SWITCH_MARKER="$SWITCH_MARKER" DFDAEMON_CLIENT_PORT=45999 CRICTL=/nonexistent FAKE_CRICTL_EMPTY=1 \
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
  BOOTSTRAP_SWITCH_MARKER="$SWITCH_MARKER" DFDAEMON_CLIENT_PORT=45999 CRICTL="${WORK}/switch-bin/crictl" FAKE_CRICTL_NO_RUNNING=1 \
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
assert_contains "$INSTALL" 'SCHEDULER_PORT=8002'
assert_contains "$INSTALL" 'NYDUS_VERSION="${NYDUS_VERSION:-${DEFAULT_NYDUS_VERSION}}"'
assert_contains "$INSTALL" 'NYDUS_SNAPSHOTTER_REPO="${NYDUS_SNAPSHOTTER_REPO:-containerd/nydus-snapshotter}"'
assert_contains "$INSTALL" 'NYDUS_SNAPSHOTTER_VERSION="${NYDUS_SNAPSHOTTER_VERSION:-${DEFAULT_NYDUS_SNAPSHOTTER_VERSION}}"'
assert_contains "$INSTALL" 'NYDUS_SNAPSHOTTER_ASSET="${NYDUS_SNAPSHOTTER_ASSET:-nydus-snapshotter-${NYDUS_SNAPSHOTTER_VERSION}-linux-${NYDUS_SNAPSHOTTER_ARCH}.tar.gz}"'
assert_not_contains "$INSTALL" 'p2p.2'
assert_not_contains "$INSTALL" 'gorizond/nydus-snapshotter'
assert_contains "$INSTALL" 'DRAGONFLY_CLIENT_LIBC="${DRAGONFLY_CLIENT_LIBC:-gnu}"'
assert_contains "$INSTALL" 'TARGET_ARCH="${TARGET_ARCH:-$(uname -m)}"'
assert_contains "$INSTALL" 'x86_64|amd64)'
assert_contains "$INSTALL" 'aarch64|arm64)'
assert_contains "$INSTALL" 'armv7l|armhf|armv6l)'
assert_contains "$INSTALL" 'ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7'
assert_contains "$INSTALL" '370ff792a1c6957a7244b770d6cd12792668e326b2282b3017fea2cbfa3510af'
assert_contains "$INSTALL" '1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072'
assert_contains "$INSTALL" '85925af702d79fc3a8b9d501a5267ddc9f81449ff9fe94841845eb98eda84593'
assert_contains "$INSTALL" '24e4ceaae9c87e396e71e89bd3eb074f2cc6a5a14b1a2081c8d890481857a0e4'
assert_contains "$INSTALL" '9bd24308ef51139310bf58927a97eec49567d0154606bc5774ad69d2ee492ff0'
assert_contains "$INSTALL" '136e9c649a63c16b3d3322a15fcdf918e7dda9a4cbb043e2923451e87a8ba08f'
assert_contains "$INSTALL" '399f7a5b800bb6d05c63bbb340e7fa1820b84609b882589a6a95dd8796d03859'
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
MIRROR_CFG="$(payload https://proxy.example.test/docker.io/)"
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
assert_not_contains "${ROOT_MIRROR}/etc/nydus/config.toml" "reads containerd's certs.d directly"
assert_not_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '127.0.0.1:4001'
assert_not_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '"proxy"'
assert_not_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" 'use_http'
assert_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '"type": "registry"'
assert_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '"type": "blobcache"'
assert_contains "${ROOT_MIRROR}/etc/nydus/nydusd-config.json" '"prefetch_all": true'
assert_not_contains "${WORK}/mirror.log" 'dummy-key'

ROOT_BAD_MIRROR="${WORK}/root-bad-mirror"
if P2P_ROOT="$ROOT_BAD_MIRROR" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$(payload ftp://proxy.example.test/docker.io)" > "${WORK}/bad-mirror.log" 2>&1; then
  echo "p2p-static self-test: invalid nydus mirror URL was accepted" >&2
  exit 1
fi
assert_contains "${WORK}/bad-mirror.log" 'nydus_mirror_url must use http:// or https://'

ROOT_FALLBACK="${WORK}/root-fallback"
FALLBACK_CFG="$(payload)"
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
if [ "${FAKE_CURL_FAIL_STATIC:-0}" = "1" ] && [[ "$url" == http://static.example.test/* ]]; then
  exit 22
fi
if [ -n "${FAKE_CURL_FAIL_STATIC_ASSETS:-}" ] && [[ "$url" == http://static.example.test/* ]]; then
  case " ${FAKE_CURL_FAIL_STATIC_ASSETS} " in
    *" ${asset} "*) exit 22 ;;
  esac
fi
if [ "${FAKE_CURL_FAIL_GITHUB:-0}" = "1" ] && [[ "$url" == https://github.com/* ]]; then
  exit 22
fi
case "$asset" in
  *.sha256sum|*.sha256)
    target="${asset%.sha256sum}"
    target="${target%.sha256}"
    if [ -n "${FAKE_CURL_FAIL_CHECKSUM_ASSETS:-}" ]; then
      case " ${FAKE_CURL_FAIL_CHECKSUM_ASSETS} " in
        *" ${target} "*) exit 22 ;;
      esac
    fi
    source="${CURL_ASSET_DIR:?}/${target}"
    if [ ! -f "$source" ]; then
      echo "fake curl: missing asset ${target}" >&2
      exit 22
    fi
    if [ "${FAKE_CURL_BAD_SHA256:-0}" = "1" ]; then
      hash="deadbeef"
    else
      hash="$(sha256sum "$source")"
      hash="${hash%% *}"
    fi
    printf '%s  %s\n' "$hash" "$target" > "$out"
    ;;
  *)
    if [ -f "${CURL_ASSET_DIR:?}/${asset}" ]; then
      cp "${CURL_ASSET_DIR}/${asset}" "$out"
    else
      printf '#!/bin/sh\nexit 0\n' > "$out"
      cp "$out" "${CURL_ASSET_DIR}/${asset}"
    fi
    ;;
esac
FAKECURL
chmod 0755 "${FAKE_BIN}/curl"

RELEASE_NYDUS_SRC="${WORK}/release-nydus/nydus-static"
RELEASE_NYDUS_TGZ="${WORK}/release-nydus.tgz"
mkdir -p "$RELEASE_NYDUS_SRC"
for binary in nydusd nydus-image nydus-overlayfs; do
  printf '#!/bin/sh\nexit 0\n' > "$RELEASE_NYDUS_SRC/$binary"
done
tar -czf "$RELEASE_NYDUS_TGZ" -C "${WORK}/release-nydus" nydus-static
RELEASE_NYDUS_SHA="$(sha256sum "$RELEASE_NYDUS_TGZ" | cut -d' ' -f1)"
cp "$RELEASE_NYDUS_TGZ" "${CURL_ASSET_DIR}/nydus-static.tgz"
cp "$RELEASE_NYDUS_TGZ" "${CURL_ASSET_DIR}/nydus-static-v2.4.5-linux-amd64.tgz"
RELEASE_SNAP_SRC="${WORK}/release-snap/bin"
RELEASE_SNAP_TGZ="${WORK}/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz"
mkdir -p "$RELEASE_SNAP_SRC"
printf '#!/bin/sh\nexit 0\n' > "$RELEASE_SNAP_SRC/containerd-nydus-grpc"
tar -czf "$RELEASE_SNAP_TGZ" -C "${WORK}/release-snap" bin
RELEASE_SNAP_SHA="$(sha256sum "$RELEASE_SNAP_TGZ" | cut -d' ' -f1)"
RELEASE_SNAP_BIN_SHA="$(sha256sum "$RELEASE_SNAP_SRC/containerd-nydus-grpc" | cut -d' ' -f1)"
cp "$RELEASE_SNAP_TGZ" "${CURL_ASSET_DIR}/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz"
RELEASE_SNAP_ARM_SRC="${WORK}/release-snap-arm/bin"
RELEASE_SNAP_ARM_TGZ="${WORK}/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz"
mkdir -p "$RELEASE_SNAP_ARM_SRC"
printf '#!/bin/sh\nexit 0\n' > "$RELEASE_SNAP_ARM_SRC/containerd-nydus-grpc"
tar -czf "$RELEASE_SNAP_ARM_TGZ" -C "${WORK}/release-snap-arm" bin
RELEASE_SNAP_ARM_SHA="$(sha256sum "$RELEASE_SNAP_ARM_TGZ" | cut -d' ' -f1)"
RELEASE_SNAP_ARM_BIN_SHA="$(sha256sum "$RELEASE_SNAP_ARM_SRC/containerd-nydus-grpc" | cut -d' ' -f1)"
cp "$RELEASE_SNAP_ARM_TGZ" "${CURL_ASSET_DIR}/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz"

ROOT_RELEASE="${WORK}/root-release"
P2P_ROOT="$ROOT_RELEASE" SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_SNAPSHOTTER_VERSION=v0.16.0 NYDUS_SNAPSHOTTER_REPO=example-org/example-repo \
  NYDUS_GITHUB_SHA256="$RELEASE_NYDUS_SHA" NYDUS_SNAPSHOTTER_SHA256="$RELEASE_SNAP_SHA" \
  NYDUS_SNAPSHOTTER_BINARY_SHA256="$RELEASE_SNAP_BIN_SHA" P2P_ASSETS=0 \
  PATH="${FAKE_BIN}:${PATH}" CURL_LOG="$CURL_LOG" CURL_ASSET_DIR="$CURL_ASSET_DIR" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/release.log" 2>&1

assert_file "${ROOT_RELEASE}/usr/local/bin/dfdaemon"
assert_file "${ROOT_RELEASE}/usr/local/bin/dfget"
assert_file "${ROOT_RELEASE}/usr/local/bin/containerd-nydus-grpc"
if [ "$(stat -c '%a' "${ROOT_RELEASE}/usr/local/bin/containerd-nydus-grpc")" != "755" ]; then
  echo "p2p-static self-test: snapshotter is not mode 0755" >&2
  exit 1
fi
assert_contains "$CURL_LOG" 'http://static.example.test/snapshotter/v0.16.0/amd64/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz'
assert_contains "$CURL_LOG" 'nydus-static-v2.4.5-linux-amd64.tgz.sha256'
assert_contains "$CURL_LOG" 'nydus-snapshotter-v0.16.0-linux-amd64.tar.gz.sha256'
assert_contains "${WORK}/release.log" 'nydus tarball: using static store'

ROOT_NO_ASSET="${WORK}/root-no-asset"
P2P_ROOT="$ROOT_NO_ASSET" SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_SNAPSHOTTER_VERSION=v0.16.0 NYDUS_SNAPSHOTTER_REPO=example-org/example-repo \
  NYDUS_GITHUB_SHA256="$RELEASE_NYDUS_SHA" NYDUS_SNAPSHOTTER_SHA256="$RELEASE_SNAP_SHA" \
  NYDUS_SNAPSHOTTER_BINARY_SHA256="$RELEASE_SNAP_BIN_SHA" P2P_ASSETS=0 \
  FAKE_CURL_FAIL_ASSET=nydus-snapshotter-v0.16.0-linux-amd64.tar.gz \
  PATH="${FAKE_BIN}:${PATH}" CURL_LOG="${WORK}/curl-fail.log" CURL_ASSET_DIR="$CURL_ASSET_DIR" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/no-asset.log" 2>&1
assert_contains "${WORK}/no-asset.log" 'upstream nydus-snapshotter v0.16.0 amd64 tarball'
assert_contains "${WORK}/no-asset.log" 'is unavailable'
if [ -f "${ROOT_NO_ASSET}/usr/local/bin/containerd-nydus-grpc" ]; then
  echo "p2p-static self-test: missing release asset installed a snapshotter" >&2
  exit 1
fi
assert_file "${ROOT_NO_ASSET}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl"
assert_contains "${ROOT_NO_ASSET}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl" 'snapshotter = "overlayfs"'

# --- P2P bootstrap asset delivery ------------------------------------------
for binary in dfdaemon dfget; do
  if [ ! -f "${CURL_ASSET_DIR}/${binary}" ]; then
    printf '#!/bin/sh\nexit 0\n' > "${CURL_ASSET_DIR}/${binary}"
  fi
done
cat > "${FAKE_BIN}/dfget" <<'FAKEDFGET'
#!/usr/bin/env bash
set -Eeuo pipefail
out=""
url=""
digest=""
policy=""
endpoint=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --scheduling-policy) policy="$2"; shift 2 ;;
    --endpoint) endpoint="$2"; shift 2 ;;
    --timeout) shift 2 ;;
    --digest) digest="$2"; shift 2 ;;
    -O) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf 'policy=%s endpoint=%s digest=%s url=%s out=%s\n' "$policy" "$endpoint" "$digest" "$url" "$out" >> "${DFGET_LOG:?}"
[ "$policy" = "always" ] || { echo "fake dfget: policy=${policy}" >&2; exit 2; }
[ "$endpoint" = "/var/run/dragonfly/dfdaemon.sock" ] || { echo "fake dfget: endpoint=${endpoint}" >&2; exit 2; }
case "${FAKE_DFGET_MODE:-ok}" in
  fail) exit 22 ;;
  corrupt) printf 'corrupt\n' > "$out"; exit 0 ;;
esac
case "$url" in
  *nydus-static-v2.4.5-linux-amd64.tgz) cp "${FAKE_NYDUS_TARBALL:?}" "$out" ;;
  *nydus-snapshotter-v0.16.0-linux-amd64.tar.gz) cp "${FAKE_SNAPSHOTTER:?}" "$out" ;;
  *) exit 22 ;;
esac
FAKEDFGET
chmod 0755 "${FAKE_BIN}/dfget"
FAKE_SNAPSHOTTER_SHA="$RELEASE_SNAP_SHA"
FAKE_SNAPSHOTTER_BIN_SHA="$RELEASE_SNAP_BIN_SHA"

DFGET_LOG="${WORK}/dfget.log"
p2p_run() {
  local root="$1" cfg="$2" p2p="$3" mode="$4"
  shift 4
  : > "$DFGET_LOG"
  env P2P_ROOT="$root" SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
    NYDUS_SNAPSHOTTER_VERSION=v0.16.0 NYDUS_SNAPSHOTTER_REPO=example-org/example-repo \
    NYDUS_GITHUB_SHA256="$RELEASE_NYDUS_SHA" NYDUS_SNAPSHOTTER_SHA256="$FAKE_SNAPSHOTTER_SHA" \
    NYDUS_SNAPSHOTTER_BINARY_SHA256="$FAKE_SNAPSHOTTER_BIN_SHA" \
    P2P_ASSETS="$p2p" FAKE_DFGET_MODE="$mode" \
    DFGET_BIN="${FAKE_BIN}/dfget" DFGET_LOG="$DFGET_LOG" \
    FAKE_NYDUS_TARBALL="$RELEASE_NYDUS_TGZ" FAKE_SNAPSHOTTER="$RELEASE_SNAP_TGZ" \
    PATH="${FAKE_BIN}:${PATH}" CURL_LOG="$CURL_LOG" CURL_ASSET_DIR="$CURL_ASSET_DIR" \
    "$@" "$RUNNER" "$INSTALL" "$cfg"
}

ROOT_P2P="${WORK}/root-p2p"
p2p_run "$ROOT_P2P" "$CFG" 1 ok > "${WORK}/p2p.log" 2>&1
assert_file "${ROOT_P2P}/usr/local/bin/dfget"
assert_file "${ROOT_P2P}/usr/local/bin/nydusd"
assert_file "${ROOT_P2P}/usr/local/bin/containerd-nydus-grpc"
assert_contains "$DFGET_LOG" 'policy=always endpoint=/var/run/dragonfly/dfdaemon.sock'
assert_contains "$DFGET_LOG" 'digest=sha256:'
assert_contains "${WORK}/p2p.log" 'nydus tarball: using P2P'
assert_contains "${WORK}/p2p.log" 'nydus-snapshotter: using P2P'

ROOT_P2P_STATIC="${WORK}/root-p2p-static"
p2p_run "$ROOT_P2P_STATIC" "$CFG" 1 fail > "${WORK}/p2p-static.log" 2>&1
assert_file "${ROOT_P2P_STATIC}/usr/local/bin/nydusd"
assert_file "${ROOT_P2P_STATIC}/usr/local/bin/containerd-nydus-grpc"
assert_contains "${WORK}/p2p-static.log" 'nydus tarball: using static store'
assert_contains "${WORK}/p2p-static.log" 'nydus-snapshotter: using'
assert_contains "$DFGET_LOG" 'policy=always endpoint=/var/run/dragonfly/dfdaemon.sock'

ROOT_P2P_GITHUB="${WORK}/root-p2p-github"
p2p_run "$ROOT_P2P_GITHUB" "$CFG" 1 fail \
  FAKE_CURL_FAIL_STATIC_ASSETS="nydus-static.tgz nydus-snapshotter-v0.16.0-linux-amd64.tar.gz" \
  > "${WORK}/p2p-github.log" 2>&1
assert_file "${ROOT_P2P_GITHUB}/usr/local/bin/nydusd"
assert_file "${ROOT_P2P_GITHUB}/usr/local/bin/containerd-nydus-grpc"
assert_contains "${WORK}/p2p-github.log" 'nydus tarball: using GitHub release'
assert_contains "${WORK}/p2p-github.log" 'nydus-snapshotter: using'

CFG_NO_IPS="$(payload "" "" "")"
ROOT_NO_SCHED="${WORK}/root-no-sched"
p2p_run "$ROOT_NO_SCHED" "$CFG_NO_IPS" 1 ok > "${WORK}/p2p-no-sched.log" 2>&1
assert_file "${ROOT_NO_SCHED}/usr/local/bin/nydusd"
if [ -s "$DFGET_LOG" ]; then
  echo "p2p-static self-test: dfget was called without scheduler IPs" >&2
  exit 1
fi

ROOT_P2P_OFF="${WORK}/root-p2p-off"
p2p_run "$ROOT_P2P_OFF" "$CFG" 0 ok > "${WORK}/p2p-off.log" 2>&1
assert_file "${ROOT_P2P_OFF}/usr/local/bin/nydusd"
if [ -s "$DFGET_LOG" ]; then
  echo "p2p-static self-test: dfget was called with P2P_ASSETS=0" >&2
  exit 1
fi

ROOT_NO_DFGET="${WORK}/root-no-dfget"
p2p_run "$ROOT_NO_DFGET" "$CFG" 1 ok DFGET_BIN="${WORK}/missing-dfget" > "${WORK}/p2p-no-dfget.log" 2>&1
assert_file "${ROOT_NO_DFGET}/usr/local/bin/nydusd"
if [ -s "$DFGET_LOG" ]; then
  echo "p2p-static self-test: dfget was called although DFGET_BIN is missing" >&2
  exit 1
fi

ROOT_BAD_DIGEST="${WORK}/root-bad-digest"
if p2p_run "$ROOT_BAD_DIGEST" "$CFG" 0 ok NYDUS_SNAPSHOTTER_SHA256=deadbeef \
  FAKE_CURL_FAIL_CHECKSUM_ASSETS="nydus-snapshotter-v0.16.0-linux-amd64.tar.gz" \
  > "${WORK}/bad-digest.log" 2>&1; then
  echo "p2p-static self-test: checksum mismatch did not fail the installer" >&2
  exit 1
fi
assert_contains "${WORK}/bad-digest.log" 'checksum verification failed'
if [ -f "${ROOT_BAD_DIGEST}/usr/local/bin/containerd-nydus-grpc" ]; then
  echo "p2p-static self-test: snapshotter was installed after a checksum mismatch" >&2
  exit 1
fi

# --- architecture selection -------------------------------------------------
make_uname() {
  local dir="$1" machine="$2"
  mkdir -p "$dir"
  printf '#!/bin/sh\necho %s\n' "$machine" > "$dir/uname"
  chmod 0755 "$dir/uname"
}

ARCH_AMD_BIN="${WORK}/arch-amd64-bin"
ARCH_ARM_BIN="${WORK}/arch-arm64-bin"
ARCH_V7_BIN="${WORK}/arch-armv7-bin"
make_uname "$ARCH_AMD_BIN" x86_64
make_uname "$ARCH_ARM_BIN" aarch64
make_uname "$ARCH_V7_BIN" armv7l

ROOT_ARCH_AMD="${WORK}/root-arch-amd64"
ARCH_AMD_CURL="${WORK}/curl-arch-amd64.log"
: > "$ARCH_AMD_CURL"
env TARGET_ARCH= P2P_ROOT="$ROOT_ARCH_AMD" SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_ALLOW_GITHUB=0 P2P_ASSETS=0 FAKE_CURL_FAIL_STATIC_ASSETS=nydus-static.tgz \
  PATH="$ARCH_AMD_BIN:$FAKE_BIN:$PATH" CURL_LOG="$ARCH_AMD_CURL" CURL_ASSET_DIR="$CURL_ASSET_DIR" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/arch-amd64.log" 2>&1
assert_contains "${WORK}/arch-amd64.log" 'pins: client v1.5.14 ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7'
assert_contains "${WORK}/arch-amd64.log" 'nydus v2.4.5 1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072'
assert_contains "${WORK}/arch-amd64.log" 'snapshotter v0.16.0 24e4ceaae9c87e396e71e89bd3eb074f2cc6a5a14b1a2081c8d890481857a0e4 (binary 9bd24308ef51139310bf58927a97eec49567d0154606bc5774ad69d2ee492ff0)'
assert_contains "$ARCH_AMD_CURL" 'http://static.example.test/client/v1.5.14/amd64/dfdaemon'
assert_contains "$ARCH_AMD_CURL" 'http://static.example.test/nydus/v2.4.5/amd64/nydus-static.tgz'

# Arm64 pin selection without downloads.
ROOT_ARCH_ARM_PINS="${WORK}/root-arch-arm64-pins"
env TARGET_ARCH= P2P_ROOT="$ROOT_ARCH_ARM_PINS" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  PATH="$ARCH_ARM_BIN:$FAKE_BIN:$PATH" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/arch-arm64-pins.log" 2>&1
assert_contains "${WORK}/arch-arm64-pins.log" 'host=arm64 client=aarch64 nydus=arm64 snapshotter=arm64'
assert_contains "${WORK}/arch-arm64-pins.log" 'pins: client v1.5.14 370ff792a1c6957a7244b770d6cd12792668e326b2282b3017fea2cbfa3510af'
assert_contains "${WORK}/arch-arm64-pins.log" 'nydus v2.4.5 85925af702d79fc3a8b9d501a5267ddc9f81449ff9fe94841845eb98eda84593'
assert_contains "${WORK}/arch-arm64-pins.log" 'snapshotter v0.16.0 136e9c649a63c16b3d3322a15fcdf918e7dda9a4cbb043e2923451e87a8ba08f (binary 399f7a5b800bb6d05c63bbb340e7fa1820b84609b882589a6a95dd8796d03859)'

# Non-default versions must use the supplied checksums, never the defaults.
ROOT_OVERRIDE="${WORK}/root-version-override"
OVERRIDE_SHA="1111111111111111111111111111111111111111111111111111111111111111"
env TARGET_ARCH=amd64 P2P_ROOT="$ROOT_OVERRIDE" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  DRAGONFLY_CLIENT_VERSION=v9.9.9 DRAGONFLY_CLIENT_SHA256="$OVERRIDE_SHA" \
  NYDUS_VERSION=v9.9.9 NYDUS_SHA256="$OVERRIDE_SHA" \
  NYDUS_SNAPSHOTTER_VERSION=v9.9.9 NYDUS_SNAPSHOTTER_SHA256="$OVERRIDE_SHA" NYDUS_SNAPSHOTTER_BINARY_SHA256="$OVERRIDE_SHA" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/version-override.log" 2>&1
assert_contains "${WORK}/version-override.log" "pins: client v9.9.9 ${OVERRIDE_SHA}"
assert_contains "${WORK}/version-override.log" "nydus v9.9.9 ${OVERRIDE_SHA}"
assert_contains "${WORK}/version-override.log" "snapshotter v9.9.9 ${OVERRIDE_SHA} (binary ${OVERRIDE_SHA})"
assert_not_contains "${WORK}/version-override.log" 'ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7'
assert_not_contains "${WORK}/version-override.log" '1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072'
assert_not_contains "${WORK}/version-override.log" '24e4ceaae9c87e396e71e89bd3eb074f2cc6a5a14b1a2081c8d890481857a0e4'
assert_not_contains "${WORK}/version-override.log" '9bd24308ef51139310bf58927a97eec49567d0154606bc5774ad69d2ee492ff0'

# A non-default version without its checksum fails before any download.
if env -u DRAGONFLY_CLIENT_SHA256 -u CLIENT_SHA256 TARGET_ARCH=amd64 P2P_ROOT="${WORK}/root-missing-client-sha" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  DRAGONFLY_CLIENT_VERSION=v9.9.9 \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/missing-client-sha.log" 2>&1; then
  echo "p2p-static self-test: non-default client without checksum did not fail" >&2
  exit 1
fi
assert_contains "${WORK}/missing-client-sha.log" 'DRAGONFLY_CLIENT_SHA256 is required for non-default dragonfly-client v9.9.9'

if env -u NYDUS_GITHUB_SHA256 -u NYDUS_SHA256 -u NYDUS_SHA256_AMD64 TARGET_ARCH=amd64 P2P_ROOT="${WORK}/root-missing-nydus-sha" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_VERSION=v9.9.9 \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/missing-nydus-sha.log" 2>&1; then
  echo "p2p-static self-test: non-default nydus without checksum did not fail" >&2
  exit 1
fi
assert_contains "${WORK}/missing-nydus-sha.log" 'NYDUS_GITHUB_SHA256 is required for non-default nydus v9.9.9'

if env -u NYDUS_SNAPSHOTTER_SHA256 -u NYDUS_SNAPSHOTTER_SHA256_AMD64 -u NYDUS_SNAPSHOTTER_BINARY_SHA256 -u NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64 \
  TARGET_ARCH=amd64 P2P_ROOT="${WORK}/root-missing-snapshotter-sha" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_SNAPSHOTTER_VERSION=v9.9.9 "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/missing-snapshotter-sha.log" 2>&1; then
  echo "p2p-static self-test: non-default snapshotter without tarball checksum did not fail" >&2
  exit 1
fi
assert_contains "${WORK}/missing-snapshotter-sha.log" 'NYDUS_SNAPSHOTTER_SHA256 is required for non-default nydus-snapshotter v9.9.9'

if env -u NYDUS_SNAPSHOTTER_BINARY_SHA256 -u NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64 \
  TARGET_ARCH=amd64 P2P_ROOT="${WORK}/root-missing-snapshotter-bin" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_SNAPSHOTTER_VERSION=v9.9.9 NYDUS_SNAPSHOTTER_SHA256="$OVERRIDE_SHA" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/missing-snapshotter-bin.log" 2>&1; then
  echo "p2p-static self-test: non-default snapshotter without binary checksum did not fail" >&2
  exit 1
fi
assert_contains "${WORK}/missing-snapshotter-bin.log" 'NYDUS_SNAPSHOTTER_BINARY_SHA256 is required for non-default nydus-snapshotter v9.9.9'

# Arm64 URL selection with the upstream arm64 snapshotter tarball. The
# checksums are overridden only to accept the tiny test artifacts.
ROOT_ARCH_ARM="${WORK}/root-arch-arm64"
ARCH_ARM_CURL="${WORK}/curl-arch-arm64.log"
: > "$ARCH_ARM_CURL"
env TARGET_ARCH= P2P_ROOT="$ROOT_ARCH_ARM" SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  NYDUS_GITHUB_SHA256="$RELEASE_NYDUS_SHA" NYDUS_ALLOW_GITHUB=0 P2P_ASSETS=0 \
  NYDUS_SNAPSHOTTER_SHA256="$RELEASE_SNAP_ARM_SHA" NYDUS_SNAPSHOTTER_BINARY_SHA256="$RELEASE_SNAP_ARM_BIN_SHA" \
  PATH="$ARCH_ARM_BIN:$FAKE_BIN:$PATH" CURL_LOG="$ARCH_ARM_CURL" CURL_ASSET_DIR="$CURL_ASSET_DIR" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/arch-arm64.log" 2>&1
assert_contains "${WORK}/arch-arm64.log" 'nydus-snapshotter: using'
assert_contains "$ARCH_ARM_CURL" 'http://static.example.test/client/v1.5.14/arm64/dfdaemon'
assert_contains "$ARCH_ARM_CURL" 'http://static.example.test/nydus/v2.4.5/arm64/nydus-static.tgz'
assert_contains "$ARCH_ARM_CURL" 'http://static.example.test/snapshotter/v0.16.0/arm64/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz'
if grep -q '/amd64/' "$ARCH_ARM_CURL"; then
  echo "p2p-static self-test: arm64 run fetched an amd64 asset" >&2
  exit 1
fi

ROOT_ARCH_V7="${WORK}/root-arch-armv7"
if env TARGET_ARCH= P2P_ROOT="$ROOT_ARCH_V7" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  PATH="$ARCH_V7_BIN:$FAKE_BIN:$PATH" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/arch-armv7.log" 2>&1; then
  echo "p2p-static self-test: armv7l architecture did not fail" >&2
  exit 1
fi
assert_contains "${WORK}/arch-armv7.log" 'unsupported architecture armv7l'
assert_contains "${WORK}/arch-armv7.log" 'no armv7/armhf Dragonfly client or nydus assets'

ROOT_ARCH_OVERRIDE="${WORK}/root-arch-override"
env TARGET_ARCH=arm64 P2P_ROOT="$ROOT_ARCH_OVERRIDE" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  PATH="$ARCH_AMD_BIN:$FAKE_BIN:$PATH" \
  "$RUNNER" "$INSTALL" "$CFG" > "${WORK}/arch-override.log" 2>&1
assert_contains "${WORK}/arch-override.log" 'target arm64: host=arm64 client=aarch64 nydus=arm64 snapshotter=arm64'

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
NO_IPS_CFG="$(payload "" "")"
P2P_ROOT="$ROOT_NO_IPS" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$NO_IPS_CFG" > "${WORK}/no-ips.log" 2>&1
assert_contains "${ROOT_NO_IPS}/etc/dragonfly/dynconfig.yaml" 'addrs: []'
for want in 'proxy:' 'blobs/sha256.*' 'manifests/sha256.*' 'schedulingPolicy: always'; do
  assert_contains "${ROOT_NO_IPS}/etc/dragonfly/dfget.yaml" "$want"
done
assert_not_contains "${WORK}/no-ips.log" 'direct-registry'
if [ -e "${ROOT_NO_IPS}/etc/dragonfly/scheduler-ips" ]; then
  echo "p2p-static self-test: empty scheduler IPs wrote the dfdaemon marker" >&2
  exit 1
fi
assert_contains "$INSTALL" 'ConditionPathExists=/etc/dragonfly/scheduler-ips'
assert_contains "$INSTALL" 'if [ -n "${SCHEDULER_IPS:-}" ]; then'

# First seed node: scheduler ID is still absent, so the operator injects the
# zero placeholder and the host must install without starting dfdaemon.
ROOT_NO_ID="${WORK}/root-no-id"
NO_ID_CFG="$(payload "" "" 0)"
P2P_ROOT="$ROOT_NO_ID" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  "$RUNNER" "$INSTALL" "$NO_ID_CFG" > "${WORK}/no-id.log" 2>&1
assert_contains "${ROOT_NO_ID}/etc/dragonfly/dfget.yaml" 'schedulerClusterID: 0'
assert_contains "${ROOT_NO_ID}/etc/dragonfly/dynconfig.yaml" 'addrs: []'
if [ -e "${ROOT_NO_ID}/etc/dragonfly/scheduler-ips" ]; then
  echo "p2p-static self-test: zero placeholder wrote the dfdaemon marker" >&2
  exit 1
fi

# P2P_DISTRO wins over the filesystem when no k3s directory exists.
ROOT_DISTRO="${WORK}/root-distro"
DISTRO_CFG="$(payload "" "")"
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
