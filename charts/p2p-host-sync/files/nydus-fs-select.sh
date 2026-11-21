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
