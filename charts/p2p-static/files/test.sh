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
  local ca cert key env host mirror
  host="${1:-scheduler.example.test}"
  mirror="${2:-}"
  ca="$(printf 'dummy-ca\n' | base64 | tr -d '\n')"
  cert="$(printf 'dummy-cert\n' | base64 | tr -d '\n')"
  key="$(printf 'dummy-key\n' | base64 | tr -d '\n')"
  env="$(printf 'MANAGER_GRPC=https://manager-grpc.example.test:65003\nSTATIC_URL=http://static.example.test\nSCHEDULER_HOST=%s\nSCHEDULER_CLUSTER_ID=2\nCA_CRT_B64=%s\nCLIENT_CRT_B64=%s\nCLIENT_KEY_B64=%s\n' "$host" "$ca" "$cert" "$key")"
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
assert_not_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'manager-grpc.example.test'
assert_contains "${ROOT_ONE}/etc/dragonfly/dfget.yaml" 'tlsVerify: true'
assert_contains "${ROOT_ONE}/etc/dragonfly/dynconfig.yaml" 'addr: "scheduler.example.test:443"'
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
assert_contains "${ROOT_ONE}/etc/nydus/config.toml" 'containerd/certs.d'
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" 'enable_tarfs'
assert_not_contains "${ROOT_ONE}/etc/nydus/config.toml" 'mirrors_config'
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
assert_not_contains "${ROOT_ONE}/usr/local/bin/nydus-updater" 'containerd-nydus-grpc'
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

ROOT_MIRROR="${WORK}/root-mirror"
MIRROR_CFG="$(payload scheduler.example.test https://proxy.example.test/docker.io/)"
P2P_ROOT="$ROOT_MIRROR" SKIP_DOWNLOAD=1 SKIP_MODULES=1 SKIP_SYSTEMD=1 SKIP_PREFLIGHT=1 CONTAINERD_MAJOR=1 \
  bash "$INSTALL" "$MIRROR_CFG" > "${WORK}/mirror.log" 2>&1
for mirror_host in docker.io index.docker.io _default; do
  MIRROR_FILE="${ROOT_MIRROR}/etc/nydus/mirrors/${mirror_host}/hosts.toml"
  assert_file "$MIRROR_FILE"
  assert_contains "$MIRROR_FILE" 'server = "https://proxy.example.test/docker.io"'
  assert_contains "$MIRROR_FILE" '[host."https://proxy.example.test/docker.io"]'
  assert_count "$MIRROR_FILE" 'server =' 1
  assert_not_contains "$MIRROR_FILE" 'dummy-'
done
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
  bash "$INSTALL" "$(payload scheduler.example.test ftp://proxy.example.test/docker.io)" > "${WORK}/bad-mirror.log" 2>&1; then
  echo "p2p-static self-test: invalid nydus mirror URL was accepted" >&2
  exit 1
fi
assert_contains "${WORK}/bad-mirror.log" 'nydus_mirror_url must use http:// or https://'

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
  bash "$INSTALL" "$CFG" > "${WORK}/release.log" 2>&1

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
  bash "$INSTALL" "$CFG" > "${WORK}/no-asset.log" 2>&1
assert_contains "${WORK}/no-asset.log" 'nydus-snapshotter release asset'
assert_contains "${WORK}/no-asset.log" 'is unavailable'
if [ -f "${ROOT_NO_ASSET}/usr/local/bin/containerd-nydus-grpc" ]; then
  echo "p2p-static self-test: missing release asset installed a snapshotter" >&2
  exit 1
fi
assert_file "${ROOT_NO_ASSET}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl"
assert_contains "${ROOT_NO_ASSET}/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl" 'snapshotter = "overlayfs"'

echo "p2p-static self-test: OK"
