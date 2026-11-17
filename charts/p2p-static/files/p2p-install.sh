#!/usr/bin/env bash
# d7y-cattle-operator:p2p-install
# Bootstrap a new RKE2 node before the RKE2 services start.
set -Eeuo pipefail
IFS=$'\n\t'

P2P_ROOT="${P2P_ROOT:-/}"
REAL_ROOT=0
if [ "$P2P_ROOT" = "/" ]; then
  REAL_ROOT=1
fi
ROOT="${P2P_ROOT%/}"

CERT_DIR="/etc/dragonfly/certs"
DFGET_CONFIG="/etc/dragonfly/dfget.yaml"
DYNCONFIG="/etc/dragonfly/dynconfig.yaml"
NYDUS_DIR="/etc/nydus"
NYDUS_VERSION="${NYDUS_VERSION:-v0.16.0}"
NYDUS_BINARIES="containerd-nydus-grpc
nydusd
nydus-image
nydus-overlayfs"
RESOLVER_BINARY="nydus-resolver"
# Bump the default with the release tag, or override for a pinned tag/latest.
RESOLVER_VERSION="${RESOLVER_VERSION:-v0.1.33}"
RESOLVER_ASSET="${RESOLVER_ASSET:-nydus-resolver-linux-amd64}"
GH_REPO="${GH_REPO:-gorizond/d7y-cattle-operator}"
RESOLVER_CONFIG="/etc/nydus-resolver/config.yaml"
RESOLVER_CA_DIR="/var/lib/nydus-resolver"
RESOLVER_CA_KEY="${RESOLVER_CA_DIR}/ca.key"
RESOLVER_CA_BUNDLE="${RESOLVER_CA_DIR}/ca-bundle.pem"
SCHEDULER_PORT=443
PREFLIGHT_TIMEOUT="${P2P_PREFLIGHT_TIMEOUT:-3}"
BIN_DIR="${ROOT}/usr/local/bin"
SERVICE_DIR="${ROOT}/etc/systemd/system"
RKE2_CONTAINERD_DIR="${ROOT}/var/lib/rancher/rke2/agent/etc/containerd"
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
  candidate="${ROOT}/var/lib/rancher/rke2/bin/containerd"
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
  # New VMs usually run this script before RKE2 installs containerd. The
  # target RKE2 releases use containerd 2.x, so prefer the v3 drop-in path.
  log "warning: containerd version not detected; using the containerd 2.x v3 config"
  printf '2'
}

# Prefer the versioned nydus asset layout, then the legacy unversioned /bin
# layout. The static store is populated externally; both layouts are served by
# the p2p-static chart.
download_nydus_asset() {
  local name out url
  name="$1"
  out="$2"
  for url in "${STATIC_URL%/}/nydus/${NYDUS_VERSION}/${name}" "${STATIC_URL%/}/bin/${name}"; do
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 300 -o "$out" "$url"; then
      if [ ! -s "$out" ]; then
        die "downloaded ${name} is empty"
      fi
      return 0
    fi
  done
  return 1
}

resolver_release_url() {
  local base="https://github.com/${GH_REPO}/releases"
  if [ "$RESOLVER_VERSION" = "latest" ]; then
    printf '%s/latest/download/%s' "$base" "$RESOLVER_ASSET"
  else
    printf '%s/download/%s/%s' "$base" "$RESOLVER_VERSION" "$RESOLVER_ASSET"
  fi
}

# Download the resolver from the GitHub release and verify the sibling
# .sha256 asset when the release provides one. Returns nonzero only when the
# asset itself is unavailable; a checksum mismatch is fatal.
download_resolver_asset() {
  local out url checksum expected actual
  out="$1"
  url="$(resolver_release_url)"
  if ! curl -fL --retry 3 --connect-timeout 10 --max-time 300 -o "$out" "$url"; then
    return 1
  fi
  if [ ! -s "$out" ]; then
    return 1
  fi
  checksum="${out}.sha256"
  if curl -fL --retry 3 --connect-timeout 10 --max-time 300 -o "$checksum" "${url}.sha256" && [ -s "$checksum" ]; then
    command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required to verify ${RESOLVER_ASSET}"
    IFS=' ' read -r expected _ < "$checksum"
    actual="$(sha256sum "$out")"
    actual="${actual%% *}"
    if [ -z "$expected" ] || [ "$actual" != "$expected" ]; then
      die "checksum verification failed for ${url}"
    fi
  fi
  rm -f "$checksum"
  return 0
}

# The nginx sub_filter replaces the single placeholder below with $arg_cfg.
# Keep exactly one placeholder so sub_filter_once produces one payload insertion.
if [ "$#" -ge 1 ] && [ -n "${1:-}" ]; then
  CFG="$1"
elif [ -n "${QUERY_STRING:-}" ]; then
  CFG="${QUERY_STRING##*cfg=}"
  CFG="${CFG%%&*}"
else
  CFG="__CFG__"
fi

if [ -z "$CFG" ]; then
  die "missing cfg payload"
fi
if ! printf '%s' "$CFG" | tr '_-' '/+' | base64 -d > "$TMP/payload.env" 2>/dev/null; then
  die "cfg is not valid base64url"
fi
if [ ! -s "$TMP/payload.env" ]; then
  die "cfg decoded to an empty payload"
fi

while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in
    ""|\#*) continue ;;
  esac
  key="${line%%=*}"
  value="${line#*=}"
  value="${value%$'\r'}"
  case "$key" in
    MANAGER_GRPC|STATIC_URL|SMART_PROXY_URL|RESOLVER_UPSTREAM|RESOLVER_LISTEN|SCHEDULER_HOST|SCHEDULER_CLUSTER_ID|CA_CRT_B64|CLIENT_CRT_B64|CLIENT_KEY_B64)
      printf -v "$key" '%s' "$value"
      ;;
  esac
done < "$TMP/payload.env"

for key in MANAGER_GRPC STATIC_URL SCHEDULER_HOST SCHEDULER_CLUSTER_ID CA_CRT_B64 CLIENT_CRT_B64 CLIENT_KEY_B64; do
  if [ -z "${!key:-}" ]; then
    die "cfg is missing $key"
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
  *) MANAGER_ADDR="${MANAGER_ADDR}:65003" ;;
esac

case "$STATIC_URL" in
  http://*|https://*) ;;
  *) die "static_url must use http:// or https://" ;;
esac
case "$STATIC_URL" in
  *" "*|*$'\t'*) die "static_url contains whitespace" ;;
esac
STATIC_URL="${STATIC_URL%/}"

RESOLVER_UPSTREAM="${RESOLVER_UPSTREAM:-}"
RESOLVER_LISTEN="${RESOLVER_LISTEN:-}"
if [ -n "$RESOLVER_UPSTREAM" ] || [ -n "$RESOLVER_LISTEN" ]; then
  if [ -z "$RESOLVER_UPSTREAM" ] || [ -z "$RESOLVER_LISTEN" ]; then
    die "resolver_upstream and resolver_listen must be set together"
  fi
  case "$RESOLVER_UPSTREAM" in
    https://*) ;;
    *) die "resolver_upstream must use https://" ;;
  esac
  case "$RESOLVER_UPSTREAM$RESOLVER_LISTEN" in
    *" "*|*$'\t'*|*'"'*|*'\'*|*'$'*|*'`'*) die "resolver config contains unsupported characters" ;;
  esac
  case "$RESOLVER_UPSTREAM" in
    *"?"*|*"@"*) die "resolver_upstream must not contain a query or credentials" ;;
  esac
  case "$RESOLVER_LISTEN" in
    ""|*[!A-Za-z0-9.:\[\]]*) die "resolver_listen is not an IP and port" ;;
  esac
  case "$RESOLVER_LISTEN" in
    *:*) ;;
    *) die "resolver_listen must include a port" ;;
  esac
fi

case "$RESOLVER_ASSET" in
  ""|*[!A-Za-z0-9._-]*) die "resolver_asset is not a valid release asset name" ;;
esac
case "$GH_REPO" in
  ""|*[!A-Za-z0-9._/-]*|*/*/*) die "gh_repo must be owner/repo" ;;
esac
case "$GH_REPO" in
  */*) ;;
  *) die "gh_repo must be owner/repo" ;;
esac
if [ "$RESOLVER_VERSION" != "latest" ]; then
  case "$RESOLVER_VERSION" in
    v[A-Za-z0-9._-]*) ;;
    *) die "resolver_version must be latest or a v* release tag" ;;
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

case "$SCHEDULER_HOST" in
  ""|*[!A-Za-z0-9.-]*|*..*|.*|*.) die "scheduler_host is not a DNS name" ;;
esac

case "$SCHEDULER_CLUSTER_ID" in
  ""|*[!0-9]*) die "scheduler_cluster_id must be a positive integer" ;;
esac
if [ "$SCHEDULER_CLUSTER_ID" -lt 1 ] 2>/dev/null; then
  die "scheduler_cluster_id must be a positive integer"
fi

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
  log "P2P unavailable: $*; using direct registry so RKE2 can start"
  write_direct_template
  if [ "$REAL_ROOT" = "1" ] && [ "${SKIP_SYSTEMD:-0}" != "1" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now dfdaemon-bootstrap.service dfdaemon-bootstrap-boot.service dfdaemon-bootstrap-switch.timer nydus-resolver.service nydus-snapshotter-bootstrap.service nydus-updater.timer >/dev/null 2>&1 || true
  fi
  log "existing certificate/config files, if any, were preserved; P2P services are not enabled"
  exit 0
}

# The nydus-resolver binary is required by the host snapshotter when nydus is
# enabled. Its config is supplied through the bootstrap payload; missing
# config falls back to the direct registry so RKE2 can still start.
RESOLVER_ARCH="${RESOLVER_ARCH:-$(uname -m)}"
case "$RESOLVER_ARCH" in
  amd64|x86_64) ;;
  *) fallback_direct "nydus-resolver has no ${RESOLVER_ARCH} asset yet" ;;
esac
if [ -z "$RESOLVER_UPSTREAM" ] || [ -z "$RESOLVER_LISTEN" ]; then
  fallback_direct "nydus-resolver config is missing RESOLVER_UPSTREAM or RESOLVER_LISTEN"
fi

SYSTEM_CA_FILE="${SYSTEM_CA_FILE:-}"
if [ -z "$SYSTEM_CA_FILE" ]; then
  for candidate in \
    /etc/ssl/certs/ca-certificates.crt \
    /etc/pki/tls/certs/ca-bundle.crt \
    /etc/ssl/ca-bundle.pem \
    /etc/ssl/cert.pem; do
    if [ -s "$candidate" ]; then
      SYSTEM_CA_FILE="$candidate"
      break
    fi
  done
fi
if [ -z "$SYSTEM_CA_FILE" ] || [ ! -s "$SYSTEM_CA_FILE" ]; then
  fallback_direct "system CA bundle not found for the resolver"
fi
case "$SYSTEM_CA_FILE" in
  *'"'*|*'\'*|*'$'*|*'`'*) die "system CA path contains unsupported characters" ;;
esac

tcp_check() {
  local host port
  host="$1"
  port="$2"
  timeout "$PREFLIGHT_TIMEOUT" bash -c "exec 3<>/dev/tcp/${host}/${port}" >/dev/null 2>&1
}

preflight() {
  local manager_host manager_port
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
  if command -v getent >/dev/null 2>&1 && ! getent ahosts "$SCHEDULER_HOST" >/dev/null 2>&1; then
    fallback_direct "scheduler DNS ${SCHEDULER_HOST} did not resolve"
  fi
  if ! tcp_check "$SCHEDULER_HOST" "$SCHEDULER_PORT"; then
    fallback_direct "scheduler ${SCHEDULER_HOST}:${SCHEDULER_PORT} is unreachable"
  fi
  log "preflight ok: manager ${manager_host}:${manager_port}, scheduler ${SCHEDULER_HOST}:${SCHEDULER_PORT}"
}

preflight

mkdir -p \
  "${ROOT}${CERT_DIR}" \
  "${ROOT}${NYDUS_DIR}" \
  "${ROOT}${RESOLVER_CONFIG%/*}" \
  "${ROOT}${RESOLVER_CA_DIR}" \
  "$BIN_DIR" \
  "$SERVICE_DIR" \
  "$RKE2_CONTAINERD_DIR" \
  "${ROOT}/var/lib/nydus/cache" \
  "${ROOT}/var/lib/containerd-nydus" \
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

# P2P data upload is intentionally plaintext here; only the scheduler control
# connection keeps TLS. The operator's p2p-data-tls annotation does not apply
# to this pre-cluster bootstrap config.
write_file "${ROOT}${DFGET_CONFIG}" 0644 <<EOF
scheduler:
  tls:
    caCert: "$CERT_DIR/ca.crt"
    cert: "$CERT_DIR/tls.crt"
    key: "$CERT_DIR/tls.key"
    tlsVerify: true
    tlsPolicy: "force"
    autoIssueCert: false
host:
  schedulerClusterID: $SCHEDULER_CLUSTER_ID
EOF

write_file "${ROOT}${DYNCONFIG}" 0644 <<EOF
scheduler:
  addr: "$SCHEDULER_HOST:$SCHEDULER_PORT"
EOF

write_file "${ROOT}${NYDUS_DIR}/config.toml" 0644 <<'TOML'
version = 1
root = "/var/lib/containerd-nydus"
address = "/run/containerd-nydus/containerd-nydus-grpc.sock"
daemon_mode = "multiple"

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

# fs_prefetch is the documented nydusd prefetch/readahead control. Avoid
# rafs.prefetch/rafs.readahead here: the pinned nydusd schema does not document
# those keys, and unknown fields can make nydusd fail to start.
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
          "use_http": false
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

write_file "${ROOT}${RESOLVER_CONFIG}" 0644 <<EOF
listen: "$RESOLVER_LISTEN"
upstream: "$RESOLVER_UPSTREAM"
ca_key: "$RESOLVER_CA_KEY"
ca_bundle: "$RESOLVER_CA_BUNDLE"
system_ca_file: "$SYSTEM_CA_FILE"
EOF

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
  use_local_image_pull = true
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

write_file "${SERVICE_DIR}/dfdaemon-bootstrap.service" 0644 <<'UNIT'
[Unit]
Description=Dragonfly bootstrap dfdaemon
Documentation=https://d7y.io
After=network-online.target
Wants=network-online.target
Before=rke2-server.service rke2-agent.service
ConditionPathExists=/etc/dragonfly/dfget.yaml

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
write_file "${SERVICE_DIR}/dfdaemon-bootstrap-boot.service" 0644 <<'UNIT'
[Unit]
Description=Start host dfdaemon bootstrap before RKE2
Documentation=https://d7y.io
After=local-fs.target
Wants=dfdaemon-bootstrap.service
Before=dfdaemon-bootstrap.service rke2-server.service rke2-agent.service
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
CRICTL="${CRICTL:-/var/lib/rancher/rke2/bin/crictl}"
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

write_file "${SERVICE_DIR}/nydus-resolver.service" 0644 <<'UNIT'
[Unit]
Description=Nydus resolver HTTPS forward proxy
Documentation=https://d7y.io
After=network-online.target
Wants=network-online.target
Before=nydus-snapshotter-bootstrap.service rke2-server.service rke2-agent.service
ConditionPathExists=/etc/nydus-resolver/config.yaml

[Service]
Type=simple
Environment=PATH=/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/usr/local/bin/nydus-resolver --config /etc/nydus-resolver/config.yaml
Restart=always
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/nydus-resolver
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6

[Install]
WantedBy=multi-user.target
UNIT

write_file "${SERVICE_DIR}/nydus-snapshotter-bootstrap.service" 0644 <<'UNIT'
[Unit]
Description=Dragonfly host nydus snapshotter
Documentation=https://d7y.io
After=network-online.target nydus-resolver.service
Wants=network-online.target nydus-resolver.service
Before=rke2-server.service rke2-agent.service
ConditionPathExists=/etc/nydus/config.toml

[Service]
Type=simple
Environment=PATH=/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin
Environment=HTTPS_PROXY=http://127.0.0.1:3999
Environment=NO_PROXY=127.0.0.1,localhost,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,.svc,.cluster.local
Environment=SSL_CERT_FILE=/var/lib/nydus-resolver/ca-bundle.pem
ExecStart=/usr/local/bin/containerd-nydus-grpc --config /etc/nydus/config.toml --nydusd /usr/local/bin/nydusd --nydusd-config /etc/nydus/nydusd-config.json
ExecStartPost=/bin/sh -c 'i=0; while [ ! -S /run/containerd-nydus/containerd-nydus-grpc.sock ]; do i=$((i + 1)); [ "$i" -ge 60 ] && exit 1; sleep 1; done'
Restart=always
RestartSec=5
RuntimeDirectory=containerd-nydus
RuntimeDirectoryMode=0755
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT

# Host-only updater: keeps the pinned nydus binaries current and restarts the
# host unit when anything changed. nydus-resolver is installed from the GitHub
# release by p2p-install.sh, not by this static-store updater.
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
for binary in containerd-nydus-grpc nydusd nydus-image nydus-overlayfs; do
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
for binary in containerd-nydus-grpc nydusd nydus-image nydus-overlayfs; do
  if [ -x "${bin_dir}/${binary}" ] && cmp -s "$tmp/$binary" "${bin_dir}/${binary}"; then
    continue
  fi
  install -m 0755 "$tmp/$binary" "${bin_dir}/.${binary}.new"
  mv -f "${bin_dir}/.${binary}.new" "${bin_dir}/${binary}"
  changed=1
done
if [ "$changed" = 1 ]; then
  systemctl restart nydus-snapshotter-bootstrap.service
  systemctl is-active --quiet nydus-snapshotter-bootstrap.service || {
    echo "nydus-updater: nydus-snapshotter-bootstrap.service is not active after restart" >&2
    exit 1
  }
  echo "nydus-updater: updated to ${NYDUS_VERSION} and restarted the host snapshotter"
else
  echo "nydus-updater: ${NYDUS_VERSION} already installed"
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
  log "SKIP_DOWNLOAD=1: skipping binary downloads (pinned nydus ${NYDUS_VERSION})"
else
  command -v curl >/dev/null 2>&1 || fallback_direct "curl is required to download binaries"
  for binary in $NYDUS_BINARIES dfdaemon; do
    tmp="${BIN_DIR}/.${binary}.tmp.$$"
    download_nydus_asset "$binary" "$tmp" || fallback_direct "failed to download ${binary} ${NYDUS_VERSION}"
    chmod 0755 "$tmp"
    mv -f "$tmp" "${BIN_DIR}/${binary}"
  done
  resolver_tmp="${BIN_DIR}/.${RESOLVER_BINARY}.tmp.$$"
  if ! download_resolver_asset "$resolver_tmp"; then
    rm -f "$resolver_tmp" "${resolver_tmp}.sha256"
    fallback_direct "nydus-resolver release asset ${RESOLVER_ASSET} (${RESOLVER_VERSION}) is unavailable from ${GH_REPO}"
  fi
  chmod 0755 "$resolver_tmp"
  mv -f "$resolver_tmp" "${BIN_DIR}/${RESOLVER_BINARY}"
fi

if [ "${SKIP_DOWNLOAD:-0}" != "1" ]; then
  for binary in $NYDUS_BINARIES dfdaemon; do
    if [ ! -x "${BIN_DIR}/${binary}" ]; then
      fallback_direct "${binary} is not executable"
    fi
  done
  if [ ! -x "${BIN_DIR}/${RESOLVER_BINARY}" ]; then
    fallback_direct "nydus-resolver is not executable after install"
  fi
fi

if [ "$REAL_ROOT" = "1" ] && [ "${SKIP_MODULES:-0}" != "1" ]; then
  if command -v modprobe >/dev/null 2>&1; then
    for module in fuse erofs; do
      if ! modprobe "$module" 2>/dev/null; then
        log "warning: could not load module ${module}"
      fi
    done
  else
    log "warning: modprobe is not available"
  fi
fi

if [ "$REAL_ROOT" = "1" ] && [ "${SKIP_SYSTEMD:-0}" != "1" ]; then
  command -v systemctl >/dev/null 2>&1 || fallback_direct "systemctl is required"
  systemctl daemon-reload || fallback_direct "systemctl daemon-reload failed"
  systemctl enable dfdaemon-bootstrap.service dfdaemon-bootstrap-boot.service nydus-resolver.service nydus-snapshotter-bootstrap.service nydus-updater.timer dfdaemon-bootstrap-switch.timer || \
    fallback_direct "failed to enable bootstrap services"
  systemctl restart nydus-resolver.service || \
    fallback_direct "nydus resolver failed to start"
  systemctl restart dfdaemon-bootstrap.service nydus-snapshotter-bootstrap.service || \
    fallback_direct "failed to start bootstrap services"
  systemctl start nydus-updater.timer dfdaemon-bootstrap-switch.timer || \
    fallback_direct "failed to start host timers"
  sleep 2
  systemctl is-active --quiet nydus-resolver.service || fallback_direct "nydus resolver is not active"
  systemctl is-active --quiet dfdaemon-bootstrap.service || fallback_direct "dfdaemon bootstrap failed to start"
  systemctl is-active --quiet nydus-snapshotter-bootstrap.service || fallback_direct "nydus bootstrap failed to start"
fi

log "certificates written to ${CERT_DIR}"
log "dfget.yaml written with scheduler cluster ID ${SCHEDULER_CLUSTER_ID}"
log "dynconfig written with scheduler ${SCHEDULER_HOST}:${SCHEDULER_PORT}"
log "nydus ${NYDUS_VERSION} configuration written to ${NYDUS_DIR}"
log "nydus-resolver config written to ${RESOLVER_CONFIG} (CA bundle ${RESOLVER_CA_BUNDLE})"
log "containerd nydus configuration written to ${CONTAINERD_CONFIG_PATH}"
log "host bootstrap services, nydus updater, and dfdaemon switch installed in ${SERVICE_DIR}"
