# Bootstrap a new RKE2/k3s node before the Kubernetes services start.
set -Eeuo pipefail
IFS=$'\n\t'

P2P_ROOT="${P2P_ROOT:-/}"
REAL_ROOT=0
if [ "$P2P_ROOT" = "/" ]; then
  REAL_ROOT=1
fi
ROOT="${P2P_ROOT%/}"

# The operator supplies P2P_DISTRO from the provisioning Cluster version; the
# filesystem check is the fallback for manual runs on an already-installed node.
DISTRO="${P2P_DISTRO:-}"
if [ -z "$DISTRO" ]; then
  DISTRO="rke2"
  if [ -d "${ROOT}/var/lib/rancher/k3s" ]; then
    DISTRO="k3s"
  fi
fi
case "$DISTRO" in
  k3s|rke2) ;;
  *) DISTRO="k3s" ;;
esac
DISTRO_SERVICE_TARGETS="rke2-server.service rke2-agent.service"
if [ "$DISTRO" = "k3s" ]; then
  DISTRO_SERVICE_TARGETS="k3s.service"
fi

CERT_DIR="/etc/dragonfly/certs"
DFGET_CONFIG="/etc/dragonfly/dfget.yaml"
DYNCONFIG="/etc/dragonfly/dynconfig.yaml"
NYDUS_DIR="/etc/nydus"
# Host-sync writes versions.env before a later block re-run. It is authoritative
# for live-converged versions/checksums; bootstrap env remains the first source.
if [ -f "${ROOT}${NYDUS_DIR}/versions.env" ]; then
  # shellcheck disable=SC1090
  . "${ROOT}${NYDUS_DIR}/versions.env"
fi
# Defaults live in the operator chart values; these constants only identify
# when the per-architecture checksum fallbacks below still apply.
DEFAULT_DRAGONFLY_CLIENT_VERSION="v1.5.14"
DEFAULT_NYDUS_VERSION="v2.4.5"
DEFAULT_NYDUS_SNAPSHOTTER_VERSION="v0.16.0"
# Static-store assets for nydusd/nydus-image/nydus-overlayfs. The host
# snapshotter is the upstream containerd/nydus-snapshotter release, see below.
NYDUS_VERSION="${NYDUS_VERSION:-${DEFAULT_NYDUS_VERSION}}"
NYDUS_BINARIES="nydusd
nydus-image
nydus-overlayfs"
# Upstream nydus-snapshotter release. v0.16.0 publishes a tarball containing
# bin/containerd-nydus-grpc for amd64 and arm64. Upstream reads mirrors only
# from [remote.mirrors_config] (certs.d-style hosts.toml under its dir); it
# does not implicitly read containerd's certs.d. Installed to the existing
# /usr/local/bin/containerd-nydus-grpc path and run by the same unit.
NYDUS_SNAPSHOTTER_REPO="${NYDUS_SNAPSHOTTER_REPO:-containerd/nydus-snapshotter}"
NYDUS_SNAPSHOTTER_VERSION="${NYDUS_SNAPSHOTTER_VERSION:-${DEFAULT_NYDUS_SNAPSHOTTER_VERSION}}"
NYDUS_SNAPSHOTTER_BINARY="containerd-nydus-grpc"
# Optional official Nydus release fallback for the daemon binaries when the
# static store is not populated. The tarball contains nydusd, nydus-image, and
# nydus-overlayfs.
NYDUS_ALLOW_GITHUB="${NYDUS_ALLOW_GITHUB:-1}"
NYDUS_GITHUB_REPO="${NYDUS_GITHUB_REPO:-${NYDUS_RELEASE_REPO:-dragonflyoss/nydus}}"
# Direct Dragonfly client tarball for dfdaemon/dfget. The first delivery must
# stay direct: dfget cannot use dfdaemon before dfdaemon exists.
DRAGONFLY_CLIENT_REPO="${DRAGONFLY_CLIENT_REPO:-${CLIENT_RELEASE_REPO:-dragonflyoss/client}}"
DRAGONFLY_CLIENT_VERSION="${DRAGONFLY_CLIENT_VERSION:-${CLIENT_VERSION:-${DEFAULT_DRAGONFLY_CLIENT_VERSION}}}"
DRAGONFLY_CLIENT_LIBC="${DRAGONFLY_CLIENT_LIBC:-gnu}"
DRAGONFLY_CLIENT_BINARIES="dfdaemon
dfget"
# Optional HTTPS mirror path for nydus blobs, for example
# https://proxy.example.test/docker.io. Empty keeps the dfdaemon proxy block in
# the generated nydusd backend; setting it makes nydusd fetch blobs directly
# from the mirror path and removes that proxy block.
NYDUS_MIRROR_DIR="/etc/nydus/mirrors"
NYDUS_MIRROR_URL="${NYDUS_MIRROR_URL:-}"
# Both nydusd configs are installed. /usr/local/bin/nydus-fs-select runs as
# ExecStartPre on every snapshotter start and picks fscache or fusedev from the
# running kernel and the fscache/cachefiles prerequisites. The installer does
# not pin the mode, so a kernel change is picked up without rerunning it.
SCHEDULER_PORT=8002
PREFLIGHT_TIMEOUT="${P2P_PREFLIGHT_TIMEOUT:-3}"
BIN_DIR="${ROOT}/usr/local/bin"
# P2P_ASSETS: auto (default) enables P2P asset fetch only with scheduler IPs
# and dfget; 1 forces it when prerequisites exist; 0 disables it.
P2P_ASSETS="${P2P_ASSETS:-auto}"
P2P_ASSETS_READY_TIMEOUT="${P2P_ASSETS_READY_TIMEOUT:-60}"
P2P_DFDAEMON_SOCKET="${P2P_DFDAEMON_SOCKET:-/var/run/dragonfly/dfdaemon.sock}"
P2P_DFGET_TIMEOUT="${P2P_DFGET_TIMEOUT:-10m}"
DFGET_BIN="${DFGET_BIN:-${BIN_DIR}/dfget}"
SERVICE_DIR="${ROOT}/etc/systemd/system"
RKE2_CONTAINERD_DIR="${ROOT}/var/lib/rancher/${DISTRO}/agent/etc/containerd"
RKE2_CONTAINERD_STATE_DIR="${ROOT}/var/lib/rancher/${DISTRO}/agent/containerd"
NYDUS_DROPIN_DIR="${RKE2_CONTAINERD_DIR}/config-v3.toml.d"
NYDUS_DROPIN="${NYDUS_DROPIN_DIR}/90-nydus.toml"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

log() {
  printf 'p2p-install: %s\n' "$*" >&2
}

die() {
  log "ERROR: $*"
  exit 1
}

write_file() {
  local path mode dir tmp
  path="$1"
  mode="$2"
  dir="$(dirname "$path")"
  mkdir -p "$dir"
  tmp="${path}.tmp.$$"
  cat > "$tmp"
  chmod "$mode" "$tmp"
  if [ -f "$path" ] && cmp -s "$tmp" "$path"; then
    rm -f "$tmp"
    chmod "$mode" "$path"
  else
    mv -f "$tmp" "$path"
  fi
}

# Match the RKE2/k3s node architecture instead of assuming amd64. armv7/armhf
# are called out explicitly because neither the Rust client nor the nydus
# static release publishes an armv7 asset.
TARGET_ARCH="${TARGET_ARCH:-$(uname -m)}"
case "$TARGET_ARCH" in
  x86_64|amd64)
    HOST_ARCH="amd64"
    DRAGONFLY_CLIENT_ARCH_DEFAULT="x86_64"
    NYDUS_ARCH="amd64"
    ;;
  aarch64|arm64)
    HOST_ARCH="arm64"
    DRAGONFLY_CLIENT_ARCH_DEFAULT="aarch64"
    NYDUS_ARCH="arm64"
    ;;
  armv7l|armhf|armv6l)
    die "unsupported architecture ${TARGET_ARCH}: no armv7/armhf Dragonfly client or nydus assets; supported architectures are x86_64/amd64 and aarch64/arm64"
    ;;
  *)
    die "unsupported architecture ${TARGET_ARCH}: supported architectures are x86_64/amd64 and aarch64/arm64"
    ;;
esac
DRAGONFLY_CLIENT_ARCH="${DRAGONFLY_CLIENT_ARCH:-${DRAGONFLY_CLIENT_ARCH_DEFAULT}}"
DRAGONFLY_CLIENT_ROOT="${DRAGONFLY_CLIENT_ROOT:-dragonfly-client-${DRAGONFLY_CLIENT_VERSION}-${DRAGONFLY_CLIENT_ARCH}-unknown-linux-${DRAGONFLY_CLIENT_LIBC}}"
DRAGONFLY_CLIENT_ASSET="${DRAGONFLY_CLIENT_ASSET:-${DRAGONFLY_CLIENT_ROOT}.tar.gz}"
DRAGONFLY_CLIENT_URL="${DRAGONFLY_CLIENT_URL:-https://github.com/${DRAGONFLY_CLIENT_REPO}/releases/download/${DRAGONFLY_CLIENT_VERSION}/${DRAGONFLY_CLIENT_ASSET}}"
NYDUS_SNAPSHOTTER_ARCH="$HOST_ARCH"
case "$HOST_ARCH" in
  amd64)
    : "${CLIENT_SHA256:=${CLIENT_SHA256_AMD64:-}}"
    : "${NYDUS_SHA256:=${NYDUS_SHA256_AMD64:-}}"
    : "${NYDUS_SNAPSHOTTER_SHA256:=${NYDUS_SNAPSHOTTER_SHA256_AMD64:-}}"
    : "${NYDUS_SNAPSHOTTER_BINARY_SHA256:=${NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64:-}}"
    ;;
  arm64)
    : "${CLIENT_SHA256:=${CLIENT_SHA256_ARM64:-}}"
    : "${NYDUS_SHA256:=${NYDUS_SHA256_ARM64:-}}"
    : "${NYDUS_SNAPSHOTTER_SHA256:=${NYDUS_SNAPSHOTTER_SHA256_ARM64:-}}"
    : "${NYDUS_SNAPSHOTTER_BINARY_SHA256:=${NYDUS_SNAPSHOTTER_BINARY_SHA256_ARM64:-}}"
    ;;
esac
if [ -n "${CLIENT_SHA256:-}" ]; then
  DRAGONFLY_CLIENT_SHA256="${DRAGONFLY_CLIENT_SHA256:-${CLIENT_SHA256}}"
fi
if [ -n "${NYDUS_SHA256:-}" ]; then
  NYDUS_GITHUB_SHA256="${NYDUS_GITHUB_SHA256:-${NYDUS_SHA256}}"
fi
NYDUS_SNAPSHOTTER_ASSET="${NYDUS_SNAPSHOTTER_ASSET:-nydus-snapshotter-${NYDUS_SNAPSHOTTER_VERSION}-linux-${NYDUS_SNAPSHOTTER_ARCH}.tar.gz}"

# Per-architecture SHA-256 pins are only a last-resort fallback for the
# versions shipped in the operator chart. Any other version must provide its
# checksums through the environment (the operator/host-sync does).
case "$HOST_ARCH" in
  amd64)
    if [ "$DRAGONFLY_CLIENT_VERSION" = "$DEFAULT_DRAGONFLY_CLIENT_VERSION" ]; then
      : "${DRAGONFLY_CLIENT_SHA256:=ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7}"
    fi
    if [ "$NYDUS_VERSION" = "$DEFAULT_NYDUS_VERSION" ]; then
      : "${NYDUS_GITHUB_SHA256:=1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072}"
    fi
    if [ "$NYDUS_SNAPSHOTTER_VERSION" = "$DEFAULT_NYDUS_SNAPSHOTTER_VERSION" ]; then
      : "${NYDUS_SNAPSHOTTER_SHA256:=24e4ceaae9c87e396e71e89bd3eb074f2cc6a5a14b1a2081c8d890481857a0e4}"
      : "${NYDUS_SNAPSHOTTER_BINARY_SHA256:=9bd24308ef51139310bf58927a97eec49567d0154606bc5774ad69d2ee492ff0}"
    fi
    ;;
  arm64)
    if [ "$DRAGONFLY_CLIENT_VERSION" = "$DEFAULT_DRAGONFLY_CLIENT_VERSION" ]; then
      : "${DRAGONFLY_CLIENT_SHA256:=370ff792a1c6957a7244b770d6cd12792668e326b2282b3017fea2cbfa3510af}"
    fi
    if [ "$NYDUS_VERSION" = "$DEFAULT_NYDUS_VERSION" ]; then
      : "${NYDUS_GITHUB_SHA256:=85925af702d79fc3a8b9d501a5267ddc9f81449ff9fe94841845eb98eda84593}"
    fi
    if [ "$NYDUS_SNAPSHOTTER_VERSION" = "$DEFAULT_NYDUS_SNAPSHOTTER_VERSION" ]; then
      : "${NYDUS_SNAPSHOTTER_SHA256:=136e9c649a63c16b3d3322a15fcdf918e7dda9a4cbb043e2923451e87a8ba08f}"
      : "${NYDUS_SNAPSHOTTER_BINARY_SHA256:=399f7a5b800bb6d05c63bbb340e7fa1820b84609b882589a6a95dd8796d03859}"
    fi
    ;;
esac
if [ "$DRAGONFLY_CLIENT_VERSION" != "$DEFAULT_DRAGONFLY_CLIENT_VERSION" ] && [ -z "${DRAGONFLY_CLIENT_SHA256:-}" ]; then
  die "DRAGONFLY_CLIENT_SHA256 is required for non-default dragonfly-client ${DRAGONFLY_CLIENT_VERSION}"
fi
if [ "$NYDUS_VERSION" != "$DEFAULT_NYDUS_VERSION" ] && [ -z "${NYDUS_GITHUB_SHA256:-}" ]; then
  die "NYDUS_GITHUB_SHA256 is required for non-default nydus ${NYDUS_VERSION}"
fi
if [ "$NYDUS_SNAPSHOTTER_VERSION" != "$DEFAULT_NYDUS_SNAPSHOTTER_VERSION" ]; then
  [ -n "${NYDUS_SNAPSHOTTER_SHA256:-}" ] || die "NYDUS_SNAPSHOTTER_SHA256 is required for non-default nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION}"
  [ -n "${NYDUS_SNAPSHOTTER_BINARY_SHA256:-}" ] || die "NYDUS_SNAPSHOTTER_BINARY_SHA256 is required for non-default nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION}"
fi

log "target ${TARGET_ARCH}: host=${HOST_ARCH} client=${DRAGONFLY_CLIENT_ARCH} nydus=${NYDUS_ARCH} snapshotter=${NYDUS_SNAPSHOTTER_ARCH}"
log "pins: client ${DRAGONFLY_CLIENT_VERSION} ${DRAGONFLY_CLIENT_SHA256}, nydus ${NYDUS_VERSION} ${NYDUS_GITHUB_SHA256}, snapshotter ${NYDUS_SNAPSHOTTER_VERSION} ${NYDUS_SNAPSHOTTER_SHA256:-unavailable} (binary ${NYDUS_SNAPSHOTTER_BINARY_SHA256:-unavailable})"

detect_containerd_major() {
  local candidate version
  candidate="${ROOT}/var/lib/rancher/${DISTRO}/bin/containerd"
  if [ ! -x "$candidate" ]; then
    candidate="$(command -v containerd || true)"
  fi
  if [ -n "$candidate" ] && [ -x "$candidate" ]; then
    version="$("$candidate" --version 2>/dev/null || true)"
    case " $version " in
      *" v2."*|*" 2."*) printf '2'; return ;;
      *) printf '1'; return ;;
    esac
  fi
  # New VMs usually run this script before RKE2/k3s installs containerd. The
  # target releases use containerd 2.x, so prefer the v3 drop-in path.
  log "warning: containerd version not detected; using the containerd 2.x v3 config"
  printf '2'
}

# Direct asset helpers. The first dfdaemon/dfget delivery is always direct;
# P2P is only used for the nydus/snapshotter payloads after that.
sha256_of() {
  local sum
  sum="$(sha256sum "$1")"
  printf '%s' "${sum%% *}"
}

verify_sha256() {
  local file expected label actual
  file="$1"
  expected="$2"
  label="$3"
  command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required to verify ${label}"
  [ -n "$expected" ] || die "missing SHA256 for ${label}"
  actual="$(sha256_of "$file")"
  if [ "$actual" != "$expected" ]; then
    log "checksum mismatch for ${label}: got ${actual} want ${expected}"
    return 1
  fi
  return 0
}

# Prefer the release-provided checksum sidecar (.sha256sum or .sha256). The
# caller's pinned fallback is used only when the release publishes no checksum
# asset (the Dragonfly client v1.5.14 release has none).
resolve_checksum() {
  local url fallback suffix sum_file sum
  url="$1"
  fallback="${2:-}"
  # Dragonfly client releases publish no checksum sidecar; skip the 404 probes.
  case "$url" in
    *dragonfly-client*)
      if [ -n "$fallback" ]; then
        printf '%s' "$fallback"
        return 0
      fi
      ;;
  esac
  for suffix in .sha256sum .sha256; do
    sum_file="${TMP}/checksum.$$"
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 60 -o "$sum_file" "${url}${suffix}" && [ -s "$sum_file" ]; then
      IFS=' ' read -r sum _ < "$sum_file"
      rm -f "$sum_file"
      if [ -n "$sum" ]; then
        printf '%s' "$sum"
        return 0
      fi
    fi
    rm -f "$sum_file"
  done
  if [ -n "$fallback" ]; then
    printf '%s' "$fallback"
    return 0
  fi
  return 1
}

nydus_github_tarball_url() {
  local base="https://github.com/${NYDUS_GITHUB_REPO}/releases"
  if [ "$NYDUS_VERSION" = "latest" ]; then
    printf '%s/latest/download/nydus-static-latest-linux-%s.tgz' "$base" "$NYDUS_ARCH"
  else
    printf '%s/download/%s/nydus-static-%s-linux-%s.tgz' "$base" "$NYDUS_VERSION" "$NYDUS_VERSION" "$NYDUS_ARCH"
  fi
}

DRAGONFLY_CLIENT_DIR=""

ensure_dragonfly_client_assets() {
  local dir tarball
  if [ -n "$DRAGONFLY_CLIENT_DIR" ]; then
    return 0
  fi
  dir="${TMP}/dragonfly-client"
  tarball="${dir}/${DRAGONFLY_CLIENT_ASSET}"
  mkdir -p "$dir"
  log "downloading direct Dragonfly client ${DRAGONFLY_CLIENT_VERSION}: ${DRAGONFLY_CLIENT_URL}"
  if ! curl -fL --retry 3 --connect-timeout 10 --max-time 300 -o "$tarball" "$DRAGONFLY_CLIENT_URL"; then
    log "Dragonfly client tarball unavailable: ${DRAGONFLY_CLIENT_URL}"
    return 1
  fi
  resolved="$(resolve_checksum "$DRAGONFLY_CLIENT_URL" "$DRAGONFLY_CLIENT_SHA256")" \
    || die "no checksum available for dragonfly-client ${DRAGONFLY_CLIENT_VERSION}: ${DRAGONFLY_CLIENT_URL}"
  verify_sha256 "$tarball" "$resolved" "dragonfly-client ${DRAGONFLY_CLIENT_VERSION}" \
    || die "direct client tarball checksum verification failed: ${DRAGONFLY_CLIENT_URL}"
  if ! tar -xzf "$tarball" -C "$dir"; then
    log "invalid Dragonfly client tarball: ${DRAGONFLY_CLIENT_URL}"
    return 1
  fi
  if [ ! -d "${dir}/${DRAGONFLY_CLIENT_ROOT}" ]; then
    log "Dragonfly client tarball has no ${DRAGONFLY_CLIENT_ROOT} directory"
    return 1
  fi
  DRAGONFLY_CLIENT_DIR="${dir}/${DRAGONFLY_CLIENT_ROOT}"
}

# dfdaemon/dfget come from the static store or the pinned client tarball. Both
# are direct; using dfget here would be the chicken-and-egg case.
install_direct_client_binary() {
  local name out url
  name="$1"
  out="$2"
  for url in \
    "${STATIC_URL%/}/client/${DRAGONFLY_CLIENT_VERSION}/${HOST_ARCH}/${name}" \
    "${STATIC_URL%/}/bin/${name}" \
    "${STATIC_URL%/}/nydus/${NYDUS_VERSION}/${name}"; do
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 300 -o "$out" "$url" && [ -s "$out" ]; then
      chmod 0755 "$out"
      log "direct client ${name}: using static store ${url}"
      return 0
    fi
  done
  rm -f "$out"
  if ensure_dragonfly_client_assets && [ -s "${DRAGONFLY_CLIENT_DIR}/${name}" ]; then
    cp "${DRAGONFLY_CLIENT_DIR}/${name}" "$out"
    chmod 0755 "$out"
    log "direct client ${name}: using client tarball ${DRAGONFLY_CLIENT_VERSION}"
    return 0
  fi
  return 1
}

install_direct_client_binaries() {
  local binary tmp
  for binary in $DRAGONFLY_CLIENT_BINARIES; do
    tmp="${BIN_DIR}/.${binary}.tmp.$$"
    if install_direct_client_binary "$binary" "$tmp"; then
      mv -f "$tmp" "${BIN_DIR}/${binary}"
      continue
    fi
    rm -f "$tmp"
    if [ "$binary" = "dfdaemon" ]; then
      fallback_direct "failed to install dfdaemon ${DRAGONFLY_CLIENT_VERSION}"
    fi
    log "warning: dfget ${DRAGONFLY_CLIENT_VERSION} is unavailable; P2P asset fetch will be disabled"
  done
}

p2p_assets_requested() {
  case "${P2P_ASSETS:-auto}" in
    0|false|no|off)
      return 1
      ;;
    1|true|yes|on|auto|"")
      return 0
      ;;
    *)
      log "warning: invalid P2P_ASSETS=${P2P_ASSETS}; treating as auto"
      P2P_ASSETS="auto"
      return 0
      ;;
  esac
}

p2p_assets_available() {
  p2p_assets_requested || return 1
  if [ -z "${SCHEDULER_IPS:-}" ]; then
    [ "${P2P_ASSETS}" = "1" ] && log "P2P_ASSETS=1 but SCHEDULER_IPS is empty; using direct asset downloads"
    return 1
  fi
  if [ ! -x "$DFGET_BIN" ]; then
    [ "${P2P_ASSETS}" = "1" ] && log "P2P_ASSETS=1 but dfget is missing at ${DFGET_BIN}; using direct asset downloads"
    return 1
  fi
  return 0
}

p2p_fetch() {
  local url out digest
  local -a args
  url="$1"
  out="$2"
  digest="${3:-}"
  [ -x "$DFGET_BIN" ] || return 1
  args=(--scheduling-policy always --endpoint "$P2P_DFDAEMON_SOCKET" --timeout "$P2P_DFGET_TIMEOUT")
  if [ -n "$digest" ]; then
    case "$digest" in
      sha256:*) ;;
      *) digest="sha256:${digest}" ;;
    esac
    args+=(--digest "$digest")
  fi
  log "P2P fetch: ${url}"
  if "$DFGET_BIN" "${args[@]}" "$url" -O "$out" && [ -s "$out" ]; then
    log "P2P fetch ok: ${url}"
    return 0
  fi
  log "P2P fetch failed; using direct fallback: ${url}"
  rm -f "$out"
  return 1
}

wait_for_dfdaemon_socket() {
  local waited=0
  while [ "$waited" -lt "$P2P_ASSETS_READY_TIMEOUT" ]; do
    if [ -S "$P2P_DFDAEMON_SOCKET" ]; then
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  return 1
}

# Start the already-installed first dfdaemon so dfget can fetch the remaining
# assets through P2P. A missing/timed-out socket is not fatal: the caller
# switches to direct downloads.
start_dfdaemon_for_assets() {
  if [ "$REAL_ROOT" != "1" ] || [ "${SKIP_SYSTEMD:-0}" = "1" ]; then
    log "systemd is disabled; assuming the pre-existing dfdaemon socket is usable for P2P asset fetch"
    return 0
  fi
  command -v systemctl >/dev/null 2>&1 || {
    log "systemctl is unavailable; using direct asset downloads"
    return 1
  }
  systemctl daemon-reload || {
    log "systemctl daemon-reload failed; using direct asset downloads"
    return 1
  }
  if ! systemctl start dfdaemon-bootstrap.service; then
    log "dfdaemon-bootstrap.service did not start; using direct asset downloads"
    return 1
  fi
  if wait_for_dfdaemon_socket; then
    log "dfdaemon socket ready for P2P asset fetch: ${P2P_DFDAEMON_SOCKET}"
    return 0
  fi
  log "dfdaemon socket ${P2P_DFDAEMON_SOCKET} was not ready after ${P2P_ASSETS_READY_TIMEOUT}s; falling back to direct asset downloads"
  systemctl stop dfdaemon-bootstrap.service >/dev/null 2>&1 || true
  return 1
}

# Prefer the pinned tarball over P2P, then the static-store mirror, then the
# GitHub release. Every source is checked against NYDUS_GITHUB_SHA256.
download_nydus_tarball() {
  local p2p_ready="$1" out="$2" github_url candidate checksum
  github_url="$(nydus_github_tarball_url)"
  checksum="$(resolve_checksum "$github_url" "$NYDUS_GITHUB_SHA256")" \
    || die "no checksum available for nydus ${NYDUS_VERSION}: ${github_url}"
  rm -f "$out"
  if [ "$p2p_ready" = "1" ] && p2p_fetch "$github_url" "$out" "$checksum"; then
    if verify_sha256 "$out" "$checksum" "nydus ${NYDUS_VERSION} (P2P)"; then
      log "nydus tarball: using P2P"
      return 0
    fi
    log "nydus P2P tarball failed checksum verification; discarding"
    rm -f "$out"
  fi
  for candidate in \
    "${STATIC_URL%/}/nydus/${NYDUS_VERSION}/${HOST_ARCH}/nydus-static.tgz" \
    "${STATIC_URL%/}/nydus/${NYDUS_VERSION}/nydus-static.tgz" \
    "${STATIC_URL%/}/bin/nydus-static.tgz"; do
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 300 -o "$out" "$candidate" && [ -s "$out" ]; then
      if verify_sha256 "$out" "$checksum" "nydus ${NYDUS_VERSION} (static store)"; then
        log "nydus tarball: using static store ${candidate}"
        return 0
      fi
      die "checksum verification failed for ${candidate}"
    fi
  done
  if [ "${NYDUS_ALLOW_GITHUB:-1}" != "1" ]; then
    log "nydus static store unavailable and NYDUS_ALLOW_GITHUB=0; not trying GitHub"
    return 1
  fi
  log "nydus static store unavailable; trying GitHub release ${github_url}"
  if ! curl -fL --retry 3 --connect-timeout 10 --max-time 300 -o "$out" "$github_url"; then
    log "nydus GitHub release unavailable: ${github_url}"
    return 1
  fi
  verify_sha256 "$out" "$checksum" "nydus ${NYDUS_VERSION} (GitHub)" \
    || die "checksum verification failed for ${github_url}"
  log "nydus tarball: using GitHub release"
  return 0
}

install_nydus_assets() {
  local p2p_ready="$1" binary tmp src
  if ! download_nydus_tarball "$p2p_ready" "${TMP}/nydus-static.tgz"; then
    return 1
  fi
  rm -rf "${TMP}/nydus-extract"
  mkdir -p "${TMP}/nydus-extract"
  if ! tar -xzf "${TMP}/nydus-static.tgz" -C "${TMP}/nydus-extract"; then
    log "nydus tarball is invalid"
    return 1
  fi
  if [ ! -d "${TMP}/nydus-extract/nydus-static" ]; then
    log "nydus tarball has no nydus-static directory"
    return 1
  fi
  for binary in $NYDUS_BINARIES; do
    src="${TMP}/nydus-extract/nydus-static/${binary}"
    if [ ! -s "$src" ]; then
      log "nydus tarball is missing ${binary}"
      return 1
    fi
    tmp="${BIN_DIR}/.${binary}.tmp.$$"
    cp "$src" "$tmp"
    chmod 0755 "$tmp"
    mv -f "$tmp" "${BIN_DIR}/${binary}"
  done
}

nydus_snapshotter_release_url() {
  local base="https://github.com/${NYDUS_SNAPSHOTTER_REPO}/releases"
  if [ "$NYDUS_SNAPSHOTTER_VERSION" = "latest" ]; then
    printf '%s/latest/download/%s' "$base" "$NYDUS_SNAPSHOTTER_ASSET"
  else
    printf '%s/download/%s/%s' "$base" "$NYDUS_SNAPSHOTTER_VERSION" "$NYDUS_SNAPSHOTTER_ASSET"
  fi
}

# Upstream v0.16.0 publishes a tarball with bin/containerd-nydus-grpc. Verify
# both the tarball and the extracted binary: the binary pin is the reference
# already running on the 236 node (9bd24308... on amd64).
extract_nydus_snapshotter_binary() {
  local tarball="$1" out="$2" extract
  extract="${TMP}/snapshotter-extract-$$"
  rm -rf "$extract"
  mkdir -p "$extract"
  if ! tar -xzf "$tarball" -C "$extract"; then
    log "invalid nydus-snapshotter tarball: ${tarball}"
    rm -rf "$extract"
    return 1
  fi
  if [ ! -s "${extract}/bin/${NYDUS_SNAPSHOTTER_BINARY}" ]; then
    log "nydus-snapshotter tarball is missing bin/${NYDUS_SNAPSHOTTER_BINARY}: ${tarball}"
    rm -rf "$extract"
    return 1
  fi
  cp "${extract}/bin/${NYDUS_SNAPSHOTTER_BINARY}" "$out"
  chmod 0755 "$out"
  rm -rf "$extract"
  if [ -n "${NYDUS_SNAPSHOTTER_BINARY_SHA256:-}" ]; then
    if ! verify_sha256 "$out" "$NYDUS_SNAPSHOTTER_BINARY_SHA256" "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} binary"; then
      rm -f "$out"
      return 1
    fi
  fi
  return 0
}

# Fetch the upstream tarball through P2P, static store, then GitHub; verify the
# tarball SHA-256 and the extracted binary SHA-256 before install.
install_nydus_snapshotter_asset() {
  local p2p_ready="$1" out="$2" url expected candidate tarball
  url="$(nydus_snapshotter_release_url)"
  tarball="${out}.tgz"
  rm -f "$out" "$tarball"
  expected="$(resolve_checksum "$url" "${NYDUS_SNAPSHOTTER_SHA256:-}")" \
    || { log "no checksum available for nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} ${HOST_ARCH}: ${url}"; return 1; }
  if [ "$p2p_ready" = "1" ] && p2p_fetch "$url" "$tarball" "$expected"; then
    if verify_sha256 "$tarball" "$expected" "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} tarball (P2P)" &&
      extract_nydus_snapshotter_binary "$tarball" "$out"; then
      log "nydus-snapshotter: using P2P"
      rm -f "$tarball"
      return 0
    fi
    log "nydus P2P snapshotter failed verification; discarding"
    rm -f "$tarball" "$out"
  fi
  for candidate in \
    "${STATIC_URL%/}/snapshotter/${NYDUS_SNAPSHOTTER_VERSION}/${HOST_ARCH}/${NYDUS_SNAPSHOTTER_ASSET}" \
    "${STATIC_URL%/}/bin/${NYDUS_SNAPSHOTTER_ASSET}" \
    "$url"; do
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 300 -o "$tarball" "$candidate" && [ -s "$tarball" ]; then
      if ! verify_sha256 "$tarball" "$expected" "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} tarball"; then
        rm -f "$tarball"
        die "checksum verification failed for ${candidate}"
      fi
      if extract_nydus_snapshotter_binary "$tarball" "$out"; then
        log "nydus-snapshotter: using ${candidate}"
        rm -f "$tarball"
        return 0
      fi
      rm -f "$tarball"
      die "nydus-snapshotter tarball failed binary verification: ${candidate}"
    fi
  done
  rm -f "$tarball"
  return 1
}

for key in MANAGER_GRPC STATIC_URL SCHEDULER_CLUSTER_ID CA_CRT_B64 CLIENT_CRT_B64 CLIENT_KEY_B64; do
  if [ -z "${!key:-}" ]; then
    die "bootstrap env is missing $key"
  fi
done

case "$MANAGER_GRPC" in
  https://*) ;;
  *) die "manager_grpc must use https://" ;;
esac
MANAGER_ADDR="${MANAGER_GRPC#*://}"
MANAGER_ADDR="${MANAGER_ADDR%%/*}"
case "$MANAGER_ADDR" in
  ""|*[!A-Za-z0-9.:\[\]-]*) die "manager_grpc has an invalid host" ;;
esac
case "$MANAGER_ADDR" in
  *:*) ;;
  *) MANAGER_ADDR="${MANAGER_ADDR}:443" ;;
esac

case "$STATIC_URL" in
  http://*|https://*) ;;
  *) die "static_url must use http:// or https://" ;;
esac
case "$STATIC_URL" in
  *" "*|*$'\t'*) die "static_url contains whitespace" ;;
esac
STATIC_URL="${STATIC_URL%/}"

case "$NYDUS_SNAPSHOTTER_ASSET" in
  ""|*[!A-Za-z0-9._-]*) die "nydus_snapshotter_asset is not a valid release asset name" ;;
esac
case "$NYDUS_SNAPSHOTTER_REPO" in
  ""|*[!A-Za-z0-9._/-]*|*/*/*) die "nydus_snapshotter_repo must be owner/repo" ;;
esac
case "$NYDUS_SNAPSHOTTER_REPO" in
  */*) ;;
  *) die "nydus_snapshotter_repo must be owner/repo" ;;
esac
if [ "$NYDUS_SNAPSHOTTER_VERSION" != "latest" ]; then
  case "$NYDUS_SNAPSHOTTER_VERSION" in
    v[A-Za-z0-9._-]*) ;;
    *) die "nydus_snapshotter_version must be latest or a v* release tag" ;;
  esac
fi

if [ -n "${SMART_PROXY_URL:-}" ]; then
  case "$SMART_PROXY_URL" in
    http://*|https://*) ;;
    *) die "smart_proxy_url must use http:// or https://" ;;
  esac
  case "$SMART_PROXY_URL" in
    *" "*|*$'\t'*) die "smart_proxy_url contains whitespace" ;;
  esac
  SMART_PROXY_URL="${SMART_PROXY_URL%/}"
fi

if [ -n "${NYDUS_MIRROR_URL:-}" ]; then
  case "$NYDUS_MIRROR_URL" in
    http://*|https://*) ;;
    *) die "nydus_mirror_url must use http:// or https://" ;;
  esac
  case "$NYDUS_MIRROR_URL" in
    *" "*|*$'\t'*|*$'\n'*|*$'\r'*|*'"'*|*'\'*|*"?"*|*"#"*|*"@"*)
      die "nydus_mirror_url contains an unsupported character" ;;
  esac
  # The registry proxy serves blobs under <mirror>/v2/..., so the mirror URL
  # must include the registry path (for example /docker.io), not only a host.
  if [[ ! "$NYDUS_MIRROR_URL" =~ ^https?://[^/]+/ ]]; then
    die "nydus_mirror_url must include the registry mirror path (for example /docker.io)"
  fi
  NYDUS_MIRROR_URL="${NYDUS_MIRROR_URL%/}"
fi

case "$SCHEDULER_CLUSTER_ID" in
  ""|*[!0-9]*) die "scheduler_cluster_id must be a non-negative integer" ;;
esac
# 0 is the pre-registration placeholder for the first seed node.
if [ "$SCHEDULER_CLUSTER_ID" -lt 0 ] 2>/dev/null; then
  die "scheduler_cluster_id must be a non-negative integer"
fi

# nydusd bootstrap id/domain_id. Precedence: optional env value, then the
# scheduler cluster ID, then hostname.
NYDUS_CLUSTER_ID="${NYDUS_CLUSTER_ID:-}"
if [ -z "$NYDUS_CLUSTER_ID" ]; then
  NYDUS_CLUSTER_ID="$SCHEDULER_CLUSTER_ID"
fi
if [ -z "$NYDUS_CLUSTER_ID" ]; then
  NYDUS_CLUSTER_ID="$(hostname 2>/dev/null || true)"
fi
if [ -z "$NYDUS_CLUSTER_ID" ]; then
  NYDUS_CLUSTER_ID="nydus"
fi
case "$NYDUS_CLUSTER_ID" in
  *[!A-Za-z0-9._-]*) die "nydus_cluster_id contains an unsupported character" ;;
esac

write_direct_template() {
  local major template
  major="${CONTAINERD_MAJOR:-$(detect_containerd_major)}"
  mkdir -p "$RKE2_CONTAINERD_DIR"
  case "$major" in
    2)
      # containerd 2.x reads the nydus drop-in from config-v3.toml.d. Remove it
      # so the node falls back to the RKE2 default overlayfs snapshotter.
      rm -f "$NYDUS_DROPIN"
      log "direct-registry containerd fallback: removed ${NYDUS_DROPIN}"
      ;;
    *)
      template="${RKE2_CONTAINERD_DIR}/config.toml.tmpl"
      write_file "$template" 0644 <<'TOML'
{{ template "base" . }}

# Direct-registry fallback: overlayfs only, no P2P snapshotter or proxy.
[plugins."io.containerd.grpc.v1.cri".containerd]
  snapshotter = "overlayfs"
TOML
      log "direct-registry containerd template written to ${template}"
      ;;
  esac
}

start_host_timers() {
  if [ "$REAL_ROOT" = "1" ] && [ "${SKIP_SYSTEMD:-0}" != "1" ] && command -v systemctl >/dev/null 2>&1; then
    if [ -f "${SERVICE_DIR}/nydus-updater.timer" ] && [ -f "${SERVICE_DIR}/dfdaemon-bootstrap-switch.timer" ]; then
      systemctl start nydus-updater.timer dfdaemon-bootstrap-switch.timer >/dev/null 2>&1 || true
    fi
  fi
}

fallback_direct() {
  start_host_timers
  if [ -z "${SCHEDULER_IPS:-}" ]; then
    log "P2P unavailable: $*; scheduler IPs are empty, keeping the nydus containerd config"
    exit 1
  fi
  log "P2P unavailable: $*; using direct registry so RKE2 can start"
  write_direct_template
  if [ "$REAL_ROOT" = "1" ] && [ "${SKIP_SYSTEMD:-0}" != "1" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now dfdaemon-bootstrap.service dfdaemon-bootstrap-boot.service nydus-snapshotter-bootstrap.service nydus-updater.timer >/dev/null 2>&1 || true
  fi
  log "existing certificate/config files, if any, were preserved; P2P services are not enabled"
  exit 0
}


tcp_check() {
  local host port
  host="$1"
  port="$2"
  timeout "$PREFLIGHT_TIMEOUT" bash -c "exec 3<>/dev/tcp/${host}/${port}" >/dev/null 2>&1
}

preflight() {
  local manager_host manager_port ip scheduler_ok
  if [ "${SKIP_PREFLIGHT:-0}" = "1" ]; then
    log "preflight skipped"
    return 0
  fi
  manager_host="${MANAGER_ADDR%:*}"
  manager_port="${MANAGER_ADDR##*:}"
  manager_host="${manager_host#[}"
  manager_host="${manager_host%]}"
  if ! tcp_check "$manager_host" "$manager_port"; then
    fallback_direct "manager gRPC ${manager_host}:${manager_port} is unreachable"
  fi
  scheduler_ok=0
  for ip in $(printf '%s' "$SCHEDULER_IPS" | tr ',' '\n'); do
    ip="${ip//[[:space:]]/}"
    [ -n "$ip" ] || continue
    if tcp_check "$ip" "$SCHEDULER_PORT"; then
      scheduler_ok=1
      break
    fi
  done
  if [ "$scheduler_ok" != "1" ]; then
    fallback_direct "no scheduler IP is reachable on port ${SCHEDULER_PORT}"
  fi
  log "preflight ok: manager ${manager_host}:${manager_port}, scheduler IPs ${SCHEDULER_IPS}"
}

if [ -n "${SCHEDULER_IPS:-}" ]; then
  preflight
else
  log "scheduler IPs are empty; installing nydus and dfdaemon without starting dfdaemon-bootstrap"
fi

# Declarative boot-time module loading for the nydus stack. cachefiles pulls
# fscache as a dependency; the selector below tolerates missing modules and
# falls back to fusedev, so this list is safe on kernels without on-demand
# fscache support.
write_file "${ROOT}/etc/modules-load.d/nydus.conf" 0644 <<'MODULES'
erofs
cachefiles
MODULES

mkdir -p \
  "${ROOT}${CERT_DIR}" \
  "${ROOT}${NYDUS_DIR}" \
  "$BIN_DIR" \
  "$SERVICE_DIR" \
  "$RKE2_CONTAINERD_DIR" \
  "${ROOT}/var/lib/nydus/cache" \
  "${ROOT}/var/lib/containerd-nydus" \
  "${ROOT}/var/lib/containerd-nydus/cache" \
  "${ROOT}/var/lib/dragonfly"

if ! printf '%s' "$CA_CRT_B64" | base64 -d > "$TMP/ca.crt"; then
  die "CA_CRT_B64 is not valid base64"
fi
if ! printf '%s' "$CLIENT_CRT_B64" | base64 -d > "$TMP/tls.crt"; then
  die "CLIENT_CRT_B64 is not valid base64"
fi
if ! printf '%s' "$CLIENT_KEY_B64" | base64 -d > "$TMP/tls.key"; then
  die "CLIENT_KEY_B64 is not valid base64"
fi
for file in "$TMP/ca.crt" "$TMP/tls.crt" "$TMP/tls.key"; do
  if [ ! -s "$file" ]; then
    die "decoded certificate material is empty"
  fi
done

write_file "${ROOT}${CERT_DIR}/ca.crt" 0644 < "$TMP/ca.crt"
write_file "${ROOT}${CERT_DIR}/tls.crt" 0644 < "$TMP/tls.crt"
write_file "${ROOT}${CERT_DIR}/tls.key" 0600 < "$TMP/tls.key"

# nydusd mirror layout: the snapshotter rewrites the registry host to the first
# matching hosts.toml entry. The same proxy path is used for docker.io and its
# index.docker.io alias; _default covers any other registry host. These files
# contain only the public mirror URL, never credentials.
if [ -n "${NYDUS_MIRROR_URL:-}" ]; then
  for mirror_host in docker.io index.docker.io _default; do
    mirror_dir="${ROOT}${NYDUS_MIRROR_DIR}/${mirror_host}"
    mkdir -p "$mirror_dir"
    {
      printf 'server = "%s"\n' "$NYDUS_MIRROR_URL"
      # nydus-snapshotter reads the containerd-compatible host table, so the
      # mirror also needs an explicit [host] entry with the same URL.
      printf '\n[host."%s"]\n' "$NYDUS_MIRROR_URL"
    } | write_file "${mirror_dir}/hosts.toml" 0644
  done
  # The selector reads this marker (not the mirror files) so an unchanged
  # mirror URL stays idempotent and stale entries are never invented.
  : | write_file "${ROOT}${NYDUS_DIR}/mirrors.enabled" 0644
else
  rm -f "${ROOT}${NYDUS_DIR}/mirrors.enabled"
fi

# P2P data upload is intentionally plaintext here. Scheduler control uses
# client mTLS with caCert/cert/key directly under scheduler, matching
# dfdaemon 1.5.14 and the in-cluster client config. The proxy scheduling rules
# match the chart client config so registry blobs/manifests use Dragonfly P2P.
# The operator's p2p-data-tls annotation does not apply to this pre-cluster
# bootstrap config.
write_file "${ROOT}${DFGET_CONFIG}" 0644 <<EOF
scheduler:
  caCert: "$CERT_DIR/ca.crt"
  cert: "$CERT_DIR/tls.crt"
  key: "$CERT_DIR/tls.key"
proxy:
  rules:
    - regex: blobs/sha256.*
      schedulingPolicy: always
    - regex: manifests/sha256.*
      schedulingPolicy: always
host:
  schedulerClusterID: $SCHEDULER_CLUSTER_ID
EOF

if [ -n "${SCHEDULER_IPS:-}" ]; then
  addrs=""
  old_ifs="$IFS"
  IFS=','
  for ip in $SCHEDULER_IPS; do
    ip="${ip//[[:space:]]/}"
    [ -n "$ip" ] || continue
    addrs="${addrs}${addrs:+, }\"${ip}:${SCHEDULER_PORT}\""
  done
  IFS="$old_ifs"
  write_file "${ROOT}${DYNCONFIG}" 0644 <<EOF
scheduler:
  addrs: [${addrs}]
EOF
  printf '%s\n' "$SCHEDULER_IPS" | write_file "${ROOT}/etc/dragonfly/scheduler-ips" 0644
else
  write_file "${ROOT}${DYNCONFIG}" 0644 <<'EOF'
scheduler:
  addrs: []
EOF
  rm -f "${ROOT}/etc/dragonfly/scheduler-ips"
fi

# /etc/nydus/config.toml is written below by nydus-fs-select after both nydusd
# configs exist, so the initial install and every snapshotter start use the
# same selection logic.

# nydusd fscache bootstrap. This is the canonical shape shipped as
# nydusd-config.fscache.json; the ids come from the environment or the
# scheduler cluster ID, never from a hardcoded cluster name. The Dragonfly local
# proxy URL/ping stay at 127.0.0.1:4001/4003 with fallback enabled.
write_file "${ROOT}${NYDUS_DIR}/nydusd-config.fscache.json" 0644 <<JSON
{
  "type": "bootstrap",
  "id": "$NYDUS_CLUSTER_ID",
  "domain_id": "$NYDUS_CLUSTER_ID",
  "config": {
    "version": 2,
    "id": "factory1",
    "backend": {
      "type": "registry",
      "registry": {
        "scheme": "https",
        "skip_verify": false,
        "timeout": 10,
        "connect_timeout": 5,
        "retry_limit": 4,
        "proxy": {
          "url": "http://127.0.0.1:4001",
          "ping_url": "http://127.0.0.1:4003/healthy",
          "fallback": true,
          "check_interval": 5
        }
      }
    },
    "cache": {
      "type": "fscache",
      "fscache": {
        "work_dir": "/var/lib/containerd-nydus/cache"
      }
    },
    "metadata_path": "/var/lib/containerd-nydus/cache"
  }
}
JSON

# fs_prefetch is the documented nydusd prefetch/readahead control. Avoid
# rafs.prefetch/rafs.readahead here: the pinned nydusd schema does not document
# those keys, and unknown fields can make nydusd fail to start.
#
# With a mirror configured, nydusd resolves the nydus blobs directly through
# the snapshotter-selected HTTPS mirror host and must not go through the
# dfdaemon proxy. Without a mirror, keep the explicit dfdaemon proxy fallback.
if [ -n "${NYDUS_MIRROR_URL:-}" ]; then
  write_file "${ROOT}${NYDUS_DIR}/nydusd-config.json" 0644 <<'JSON'
{
  "device": {
    "backend": {
      "type": "registry",
      "config": {
        "timeout": 5,
        "connect_timeout": 5,
        "retry_limit": 2
      }
    },
    "cache": {
      "type": "blobcache",
      "config": {
        "work_dir": "/var/lib/nydus/cache/"
      }
    }
  },
  "mode": "direct",
  "digest_validate": false,
  "iostats_files": false,
  "enable_xattr": true,
  "fs_prefetch": {
    "enable": true,
    "threads_count": 8,
    "merging_size": 1048576,
    "prefetch_all": true
  }
}
JSON
else
  # dfdaemon MITMs CONNECT with a self-signed cert (CN=rcgen); nydusd fails TLS
  # verification and falls back to direct Harbor. Plain HTTP to 127.0.0.1:4001
  # avoids the MITM.
  write_file "${ROOT}${NYDUS_DIR}/nydusd-config.json" 0644 <<'JSON'
{
  "device": {
    "backend": {
      "type": "registry",
      "config": {
        "proxy": {
          "url": "http://127.0.0.1:4001",
          "fallback": true,
          "check_interval": 5,
          "use_http": true
        },
        "timeout": 5,
        "connect_timeout": 5,
        "retry_limit": 2
      }
    },
    "cache": {
      "type": "blobcache",
      "config": {
        "work_dir": "/var/lib/nydus/cache/"
      }
    }
  },
  "mode": "direct",
  "digest_validate": false,
  "iostats_files": false,
  "enable_xattr": true,
  "fs_prefetch": {
    "enable": true,
    "threads_count": 8,
    "merging_size": 1048576,
    "prefetch_all": true
  }
}
JSON
fi

# The selector is the single writer of /etc/nydus/config.toml. Both modes stay
# installed; at every snapshotter start it probes the running kernel and picks
# the driver that can actually mount images. Keep this heredoc byte-identical
# to the canonical nydus-fs-select.sh asset (test.sh compares the installed copy).
write_file "${BIN_DIR}/nydus-fs-select" 0755 <<'SELECTOR'
#!/usr/bin/env bash
# Select the nydus snapshotter filesystem driver at service start.
#
# fscache (EROFS over the kernel fscache/cachefiles stack) is used only when
# the running kernel has on-demand read support, the required modules load,
# /dev/cachefiles is available, and cachefilesd does not own it. Every other
# case selects fusedev, so the snapshotter always has a working config.
set -Eeuo pipefail
IFS=$'\n\t'

P2P_ROOT="${P2P_ROOT:-/}"
ROOT="${P2P_ROOT%/}"
NYDUS_DIR="/etc/nydus"
CONFIG_PATH="${ROOT}${NYDUS_DIR}/config.toml"
FSCACHE_JSON="${NYDUS_DIR}/nydusd-config.fscache.json"
FUSEDEV_JSON="${NYDUS_DIR}/nydusd-config.json"
CACHEFILES_DEVICE="${CACHEFILES_DEVICE:-/dev/cachefiles}"
MODPROBE_BIN="${MODPROBE:-modprobe}"
SYSTEMCTL_BIN="${SYSTEMCTL:-systemctl}"
KERNEL_CONFIG_FILE="${KERNEL_CONFIG_FILE:-}"
MIRRORS_DIR="${NYDUS_MIRROR_DIR:-/etc/nydus/mirrors}"
MIRRORS_MARKER="${ROOT}${NYDUS_DIR}/mirrors.enabled"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

log() {
  printf 'nydus-fs-select: %s\n' "$*" >&2
}

write_config() {
  local tmp
  mkdir -p "$(dirname "$CONFIG_PATH")"
  tmp="${CONFIG_PATH}.tmp.$$"
  cat > "$tmp"
  chmod 0644 "$tmp"
  if [ -f "$CONFIG_PATH" ] && cmp -s "$tmp" "$CONFIG_PATH"; then
    rm -f "$tmp"
    chmod 0644 "$CONFIG_PATH"
  else
    mv -f "$tmp" "$CONFIG_PATH"
  fi
}

kernel_config_file() {
  local candidate
  if [ -n "$KERNEL_CONFIG_FILE" ]; then
    printf '%s' "$KERNEL_CONFIG_FILE"
    return 0
  fi
  candidate="${ROOT}/boot/config-$(uname -r)"
  if [ -r "$candidate" ]; then
    printf '%s' "$candidate"
    return 0
  fi
  if [ -r /proc/config.gz ] && command -v zcat >/dev/null 2>&1; then
    if zcat /proc/config.gz > "$TMP/kernel-config" 2>/dev/null; then
      printf '%s' "$TMP/kernel-config"
      return 0
    fi
  fi
  return 1
}

reason=""

release_cachefilesd() {
  local unit
  for unit in \
    "${ROOT}/etc/systemd/system/cachefilesd.service" \
    "${ROOT}/usr/lib/systemd/system/cachefilesd.service" \
    "${ROOT}/lib/systemd/system/cachefilesd.service"; do
    if [ -e "$unit" ]; then
      if ! command -v "$SYSTEMCTL_BIN" >/dev/null 2>&1; then
        reason="${SYSTEMCTL_BIN} is unavailable while cachefilesd.service exists"
        return 1
      fi
      "$SYSTEMCTL_BIN" stop cachefilesd.service >/dev/null 2>&1 || true
      "$SYSTEMCTL_BIN" disable cachefilesd.service >/dev/null 2>&1 || true
      if ! "$SYSTEMCTL_BIN" mask cachefilesd.service >/dev/null 2>&1; then
        reason="cachefilesd.service could not be masked; it may own ${CACHEFILES_DEVICE}"
        return 1
      fi
      if "$SYSTEMCTL_BIN" is-active --quiet cachefilesd.service >/dev/null 2>&1; then
        reason="cachefilesd.service is active and may own ${CACHEFILES_DEVICE}"
        return 1
      fi
      break
    fi
  done
  if command -v pgrep >/dev/null 2>&1 && pgrep -x cachefilesd >/dev/null 2>&1; then
    reason="cachefilesd is running and may own ${CACHEFILES_DEVICE}"
    return 1
  fi
  return 0
}

fscache_usable() {
  local cfg key found module
  if ! cfg="$(kernel_config_file)"; then
    reason="no readable kernel config (${ROOT}/boot/config-$(uname -r) or /proc/config.gz)"
    return 1
  fi
  if [ ! -r "$cfg" ]; then
    reason="kernel config ${cfg} is not readable"
    return 1
  fi
  for key in CONFIG_EROFS_FS_ONDEMAND CONFIG_CACHEFILES_ONDEMAND; do
    found="$(grep -m1 "^${key}=" "$cfg" 2>/dev/null | cut -d= -f2- || true)"
    if [ "$found" != "y" ]; then
      reason="${key} is '${found:-unset}', required literal ${key}=y"
      return 1
    fi
  done

  if [ "${SKIP_MODULES:-0}" != "1" ]; then
    if ! command -v "$MODPROBE_BIN" >/dev/null 2>&1; then
      reason="${MODPROBE_BIN} is unavailable"
      return 1
    fi
    for module in erofs fscache cachefiles; do
      if ! "$MODPROBE_BIN" "$module" >/dev/null 2>&1; then
        reason="kernel module ${module} could not be loaded"
        return 1
      fi
    done
  fi
  if [ ! -e "$CACHEFILES_DEVICE" ]; then
    reason="${CACHEFILES_DEVICE} is missing after loading cachefiles"
    return 1
  fi
  if [ "${SKIP_SYSTEMD:-0}" != "1" ]; then
    if ! release_cachefilesd; then
      return 1
    fi
  fi
  return 0
}

load_fusedev_modules() {
  local module
  [ "${SKIP_MODULES:-0}" = "1" ] && return 0
  command -v "$MODPROBE_BIN" >/dev/null 2>&1 || return 0
  for module in fuse erofs; do
    "$MODPROBE_BIN" "$module" >/dev/null 2>&1 || log "warning: could not load module ${module}"
  done
}

if ! fscache_usable; then
  mode="fusedev"
elif [ ! -f "${ROOT}${FSCACHE_JSON}" ]; then
  mode="fusedev"
  reason="${FSCACHE_JSON} is not installed"
else
  mode="fscache"
  reason="kernel has CONFIG_EROFS_FS_ONDEMAND=y and CONFIG_CACHEFILES_ONDEMAND=y, modules loaded, ${CACHEFILES_DEVICE} present, cachefilesd is not running"
fi

if [ "$mode" = "fusedev" ]; then
  if [ ! -f "${ROOT}${FUSEDEV_JSON}" ]; then
    log "ERROR: ${FUSEDEV_JSON} is missing; refusing to write an unusable config"
    exit 1
  fi
  load_fusedev_modules
  {
    cat <<'TOML'
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
    if [ -f "$MIRRORS_MARKER" ]; then
      cat <<TOML

# Mirror host selection for nydusd. The snapshotter rewrites the backend host
# to the first matching mirror before each mount; the files are written under
# ${MIRRORS_DIR}. Without a mirror entry nydusd keeps the origin registry.
[remote.mirrors_config]
  dir = "$MIRRORS_DIR"
TOML
    fi
  } | write_config
else
  if [ -f "$MIRRORS_MARKER" ]; then
    log "warning: nydus mirrors are only applied with fs_driver=fusedev; fscache keeps the dfdaemon proxy"
  fi
  write_config <<'TOML'
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
fi

log "selected fs_driver=${mode}: ${reason}"
SELECTOR

# Write the initial config.toml now so ConditionPathExists on the unit passes
# before the first start. ExecStartPre repeats the same probe on every start.
if ! "${BIN_DIR}/nydus-fs-select"; then
  fallback_direct "nydus filesystem selector failed"
fi

CONTAINERD_MAJOR="${CONTAINERD_MAJOR:-$(detect_containerd_major)}"
mkdir -p "$RKE2_CONTAINERD_DIR"
if [ "$CONTAINERD_MAJOR" = "2" ]; then
  # containerd 2.x: a drop-in keeps the RKE2 base template untouched. Nydus is
  # the node-wide CRI snapshotter; there is no in-cluster DaemonSet.
  mkdir -p "$NYDUS_DROPIN_DIR"
  write_file "$NYDUS_DROPIN" 0644 <<'TOML'
# Host-run nydus snapshotter from p2p-install.sh. Every CRI workload on this
# node uses nydus; there is no in-cluster DaemonSet.
[plugins.'io.containerd.cri.v1.images']
  snapshotter = 'nydus'
  # RKE2 defaults this to true, which bypasses the standard CRI unpack path.
  # The nydus snapshotter needs CRI labels (cri.image-ref/cri.manifest-digest)
  # and layer annotations to detect nydus meta/index; with true detection
  # silently fails.
  use_local_image_pull = false
  disable_snapshot_annotations = false
  discard_unpacked_layers = false

[proxy_plugins.nydus]
  type = 'snapshot'
  address = '/run/containerd-nydus/containerd-nydus-grpc.sock'
TOML
  # kubelet stats the CRI imagefs under containerd's state/root dir, not its
  # config dir, while the host nydus snapshotter stores data in
  # /var/lib/containerd-nydus. Without this link kubelet image GC reports
  # ImageGCFailed and node-pressure eviction aborts. Symlink only: no restart,
  # no data move.
  mkdir -p "$RKE2_CONTAINERD_STATE_DIR"
  ln -sfn /var/lib/containerd-nydus "${RKE2_CONTAINERD_STATE_DIR}/io.containerd.snapshotter.v1.nydus"
  CONTAINERD_CONFIG_PATH="$NYDUS_DROPIN"
else
  # containerd 1.x has no drop-in imports; write a global-nydus template. This
  # keeps the same host-only snapshotter model for older RKE2 nodes.
  TEMPLATE_PATH="${RKE2_CONTAINERD_DIR}/config.toml.tmpl"
  write_file "$TEMPLATE_PATH" 0644 <<'TOML'
{{ template "base" . }}

# Host-run nydus snapshotter from p2p-install.sh. Every CRI workload on this
# node uses nydus; there is no in-cluster DaemonSet.
[plugins."io.containerd.grpc.v1.cri".containerd]
  snapshotter = "nydus"
  disable_snapshot_annotations = false
  discard_unpacked_layers = false

[proxy_plugins.nydus]
  type = "snapshot"
  address = "/run/containerd-nydus/containerd-nydus-grpc.sock"
TOML
  CONTAINERD_CONFIG_PATH="$TEMPLATE_PATH"
fi

write_file "${SERVICE_DIR}/dfdaemon-bootstrap.service" 0644 <<UNIT
[Unit]
Description=Dragonfly bootstrap dfdaemon
Documentation=https://d7y.io
After=network-online.target
Wants=network-online.target
Before=${DISTRO_SERVICE_TARGETS}
ConditionPathExists=/etc/dragonfly/dfget.yaml
ConditionPathExists=/etc/dragonfly/scheduler-ips

[Service]
Type=simple
ExecStart=/usr/local/bin/dfdaemon --config /etc/dragonfly/dfget.yaml
Restart=always
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT

# A boot helper starts the host bootstrap even if the previous shutdown left it
# disabled after the switch. Wants= starts it regardless of enablement; the
# switch can then disable it when the in-cluster client takes over.
write_file "${SERVICE_DIR}/dfdaemon-bootstrap-boot.service" 0644 <<UNIT
[Unit]
Description=Start host dfdaemon bootstrap before RKE2
Documentation=https://d7y.io
After=local-fs.target
Wants=dfdaemon-bootstrap.service
Before=dfdaemon-bootstrap.service ${DISTRO_SERVICE_TARGETS}
ConditionPathExists=/etc/dragonfly/dfget.yaml

[Service]
Type=oneshot
ExecStart=/bin/true
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT

# Host-only switch: the in-cluster client DaemonSet is the steady state. The
# switch disables the host bootstrap only while the client is running, and
# re-enables it when the client disappears. It never touches the nydus units.
write_file "${BIN_DIR}/dfdaemon-bootstrap-switch" 0755 <<'SWITCH'
#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

UNIT="${DFDAEMON_UNIT:-dfdaemon-bootstrap.service}"
MARKER="${BOOTSTRAP_SWITCH_MARKER:-/var/lib/dragonfly/bootstrap-switched}"
NAMESPACE="${DFDAEMON_NAMESPACE:-dragonfly-system}"
CLIENT_PORT="${DFDAEMON_CLIENT_PORT:-4003}"
DISTRO="rke2"
if [ -d /var/lib/rancher/k3s ]; then
  DISTRO="k3s"
fi
CRICTL="${CRICTL:-}"
# RKE2 and k3s both expose the containerd socket at /run/k3s.
SOCKET="${CONTAINERD_SOCKET:-/run/k3s/containerd/containerd.sock}"

resolve_crictl() {
  local candidate
  if [ -n "$CRICTL" ] && [ -x "$CRICTL" ]; then
    return 0
  fi
  if command -v crictl >/dev/null 2>&1; then
    CRICTL="$(command -v crictl)"
    return 0
  fi
  for candidate in /usr/local/bin/crictl /var/lib/rancher/k3s/bin/crictl /var/lib/rancher/rke2/bin/crictl; do
    if [ -x "$candidate" ]; then
      CRICTL="$candidate"
      return 0
    fi
  done
  while IFS= read -r candidate; do
    if [ -x "$candidate" ]; then
      CRICTL="$candidate"
      return 0
    fi
  done < <(ls -1dt /var/lib/rancher/k3s/data/*/bin/crictl 2>/dev/null || true)
  CRICTL=""
  command -v k3s >/dev/null 2>&1
}
resolve_crictl || true

log() {
  printf 'dfdaemon-bootstrap-switch: %s\n' "$*" >&2
}

bootstrap_enabled() {
  systemctl is-enabled --quiet "$UNIT"
}

bootstrap_active() {
  systemctl is-active --quiet "$UNIT"
}

crictl_cmd() {
  if [ -n "$CRICTL" ]; then
    timeout 10 "$CRICTL" --runtime-endpoint "unix://${SOCKET}" "$@"
  else
    timeout 10 k3s crictl --runtime-endpoint "unix://${SOCKET}" "$@"
  fi
}

client_present() {
  local pods pod
  if [ -z "$CRICTL" ] && ! command -v k3s >/dev/null 2>&1; then
    return 1
  fi

  # The pod may be ContainerCreating while the host dfdaemon holds the
  # metadata lock. Its presence, not a running container, is the handoff signal.
  pods="$(crictl_cmd pods --namespace "$NAMESPACE" --label component=client -q 2>/dev/null || true)"
  [ -n "$pods" ] && return 0
  pods="$(crictl_cmd pods --namespace "$NAMESPACE" --label app.kubernetes.io/name=client -q 2>/dev/null || true)"
  [ -n "$pods" ] && return 0
  pods="$(crictl_cmd pods --namespace "$NAMESPACE" --name '.*-data-client-.*' -q 2>/dev/null || true)"
  [ -n "$pods" ] && return 0

  # Container-name fallback for crictl versions without pod label/name filters.
  for name in '^client$' '^dfdaemon$'; do
    if crictl_cmd ps --state running --label "io.kubernetes.pod.namespace=${NAMESPACE}" --name "$name" -q 2>/dev/null | grep -q .; then
      return 0
    fi
  done
  return 1
}

tcp_ready() {
  timeout 1 bash -c "exec 3<>/dev/tcp/127.0.0.1/$1" >/dev/null 2>&1
}

client_ready() {
  client_present && return 0
  # The host bootstrap and the in-cluster client may use the same local ports,
  # so the port fallback is only meaningful after the host bootstrap is off.
  if ! bootstrap_enabled && tcp_ready "$CLIENT_PORT"; then
    return 0
  fi
  return 1
}

if client_ready; then
  if bootstrap_enabled || bootstrap_active; then
    systemctl disable --now "$UNIT"
    mkdir -p "$(dirname "$MARKER")"
    date -u '+%Y-%m-%dT%H:%M:%SZ' > "$MARKER"
    log "in-cluster client pod present; host bootstrap disabled"
  fi
else
  if ! bootstrap_enabled || ! bootstrap_active; then
    systemctl enable --now "$UNIT"
    rm -f "$MARKER"
    log "in-cluster client is absent; host bootstrap re-enabled"
  fi
fi
SWITCH

write_file "${SERVICE_DIR}/dfdaemon-bootstrap-switch.service" 0644 <<'UNIT'
[Unit]
Description=Switch host dfdaemon bootstrap based on the in-cluster client
Documentation=https://d7y.io
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/dfdaemon-bootstrap-switch
Nice=10
IOSchedulingClass=idle
UNIT

write_file "${SERVICE_DIR}/dfdaemon-bootstrap-switch.timer" 0644 <<'UNIT'
[Unit]
Description=Periodic host dfdaemon bootstrap switch check
Documentation=https://d7y.io

[Timer]
OnBootSec=45s
OnUnitActiveSec=30s
AccuracySec=5s
Unit=dfdaemon-bootstrap-switch.service

[Install]
WantedBy=timers.target
UNIT

{
  cat <<UNIT
[Unit]
Description=Dragonfly host nydus snapshotter
Documentation=https://d7y.io
After=network-online.target
Wants=network-online.target
After=dfdaemon-bootstrap.service
Before=${DISTRO_SERVICE_TARGETS}
ConditionPathExists=/etc/nydus/config.toml

[Service]
Type=simple
Environment=PATH=/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin
UNIT
  cat <<'UNIT'
ExecStartPre=/usr/local/bin/nydus-fs-select
ExecStart=/usr/local/bin/containerd-nydus-grpc --config /etc/nydus/config.toml --nydusd /usr/local/bin/nydusd
UNIT
  cat <<'UNIT'
ExecStartPost=/bin/sh -c 'i=0; while [ ! -S /run/containerd-nydus/containerd-nydus-grpc.sock ]; do i=$((i + 1)); [ "$i" -ge 60 ] && exit 1; sleep 1; done'
Restart=always
RestartSec=5
RuntimeDirectory=containerd-nydus
RuntimeDirectoryMode=0755
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT
} | write_file "${SERVICE_DIR}/nydus-snapshotter-bootstrap.service" 0644

# Host-only updater: keeps the pinned static nydus binaries current and
# restarts the host unit when anything changed. The upstream nydus-snapshotter
# binary comes from its release tarball and is updated by rerunning this
# script, not by the static-store updater. It never touches Kubernetes.
{
  printf 'STATIC_URL=%q\n' "$STATIC_URL"
  printf 'CLIENT_VERSION=%q\n' "$DRAGONFLY_CLIENT_VERSION"
  printf 'CLIENT_RELEASE_REPO=%q\n' "$DRAGONFLY_CLIENT_REPO"
  printf 'CLIENT_SHA256=%q\n' "${DRAGONFLY_CLIENT_SHA256:-}"
  printf 'NYDUS_VERSION=%q\n' "$NYDUS_VERSION"
  printf 'NYDUS_RELEASE_REPO=%q\n' "$NYDUS_GITHUB_REPO"
  printf 'NYDUS_GITHUB_REPO=%q\n' "$NYDUS_GITHUB_REPO"
  printf 'NYDUS_SHA256=%q\n' "${NYDUS_GITHUB_SHA256:-}"
  printf 'NYDUS_GITHUB_SHA256=%q\n' "${NYDUS_GITHUB_SHA256:-}"
  printf 'NYDUS_SNAPSHOTTER_VERSION=%q\n' "$NYDUS_SNAPSHOTTER_VERSION"
  printf 'NYDUS_SNAPSHOTTER_REPO=%q\n' "$NYDUS_SNAPSHOTTER_REPO"
  printf 'NYDUS_SNAPSHOTTER_SHA256=%q\n' "${NYDUS_SNAPSHOTTER_SHA256:-}"
  printf 'NYDUS_SNAPSHOTTER_BINARY_SHA256=%q\n' "${NYDUS_SNAPSHOTTER_BINARY_SHA256:-}"
  printf 'NYDUS_ALLOW_GITHUB=%q\n' "$NYDUS_ALLOW_GITHUB"
} | write_file "${ROOT}${NYDUS_DIR}/updater.env" 0644
{
  printf 'STATIC_URL=%q\n' "$STATIC_URL"
  printf 'CLIENT_VERSION=%q\n' "$DRAGONFLY_CLIENT_VERSION"
  printf 'CLIENT_RELEASE_REPO=%q\n' "$DRAGONFLY_CLIENT_REPO"
  printf 'CLIENT_SHA256=%q\n' "${DRAGONFLY_CLIENT_SHA256:-}"
  printf 'NYDUS_VERSION=%q\n' "$NYDUS_VERSION"
  printf 'NYDUS_RELEASE_REPO=%q\n' "$NYDUS_GITHUB_REPO"
  printf 'NYDUS_GITHUB_REPO=%q\n' "$NYDUS_GITHUB_REPO"
  printf 'NYDUS_SHA256=%q\n' "${NYDUS_GITHUB_SHA256:-}"
  printf 'NYDUS_GITHUB_SHA256=%q\n' "${NYDUS_GITHUB_SHA256:-}"
  printf 'NYDUS_SNAPSHOTTER_VERSION=%q\n' "$NYDUS_SNAPSHOTTER_VERSION"
  printf 'NYDUS_SNAPSHOTTER_REPO=%q\n' "$NYDUS_SNAPSHOTTER_REPO"
  printf 'NYDUS_SNAPSHOTTER_SHA256=%q\n' "${NYDUS_SNAPSHOTTER_SHA256:-}"
  printf 'NYDUS_SNAPSHOTTER_BINARY_SHA256=%q\n' "${NYDUS_SNAPSHOTTER_BINARY_SHA256:-}"
  printf 'NYDUS_ALLOW_GITHUB=%q\n' "$NYDUS_ALLOW_GITHUB"
} | write_file "${ROOT}${NYDUS_DIR}/versions.env" 0644

write_file "${BIN_DIR}/nydus-updater" 0755 <<'UPDATER'
#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

env_file="${NYDUS_UPDATER_ENV:-/etc/nydus/updater.env}"
if [ ! -f "$env_file" ]; then
  echo "nydus-updater: missing ${env_file}" >&2
  exit 1
fi
# shellcheck disable=SC1090
. "$env_file"
: "${CLIENT_VERSION:?CLIENT_VERSION is required}"
: "${NYDUS_VERSION:?NYDUS_VERSION is required}"
: "${NYDUS_SNAPSHOTTER_VERSION:?NYDUS_SNAPSHOTTER_VERSION is required}"
: "${CLIENT_RELEASE_REPO:?CLIENT_RELEASE_REPO is required}"
: "${NYDUS_RELEASE_REPO:?NYDUS_RELEASE_REPO is required}"
: "${NYDUS_SNAPSHOTTER_REPO:?NYDUS_SNAPSHOTTER_REPO is required}"
CLIENT_SHA256="${CLIENT_SHA256:-}"
NYDUS_SHA256="${NYDUS_SHA256:-}"
NYDUS_SNAPSHOTTER_SHA256="${NYDUS_SNAPSHOTTER_SHA256:-}"
NYDUS_SNAPSHOTTER_BINARY_SHA256="${NYDUS_SNAPSHOTTER_BINARY_SHA256:-}"
NYDUS_ALLOW_GITHUB="${NYDUS_ALLOW_GITHUB:-1}"

bin_dir="${NYDUS_UPDATER_BIN_DIR:-/usr/local/bin}"
selector="${NYDUS_UPDATER_SELECTOR:-/usr/local/bin/nydus-fs-select}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

log() { echo "nydus-updater: $*" >&2; }

case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH="amd64"; CLIENT_ARCH="x86_64" ;;
  aarch64|arm64) HOST_ARCH="arm64"; CLIENT_ARCH="aarch64" ;;
  armv7l|armhf|armv6l) log "unsupported architecture $(uname -m)"; exit 1 ;;
  *) log "unsupported architecture $(uname -m)"; exit 1 ;;
esac
case "$HOST_ARCH" in
  amd64)
    CLIENT_SHA256="${CLIENT_SHA256_AMD64:-${CLIENT_SHA256:-}}"
    NYDUS_SHA256="${NYDUS_SHA256_AMD64:-${NYDUS_SHA256:-}}"
    NYDUS_SNAPSHOTTER_SHA256="${NYDUS_SNAPSHOTTER_SHA256_AMD64:-${NYDUS_SNAPSHOTTER_SHA256:-}}"
    NYDUS_SNAPSHOTTER_BINARY_SHA256="${NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64:-${NYDUS_SNAPSHOTTER_BINARY_SHA256:-}}"
    ;;
  arm64)
    CLIENT_SHA256="${CLIENT_SHA256_ARM64:-${CLIENT_SHA256:-}}"
    NYDUS_SHA256="${NYDUS_SHA256_ARM64:-${NYDUS_SHA256:-}}"
    NYDUS_SNAPSHOTTER_SHA256="${NYDUS_SNAPSHOTTER_SHA256_ARM64:-${NYDUS_SNAPSHOTTER_SHA256:-}}"
    NYDUS_SNAPSHOTTER_BINARY_SHA256="${NYDUS_SNAPSHOTTER_BINARY_SHA256_ARM64:-${NYDUS_SNAPSHOTTER_BINARY_SHA256:-}}"
    ;;
esac

# Release-provided checksum first; the configured pin is only a fallback.
resolve_checksum() {
  local url="$1" fallback="${2:-}" suffix sum_file sum
  case "$url" in
    *dragonfly-client*)
      if [ -n "$fallback" ]; then
        printf '%s' "$fallback"
        return 0
      fi
      ;;
  esac
  for suffix in .sha256sum .sha256; do
    sum_file="${tmp}/checksum.$$"
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 60 -o "$sum_file" "${url}${suffix}" && [ -s "$sum_file" ]; then
      IFS=' ' read -r sum _ < "$sum_file"
      rm -f "$sum_file"
      if [ -n "$sum" ]; then
        printf '%s' "$sum"
        return 0
      fi
    fi
    rm -f "$sum_file"
  done
  [ -n "$fallback" ] && { printf '%s' "$fallback"; return 0; }
  return 1
}

verify_sha256() {
  local file="$1" expected="$2" label="$3" actual
  [ -n "$expected" ] || { log "missing checksum for ${label}"; return 1; }
  actual="$(sha256sum "$file")"
  actual="${actual%% *}"
  if [ "$actual" != "$expected" ]; then
    log "checksum mismatch for ${label}: got ${actual} want ${expected}"
    return 1
  fi
}

download_verified() {
  local url="$1" out="$2" checksum="$3" label="$4"
  if ! curl -fL --retry 3 --connect-timeout 10 --max-time 600 -o "$out" "$url"; then
    log "download failed: ${url}"
    return 1
  fi
  verify_sha256 "$out" "$checksum" "$label"
}

install_if_changed() {
  local src="$1" dst="$2" label="$3"
  if [ -x "$dst" ] && cmp -s "$src" "$dst"; then
    return 0
  fi
  install -m 0755 "$src" "${dst}.new.$$"
  mv -f "${dst}.new.$$" "$dst"
  log "updated ${label}"
  return 1
}

extract_tarball() {
  local tarball="$1" dir="$2"
  rm -rf "$dir"
  mkdir -p "$dir"
  tar -xzf "$tarball" -C "$dir"
}

# Client: prefer versioned static-store binaries, then the GitHub tarball.
client_root="dragonfly-client-${CLIENT_VERSION}-${CLIENT_ARCH}-unknown-linux-gnu"
client_url="https://github.com/${CLIENT_RELEASE_REPO}/releases/download/${CLIENT_VERSION}/${client_root}.tar.gz"
client_changed=0
client_static=0
if [ -n "${STATIC_URL:-}" ]; then
  client_static=1
  for binary in dfdaemon dfget; do
    if ! curl -fsSL --retry 3 --connect-timeout 10 --max-time 600 \
      -o "$tmp/static-${binary}" "${STATIC_URL%/}/client/${CLIENT_VERSION}/${HOST_ARCH}/${binary}"; then
      client_static=0
      break
    fi
  done
fi
if [ "$client_static" = 1 ]; then
  for binary in dfdaemon dfget; do
    install_if_changed "$tmp/static-${binary}" "${bin_dir}/${binary}" "client ${binary} ${CLIENT_VERSION}" || client_changed=1
  done
  log "client ${CLIENT_VERSION} using static store"
else
  client_checksum="$(resolve_checksum "$client_url" "$CLIENT_SHA256")" \
    || { log "no checksum available for client ${CLIENT_VERSION} ${HOST_ARCH}"; exit 1; }
  if ! download_verified "$client_url" "$tmp/client.tgz" "$client_checksum" "client ${CLIENT_VERSION}"; then
    exit 1
  fi
  extract_tarball "$tmp/client.tgz" "$tmp/client"
  for binary in dfdaemon dfget; do
    src="$tmp/client/${client_root}/${binary}"
    [ -s "$src" ] || { log "client tarball is missing ${binary}"; exit 1; }
    install_if_changed "$src" "${bin_dir}/${binary}" "client ${binary} ${CLIENT_VERSION}" || client_changed=1
  done
  log "client ${CLIENT_VERSION} using GitHub release"
fi

# Nydus daemon binaries: versioned static tarball, compat tarballs, GitHub.
nydus_url="https://github.com/${NYDUS_RELEASE_REPO}/releases/download/${NYDUS_VERSION}/nydus-static-${NYDUS_VERSION}-linux-${HOST_ARCH}.tgz"
nydus_checksum="$(resolve_checksum "$nydus_url" "$NYDUS_SHA256")" \
  || { log "no checksum available for nydus ${NYDUS_VERSION} ${HOST_ARCH}"; exit 1; }
nydus_tarball=""
nydus_source=""
if [ -n "${STATIC_URL:-}" ]; then
  for path in \
    "/nydus/${NYDUS_VERSION}/${HOST_ARCH}/nydus-static.tgz" \
    "/nydus/${NYDUS_VERSION}/nydus-static.tgz" \
    "/bin/nydus-static.tgz"; do
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 600 -o "$tmp/nydus.tgz" "${STATIC_URL%/}${path}"; then
      nydus_tarball="$tmp/nydus.tgz"
      nydus_source="static store ${path}"
      break
    fi
  done
fi
if [ -z "$nydus_tarball" ]; then
  if ! download_verified "$nydus_url" "$tmp/nydus.tgz" "$nydus_checksum" "nydus ${NYDUS_VERSION}"; then
    exit 1
  fi
  nydus_tarball="$tmp/nydus.tgz"
  nydus_source="GitHub release"
else
  verify_sha256 "$nydus_tarball" "$nydus_checksum" "nydus ${NYDUS_VERSION} (static)" || exit 1
fi
extract_tarball "$nydus_tarball" "$tmp/nydus"
nydus_changed=0
for binary in nydusd nydus-image nydus-overlayfs; do
  src="$tmp/nydus/nydus-static/${binary}"
  [ -s "$src" ] || { log "nydus tarball is missing ${binary}"; exit 1; }
  install_if_changed "$src" "${bin_dir}/${binary}" "nydus ${binary} ${NYDUS_VERSION}" || nydus_changed=1
done
log "nydus ${NYDUS_VERSION} using ${nydus_source}"

# Upstream snapshotter tarball: versioned static, compat /bin, GitHub.
snapshotter_asset="nydus-snapshotter-${NYDUS_SNAPSHOTTER_VERSION}-linux-${HOST_ARCH}.tar.gz"
snapshotter_url="https://github.com/${NYDUS_SNAPSHOTTER_REPO}/releases/download/${NYDUS_SNAPSHOTTER_VERSION}/${snapshotter_asset}"
snapshotter_checksum="$(resolve_checksum "$snapshotter_url" "$NYDUS_SNAPSHOTTER_SHA256")" \
  || { log "no checksum available for nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} ${HOST_ARCH}"; exit 1; }
snapshotter_tarball=""
snapshotter_source=""
if [ -n "${STATIC_URL:-}" ]; then
  for path in \
    "/snapshotter/${NYDUS_SNAPSHOTTER_VERSION}/${HOST_ARCH}/${snapshotter_asset}" \
    "/bin/${snapshotter_asset}"; do
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 600 -o "$tmp/snapshotter.tgz" "${STATIC_URL%/}${path}"; then
      snapshotter_tarball="$tmp/snapshotter.tgz"
      snapshotter_source="static store ${path}"
      break
    fi
  done
fi
if [ -z "$snapshotter_tarball" ]; then
  if ! download_verified "$snapshotter_url" "$tmp/snapshotter.tgz" "$snapshotter_checksum" "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION}"; then
    exit 1
  fi
  snapshotter_tarball="$tmp/snapshotter.tgz"
  snapshotter_source="GitHub release"
else
  verify_sha256 "$snapshotter_tarball" "$snapshotter_checksum" "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} (static)" || exit 1
fi
extract_tarball "$snapshotter_tarball" "$tmp/snapshotter"
snapshotter_src="$tmp/snapshotter/bin/containerd-nydus-grpc"
[ -s "$snapshotter_src" ] || { log "nydus-snapshotter tarball is missing bin/containerd-nydus-grpc"; exit 1; }
if [ -n "$NYDUS_SNAPSHOTTER_BINARY_SHA256" ]; then
  verify_sha256 "$snapshotter_src" "$NYDUS_SNAPSHOTTER_BINARY_SHA256" "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} binary" || exit 1
fi
snapshotter_changed=0
install_if_changed "$snapshotter_src" "${bin_dir}/containerd-nydus-grpc" "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION}" || snapshotter_changed=1
log "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} using ${snapshotter_source}"

# Re-evaluate the driver even when binaries did not change: a kernel upgrade can
# make fscache usable (or unusable). Restart only when the selection or binaries
# actually changed.
config_changed=0
if [ -x "$selector" ]; then
  config_before=""
  config_after=""
  if [ -f /etc/nydus/config.toml ]; then
    config_before="$(sha256sum /etc/nydus/config.toml | cut -d' ' -f1)"
  fi
  "$selector"
  if [ -f /etc/nydus/config.toml ]; then
    config_after="$(sha256sum /etc/nydus/config.toml | cut -d' ' -f1)"
  fi
  if [ "$config_before" != "$config_after" ]; then
    config_changed=1
  fi
fi

if [ "$nydus_changed" = 1 ] || [ "$snapshotter_changed" = 1 ] || [ "$config_changed" = 1 ]; then
  systemctl restart nydus-snapshotter-bootstrap.service
  systemctl is-active --quiet nydus-snapshotter-bootstrap.service || {
    log "nydus-snapshotter-bootstrap.service is not active after restart"
    exit 1
  }
  log "updated nydus/snapshotter and restarted the host snapshotter"
else
  log "nydus/snapshotter already current; fs_driver selection unchanged"
fi

if [ "$client_changed" = 1 ]; then
  if systemctl is-active --quiet dfdaemon-bootstrap.service; then
    systemctl restart dfdaemon-bootstrap.service
    log "updated client binaries and restarted dfdaemon-bootstrap"
  else
    log "updated client binaries; dfdaemon-bootstrap is inactive"
  fi
fi
UPDATER

write_file "${SERVICE_DIR}/nydus-updater.service" 0644 <<'UNIT'
[Unit]
Description=Update host nydus snapshotter binaries
Documentation=https://d7y.io
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/nydus-updater
Nice=10
IOSchedulingClass=idle
UNIT

write_file "${SERVICE_DIR}/nydus-updater.timer" 0644 <<'UNIT'
[Unit]
Description=Daily host nydus snapshotter binary update check
Documentation=https://d7y.io

[Timer]
OnBootSec=10min
OnUnitActiveSec=24h
RandomizedDelaySec=30min
Unit=nydus-updater.service

[Install]
WantedBy=timers.target
UNIT

if [ "${SKIP_DOWNLOAD:-0}" = "1" ]; then
  log "SKIP_DOWNLOAD=1: skipping binary downloads (nydus ${NYDUS_VERSION}, snapshotter ${NYDUS_SNAPSHOTTER_VERSION})"
else
  command -v curl >/dev/null 2>&1 || fallback_direct "curl is required to download binaries"
  # First delivery stays direct: dfdaemon and dfget come from the static store
  # or the pinned client tarball. Only after dfdaemon is installed and its
  # socket exists may the remaining payloads use dfget/P2P.
  install_direct_client_binaries

  p2p_ready=0
  if p2p_assets_available; then
    if start_dfdaemon_for_assets; then
      p2p_ready=1
    fi
  else
    log "P2P asset fetch disabled; downloading nydus/snapshotter directly"
  fi

  if ! install_nydus_assets "$p2p_ready"; then
    fallback_direct "failed to download nydus ${NYDUS_VERSION} (P2P/static/GitHub)"
  fi

  snapshotter_tmp="${BIN_DIR}/.${NYDUS_SNAPSHOTTER_BINARY}.tmp.$$"
  if ! install_nydus_snapshotter_asset "$p2p_ready" "$snapshotter_tmp"; then
    rm -f "$snapshotter_tmp" "${snapshotter_tmp}.tgz" "${snapshotter_tmp}.sha256sum"
    fallback_direct "upstream nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} ${HOST_ARCH} tarball ${NYDUS_SNAPSHOTTER_ASSET} is unavailable from ${NYDUS_SNAPSHOTTER_REPO}"
  fi
  chmod 0755 "$snapshotter_tmp"
  mv -f "$snapshotter_tmp" "${BIN_DIR}/${NYDUS_SNAPSHOTTER_BINARY}"
fi

if [ "${SKIP_DOWNLOAD:-0}" != "1" ]; then
  for binary in $NYDUS_BINARIES dfdaemon; do
    if [ ! -x "${BIN_DIR}/${binary}" ]; then
      fallback_direct "${binary} is not executable"
    fi
  done
  if [ ! -x "${BIN_DIR}/${NYDUS_SNAPSHOTTER_BINARY}" ]; then
    fallback_direct "nydus-snapshotter is not executable after install"
  fi
fi

if [ "$REAL_ROOT" = "1" ] && [ "${SKIP_SYSTEMD:-0}" != "1" ]; then
  command -v systemctl >/dev/null 2>&1 || fallback_direct "systemctl is required"
  systemctl daemon-reload || fallback_direct "systemctl daemon-reload failed"
  systemctl enable dfdaemon-bootstrap.service dfdaemon-bootstrap-boot.service nydus-snapshotter-bootstrap.service nydus-updater.timer dfdaemon-bootstrap-switch.timer || \
    fallback_direct "failed to enable bootstrap services"
  if [ -n "${SCHEDULER_IPS:-}" ]; then
    systemctl start nydus-updater.timer dfdaemon-bootstrap-switch.timer || \
      fallback_direct "failed to start host timers"
    systemctl restart dfdaemon-bootstrap.service nydus-snapshotter-bootstrap.service || \
      fallback_direct "failed to start bootstrap services"
    sleep 2
    systemctl is-active --quiet dfdaemon-bootstrap.service || fallback_direct "dfdaemon bootstrap failed to start"
  else
    systemctl start nydus-updater.timer dfdaemon-bootstrap-switch.timer || \
      fallback_direct "failed to start host timers"
    systemctl restart nydus-snapshotter-bootstrap.service || \
      fallback_direct "failed to start nydus bootstrap service"
    sleep 2
  fi
  systemctl is-active --quiet nydus-snapshotter-bootstrap.service || fallback_direct "nydus bootstrap failed to start"
fi

log "certificates written to ${CERT_DIR}"
log "dfget.yaml written with scheduler cluster ID ${SCHEDULER_CLUSTER_ID}"
log "dynconfig written with scheduler IPs ${SCHEDULER_IPS:-}"
log "nydus ${NYDUS_VERSION} fscache and fusedev configs written to ${NYDUS_DIR}; nydus-fs-select chooses the driver at snapshotter start"
log "nydus fscache requires RAFS v6 images; the nydusify conversion must pass --fs-version 6 (fs_version=6; proxy-owned, not this chart)"
if [ -n "${NYDUS_MIRROR_URL:-}" ]; then
  log "nydus mirror ${NYDUS_MIRROR_URL} installed for fusedev; fscache keeps the dfdaemon proxy"
else
  log "nydus mirror disabled; keeping the dfdaemon proxy fallback for nydus blobs"
fi
log "nydus-snapshotter ${NYDUS_SNAPSHOTTER_VERSION} installed from ${NYDUS_SNAPSHOTTER_REPO}"
log "containerd nydus configuration written to ${CONTAINERD_CONFIG_PATH}"
log "host bootstrap services, nydus updater, and dfdaemon switch installed in ${SERVICE_DIR}"
