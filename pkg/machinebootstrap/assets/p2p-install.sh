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
# Static-store assets for nydusd/nydus-image/nydus-overlayfs. The host
# snapshotter is a separate patched release, see below.
NYDUS_VERSION="${NYDUS_VERSION:-v2.4.5}"
NYDUS_BINARIES="nydusd
nydus-image
nydus-overlayfs"
# Patched nydus-snapshotter release (host containerd-nydus-grpc binary). It
# reads containerd certs.d/hosts.toml, so a docker.io ref resolves through the
# same mirror that containerd used for the pull. Installed to the existing
# /usr/local/bin/containerd-nydus-grpc path and run by the same unit.
NYDUS_SNAPSHOTTER_REPO="${NYDUS_SNAPSHOTTER_REPO:-gorizond/nydus-snapshotter}"
NYDUS_SNAPSHOTTER_VERSION="${NYDUS_SNAPSHOTTER_VERSION:-v0.16.0-p2p.2}"
NYDUS_SNAPSHOTTER_ASSET="${NYDUS_SNAPSHOTTER_ASSET:-nydus-snapshotter-linux-amd64}"
NYDUS_SNAPSHOTTER_BINARY="containerd-nydus-grpc"
# Optional official Nydus release fallback for the daemon binaries when the
# static store is not populated. The tarball contains nydusd, nydus-image, and
# nydus-overlayfs.
NYDUS_ALLOW_GITHUB="${NYDUS_ALLOW_GITHUB:-1}"
NYDUS_GITHUB_REPO="${NYDUS_GITHUB_REPO:-dragonflyoss/nydus}"
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
SCHEDULER_PORT=443
PREFLIGHT_TIMEOUT="${P2P_PREFLIGHT_TIMEOUT:-3}"
BIN_DIR="${ROOT}/usr/local/bin"
SERVICE_DIR="${ROOT}/etc/systemd/system"
RKE2_CONTAINERD_DIR="${ROOT}/var/lib/rancher/${DISTRO}/agent/etc/containerd"
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

# Prefer the versioned nydus asset layout, then the legacy unversioned /bin
# layout. The static store is populated externally; both layouts are served by
# the p2p-static chart.
NYDUS_GITHUB_DIR=""

nydus_github_tarball_url() {
  local base="https://github.com/${NYDUS_GITHUB_REPO}/releases"
  if [ "$NYDUS_VERSION" = "latest" ]; then
    printf '%s/latest/download/nydus-static-latest-linux-amd64.tgz' "$base"
  else
    printf '%s/download/%s/nydus-static-%s-linux-amd64.tgz' "$base" "$NYDUS_VERSION" "$NYDUS_VERSION"
  fi
}

ensure_nydus_github_assets() {
  local dir url tarball
  if [ -n "$NYDUS_GITHUB_DIR" ]; then
    return 0
  fi
  if [ "${NYDUS_ALLOW_GITHUB:-1}" != "1" ]; then
    log "nydus static store unavailable and NYDUS_ALLOW_GITHUB=0; not trying GitHub"
    return 1
  fi
  dir="${TMP}/nydus-github"
  tarball="${dir}/nydus-static.tgz"
  mkdir -p "$dir"
  url="$(nydus_github_tarball_url)"
  if ! curl -fL --retry 3 --connect-timeout 10 --max-time 300 -o "$tarball" "$url"; then
    log "nydus GitHub release unavailable: ${url}"
    return 1
  fi
  if ! tar -xzf "$tarball" -C "$dir"; then
    log "nydus GitHub release tarball is invalid: ${url}"
    return 1
  fi
  if [ ! -d "${dir}/nydus-static" ]; then
    log "nydus GitHub release tarball has no nydus-static directory: ${url}"
    return 1
  fi
  NYDUS_GITHUB_DIR="${dir}/nydus-static"
  log "nydus binaries using GitHub release ${NYDUS_VERSION}"
  return 0
}

download_nydus_asset() {
  local name out url
  name="$1"
  out="$2"
  for url in "${STATIC_URL%/}/nydus/${NYDUS_VERSION}/${name}" "${STATIC_URL%/}/bin/${name}"; do
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 300 -o "$out" "$url"; then
      if [ ! -s "$out" ]; then
        die "downloaded ${name} is empty"
      fi
      log "nydus ${name}: using static store"
      return 0
    fi
  done
  if ensure_nydus_github_assets && [ -s "${NYDUS_GITHUB_DIR}/${name}" ]; then
    cp "${NYDUS_GITHUB_DIR}/${name}" "$out"
    log "nydus ${name}: using GitHub release ${NYDUS_VERSION}"
    return 0
  fi
  return 1
}

nydus_snapshotter_release_url() {
  local base="https://github.com/${NYDUS_SNAPSHOTTER_REPO}/releases"
  if [ "$NYDUS_SNAPSHOTTER_VERSION" = "latest" ]; then
    printf '%s/latest/download/%s' "$base" "$NYDUS_SNAPSHOTTER_ASSET"
  else
    printf '%s/download/%s/%s' "$base" "$NYDUS_SNAPSHOTTER_VERSION" "$NYDUS_SNAPSHOTTER_ASSET"
  fi
}

# Download the patched nydus-snapshotter release and verify its sibling
# .sha256 asset. Returns nonzero when the release asset is unavailable; a
# checksum mismatch is fatal.
download_nydus_snapshotter_asset() {
  local out url checksum expected actual
  out="$1"
  url="$(nydus_snapshotter_release_url)"
  if ! curl -fL --retry 3 --connect-timeout 10 --max-time 300 -o "$out" "$url"; then
    return 1
  fi
  if [ ! -s "$out" ]; then
    return 1
  fi
  checksum="${out}.sha256"
  if ! curl -fL --retry 3 --connect-timeout 10 --max-time 300 -o "$checksum" "${url}.sha256" || [ ! -s "$checksum" ]; then
    rm -f "$checksum"
    return 1
  fi
  command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required to verify ${NYDUS_SNAPSHOTTER_ASSET}"
  IFS=' ' read -r expected _ < "$checksum"
  actual="$(sha256sum "$out")"
  actual="${actual%% *}"
  if [ -z "$expected" ] || [ "$actual" != "$expected" ]; then
    die "checksum verification failed for ${url}"
  fi
  rm -f "$checksum"
  return 0
}

for key in MANAGER_GRPC STATIC_URL SCHEDULER_HOST SCHEDULER_CLUSTER_ID CA_CRT_B64 CLIENT_CRT_B64 CLIENT_KEY_B64; do
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

case "$SCHEDULER_HOST" in
  ""|*[!A-Za-z0-9.-]*|*..*|.*|*.) die "scheduler_host is not a DNS name" ;;
esac

case "$SCHEDULER_CLUSTER_ID" in
  ""|*[!0-9]*) die "scheduler_cluster_id must be a positive integer" ;;
esac
if [ "$SCHEDULER_CLUSTER_ID" -lt 1 ] 2>/dev/null; then
  die "scheduler_cluster_id must be a positive integer"
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

fallback_direct() {
  if [ -z "${SCHEDULER_IPS:-}" ]; then
    log "P2P unavailable: $*; scheduler IPs are empty, keeping the nydus containerd config"
    exit 1
  fi
  log "P2P unavailable: $*; using direct registry so RKE2 can start"
  write_direct_template
  if [ "$REAL_ROOT" = "1" ] && [ "${SKIP_SYSTEMD:-0}" != "1" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now dfdaemon-bootstrap.service dfdaemon-bootstrap-boot.service dfdaemon-bootstrap-switch.timer nydus-snapshotter-bootstrap.service nydus-updater.timer >/dev/null 2>&1 || true
  fi
  log "existing certificate/config files, if any, were preserved; P2P services are not enabled"
  exit 0
}

# The patched release currently publishes an amd64 asset only. Fall back to
# the direct registry so a non-amd64 node can still start RKE2.
NYDUS_SNAPSHOTTER_ARCH="${NYDUS_SNAPSHOTTER_ARCH:-$(uname -m)}"
case "$NYDUS_SNAPSHOTTER_ARCH" in
  amd64|x86_64) ;;
  *) fallback_direct "nydus-snapshotter has no ${NYDUS_SNAPSHOTTER_ARCH} release asset" ;;
esac

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
# dfdaemon 1.5.14 and the in-cluster client config. The operator's
# p2p-data-tls annotation does not apply to this pre-cluster bootstrap config.
write_file "${ROOT}${DFGET_CONFIG}" 0644 <<EOF
scheduler:
  caCert: "$CERT_DIR/ca.crt"
  cert: "$CERT_DIR/tls.crt"
  key: "$CERT_DIR/tls.key"
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
CRICTL="${CRICTL:-/var/lib/rancher/${DISTRO}/bin/crictl}"
# RKE2 and k3s both expose the containerd socket at /run/k3s.
SOCKET="${CONTAINERD_SOCKET:-/run/k3s/containerd/containerd.sock}"

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
  timeout 10 "$CRICTL" --runtime-endpoint "unix://${SOCKET}" "$@"
}

pod_has_running_container() {
  crictl_cmd ps --state running --pod "$1" -q 2>/dev/null | grep -q .
}

client_containers_running() {
  local pods pod
  [ -x "$CRICTL" ] || return 1

  pods="$(crictl_cmd pods --namespace "$NAMESPACE" --label app.kubernetes.io/name=client -q 2>/dev/null || true)"
  for pod in $pods; do
    pod_has_running_container "$pod" && return 0
  done

  pods="$(crictl_cmd pods --namespace "$NAMESPACE" --name '.*-data-client-.*' -q 2>/dev/null || true)"
  for pod in $pods; do
    pod_has_running_container "$pod" && return 0
  done

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
  client_containers_running && return 0
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
    log "in-cluster client is running; host bootstrap disabled"
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
# restarts the host unit when anything changed. The patched nydus-snapshotter
# binary comes from its GitHub release and is updated by rerunning this
# script, not by the static-store updater. It never touches Kubernetes.
{
  printf 'STATIC_URL=%q\n' "$STATIC_URL"
  printf 'NYDUS_VERSION=%q\n' "$NYDUS_VERSION"
} | write_file "${ROOT}${NYDUS_DIR}/updater.env" 0644

write_file "${BIN_DIR}/nydus-updater" 0755 <<'UPDATER'
#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

env_file="/etc/nydus/updater.env"
if [ ! -f "$env_file" ]; then
  echo "nydus-updater: missing ${env_file}" >&2
  exit 1
fi
# shellcheck disable=SC1090
. "$env_file"
: "${STATIC_URL:?STATIC_URL is required}"
: "${NYDUS_VERSION:?NYDUS_VERSION is required}"

bin_dir="/usr/local/bin"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# Fetch every asset first so a partial update never mixes versions.
for binary in nydusd nydus-image nydus-overlayfs; do
  fetched=0
  for url in "${STATIC_URL%/}/nydus/${NYDUS_VERSION}/${binary}" "${STATIC_URL%/}/bin/${binary}"; do
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 300 -o "$tmp/$binary" "$url" && [ -s "$tmp/$binary" ]; then
      fetched=1
      break
    fi
  done
  if [ "$fetched" != 1 ]; then
    echo "nydus-updater: failed to fetch ${binary} ${NYDUS_VERSION}" >&2
    exit 1
  fi
done

changed=0
for binary in nydusd nydus-image nydus-overlayfs; do
  if [ -x "${bin_dir}/${binary}" ] && cmp -s "$tmp/$binary" "${bin_dir}/${binary}"; then
    continue
  fi
  install -m 0755 "$tmp/$binary" "${bin_dir}/.${binary}.new"
  mv -f "${bin_dir}/.${binary}.new" "${bin_dir}/${binary}"
  changed=1
done
# Re-evaluate the driver even when the binaries did not change: a kernel
# upgrade can make fscache usable (or unusable). Restart only when the
# selection actually rewrote config.toml.
config_changed=0
if [ -x /usr/local/bin/nydus-fs-select ]; then
  config_before=""
  config_after=""
  if [ -f /etc/nydus/config.toml ]; then
    config_before="$(sha256sum /etc/nydus/config.toml | cut -d' ' -f1)"
  fi
  /usr/local/bin/nydus-fs-select
  if [ -f /etc/nydus/config.toml ]; then
    config_after="$(sha256sum /etc/nydus/config.toml | cut -d' ' -f1)"
  fi
  if [ "$config_before" != "$config_after" ]; then
    config_changed=1
  fi
fi

if [ "$changed" = 1 ] || [ "$config_changed" = 1 ]; then
  systemctl restart nydus-snapshotter-bootstrap.service
  systemctl is-active --quiet nydus-snapshotter-bootstrap.service || {
    echo "nydus-updater: nydus-snapshotter-bootstrap.service is not active after restart" >&2
    exit 1
  }
  echo "nydus-updater: updated to ${NYDUS_VERSION} and restarted the host snapshotter"
else
  echo "nydus-updater: ${NYDUS_VERSION} already installed; fs_driver selection unchanged"
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
  for binary in $NYDUS_BINARIES dfdaemon; do
    tmp="${BIN_DIR}/.${binary}.tmp.$$"
    download_nydus_asset "$binary" "$tmp" || fallback_direct "failed to download ${binary} ${NYDUS_VERSION}"
    chmod 0755 "$tmp"
    mv -f "$tmp" "${BIN_DIR}/${binary}"
  done
  snapshotter_tmp="${BIN_DIR}/.${NYDUS_SNAPSHOTTER_BINARY}.tmp.$$"
  if ! download_nydus_snapshotter_asset "$snapshotter_tmp"; then
    rm -f "$snapshotter_tmp" "${snapshotter_tmp}.sha256"
    fallback_direct "nydus-snapshotter release asset ${NYDUS_SNAPSHOTTER_ASSET} (${NYDUS_SNAPSHOTTER_VERSION}) is unavailable from ${NYDUS_SNAPSHOTTER_REPO}"
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
    systemctl restart dfdaemon-bootstrap.service nydus-snapshotter-bootstrap.service || \
      fallback_direct "failed to start bootstrap services"
    systemctl start nydus-updater.timer dfdaemon-bootstrap-switch.timer || \
      fallback_direct "failed to start host timers"
    sleep 2
    systemctl is-active --quiet dfdaemon-bootstrap.service || fallback_direct "dfdaemon bootstrap failed to start"
  else
    systemctl restart nydus-snapshotter-bootstrap.service || \
      fallback_direct "failed to start nydus bootstrap service"
    systemctl start nydus-updater.timer || \
      fallback_direct "failed to start host timers"
    sleep 2
  fi
  systemctl is-active --quiet nydus-snapshotter-bootstrap.service || fallback_direct "nydus bootstrap failed to start"
fi

log "certificates written to ${CERT_DIR}"
log "dfget.yaml written with scheduler cluster ID ${SCHEDULER_CLUSTER_ID}"
log "dynconfig written with scheduler ${SCHEDULER_HOST}:${SCHEDULER_PORT}"
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
