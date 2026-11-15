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
SCHEDULER_PORT=443
PREFLIGHT_TIMEOUT="${P2P_PREFLIGHT_TIMEOUT:-3}"
BIN_DIR="${ROOT}/usr/local/bin"
SERVICE_DIR="${ROOT}/etc/systemd/system"
RKE2_CONTAINERD_DIR="${ROOT}/var/lib/rancher/rke2/agent/etc/containerd"

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
  log "warning: containerd version not detected; using the v1 template"
  printf '1'
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
    MANAGER_GRPC|STATIC_URL|SCHEDULER_HOST|SCHEDULER_CLUSTER_ID|CA_CRT_B64|CLIENT_CRT_B64|CLIENT_KEY_B64)
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
  case "$major" in
    2) template="${RKE2_CONTAINERD_DIR}/config-v3.toml.tmpl" ;;
    *) template="${RKE2_CONTAINERD_DIR}/config.toml.tmpl" ;;
  esac
  mkdir -p "$RKE2_CONTAINERD_DIR"
  if [ "$major" = "2" ]; then
    write_file "$template" 0644 <<'TOML'
{{ template "base" . }}

# Direct-registry fallback: overlayfs only, no P2P snapshotter or proxy.
[plugins.'io.containerd.cri.v1.images']
  snapshotter = 'overlayfs'
TOML
  else
    write_file "$template" 0644 <<'TOML'
{{ template "base" . }}

# Direct-registry fallback: overlayfs only, no P2P snapshotter or proxy.
[plugins."io.containerd.grpc.v1.cri".containerd]
  snapshotter = "overlayfs"
TOML
  fi
  log "direct-registry containerd template written to ${template}"
}

fallback_direct() {
  log "P2P unavailable: $*; using direct registry so RKE2 can start"
  write_direct_template
  if [ "$REAL_ROOT" = "1" ] && [ "${SKIP_SYSTEMD:-0}" != "1" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now dfdaemon-bootstrap.service nydus-snapshotter-bootstrap.service >/dev/null 2>&1 || true
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
  "$BIN_DIR" \
  "$SERVICE_DIR" \
  "$RKE2_CONTAINERD_DIR" \
  "${ROOT}/var/lib/nydus/cache"

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

write_file "${ROOT}${NYDUS_DIR}/nydusd-config.json" 0644 <<'JSON'
{
  "device": {
    "backend": {
      "type": "proxy",
      "config": {
        "proxy": "http://127.0.0.1:4001",
        "fallback": true
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

CONTAINERD_MAJOR="${CONTAINERD_MAJOR:-$(detect_containerd_major)}"
case "$CONTAINERD_MAJOR" in
  2) TEMPLATE_PATH="${RKE2_CONTAINERD_DIR}/config-v3.toml.tmpl" ;;
  *) TEMPLATE_PATH="${RKE2_CONTAINERD_DIR}/config.toml.tmpl" ;;
esac

if [ "$CONTAINERD_MAJOR" = "2" ]; then
  write_file "$TEMPLATE_PATH" 0644 <<'TOML'
{{ template "base" . }}

# Keep overlayfs as the global snapshotter. Nydus is selected only through
# runtimeClass: nydus, so system pods remain on overlayfs.
[plugins.'io.containerd.cri.v1.images']
  snapshotter = 'overlayfs'
  disable_snapshot_annotations = false
  discard_unpacked_layers = false
  [plugins.'io.containerd.cri.v1.images'.runtime_platforms.nydus]
    snapshotter = 'nydus'

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.nydus]
  runtime_type = 'io.containerd.runc.v2'
  snapshotter = 'nydus'
  [plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.nydus.options]
    SystemdCgroup = true

[proxy_plugins.nydus]
  type = 'snapshot'
  address = '/run/containerd-nydus/containerd-nydus-grpc.sock'
TOML
else
  write_file "$TEMPLATE_PATH" 0644 <<'TOML'
{{ template "base" . }}

# Keep overlayfs as the global snapshotter. Nydus is selected only through
# runtimeClass: nydus, so system pods remain on overlayfs.
[plugins."io.containerd.grpc.v1.cri".containerd]
  snapshotter = "overlayfs"
  disable_snapshot_annotations = false
  discard_unpacked_layers = false
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.nydus]
    runtime_type = "io.containerd.runc.v2"
    snapshotter = "nydus"
    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.nydus.options]
      SystemdCgroup = true

[proxy_plugins.nydus]
  type = "snapshot"
  address = "/run/containerd-nydus/containerd-nydus-grpc.sock"
TOML
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

write_file "${SERVICE_DIR}/nydus-snapshotter-bootstrap.service" 0644 <<'UNIT'
[Unit]
Description=Dragonfly bootstrap nydus snapshotter
Documentation=https://d7y.io
After=network-online.target
Wants=network-online.target
Before=rke2-server.service rke2-agent.service
ConditionPathExists=/etc/nydus/nydusd-config.json

[Service]
Type=simple
ExecStart=/usr/local/bin/containerd-nydus-grpc --nydusd-path /usr/local/bin/nydusd --nydusd-config /etc/nydus/nydusd-config.json --root /var/lib/containerd-nydus --address /run/containerd-nydus/containerd-nydus-grpc.sock --log-level info
Restart=always
RestartSec=5
RuntimeDirectory=containerd-nydus
RuntimeDirectoryMode=0755
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT

if [ "${SKIP_DOWNLOAD:-0}" = "1" ]; then
  log "SKIP_DOWNLOAD=1: skipping binary downloads"
else
  command -v curl >/dev/null 2>&1 || fallback_direct "curl is required to download binaries"
  for binary in nydusd nydus-image containerd-nydus-grpc dfdaemon; do
    tmp="${BIN_DIR}/.${binary}.tmp.$$"
    curl -fsSL --retry 3 --connect-timeout 10 --max-time 300 \
      -o "$tmp" "${STATIC_URL}/bin/${binary}" || fallback_direct "failed to download ${binary}"
    if [ ! -s "$tmp" ]; then
      rm -f "$tmp"
      die "downloaded ${binary} is empty"
    fi
    chmod 0755 "$tmp"
    mv -f "$tmp" "${BIN_DIR}/${binary}"
  done
fi

if [ "${SKIP_DOWNLOAD:-0}" != "1" ]; then
  for binary in nydusd nydus-image containerd-nydus-grpc dfdaemon; do
    if [ ! -x "${BIN_DIR}/${binary}" ]; then
      fallback_direct "${binary} is not executable"
    fi
  done
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
  systemctl enable dfdaemon-bootstrap.service nydus-snapshotter-bootstrap.service || \
    fallback_direct "failed to enable bootstrap services"
  systemctl restart dfdaemon-bootstrap.service nydus-snapshotter-bootstrap.service || \
    fallback_direct "failed to start bootstrap services"
  sleep 2
  systemctl is-active --quiet dfdaemon-bootstrap.service || fallback_direct "dfdaemon bootstrap failed to start"
  systemctl is-active --quiet nydus-snapshotter-bootstrap.service || fallback_direct "nydus bootstrap failed to start"
fi

log "certificates written to ${CERT_DIR}"
log "dfget.yaml written with scheduler cluster ID ${SCHEDULER_CLUSTER_ID}"
log "dynconfig written with scheduler ${SCHEDULER_HOST}:${SCHEDULER_PORT}"
log "nydus configuration written to ${NYDUS_DIR}"
log "containerd template written to ${TEMPLATE_PATH}"
log "bootstrap services installed in ${SERVICE_DIR}"
