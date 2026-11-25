#!/usr/bin/env bash
# Initialize Dragonfly/Nydus inside an unprivileged GPU container.
#
# Installs dfdaemon, dfget and nydusd, writes mTLS configs, starts dfdaemon and
# waits for its health endpoint. It does not configure networking and does not
# mount model images: mounting is the container entrypoint's step after init.
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

P2P_ROOT="${P2P_ROOT:-/}"
P2P_BIN_DIR="${P2P_BIN_DIR:-/usr/local/bin}"
DRAGONFLY_CLIENT_VERSION="${DRAGONFLY_CLIENT_VERSION:-v1.5.14}"
NYDUS_VERSION="${NYDUS_VERSION:-v2.4.5}"
DFDAEMON_HEALTH_TIMEOUT="${DFDAEMON_HEALTH_TIMEOUT:-60}"
P2P_PREFLIGHT_TIMEOUT="${P2P_PREFLIGHT_TIMEOUT:-3}"
SKIP_PREFLIGHT="${SKIP_PREFLIGHT:-0}"

SCHEDULER_PORT=8002
PROXY_PORT=4001
HEALTH_PORT=4003
METRICS_PORT=4002

CERT_DIR="/etc/dragonfly/certs"
DRAGONFLY_DIR="/etc/dragonfly"
DFDAEMON_CONFIG="/etc/dragonfly/dfdaemon.yaml"
DYNCONFIG="/etc/dragonfly/dynconfig.yaml"
NYDUS_CONFIG="/etc/nydus/config.json"
LOG_FILE="/var/log/dfdaemon.log"
RUN_DIR="/var/run/dragonfly"
PID_FILE="${RUN_DIR}/dfdaemon.pid"
LOCK_DIR="${RUN_DIR}/dfdaemon.lock"

ROOT=""
ARCH=""
MANAGER_HOST=""
MANAGER_PORT=""
AUTH_B64=""
REGISTRY_CA_PRESENT=0
BIN_PATH=""
CERT_ROOT=""
DRAGONFLY_ROOT=""
DFDAEMON_CONFIG_PATH=""
DYNCONFIG_PATH=""
NYDUS_CONFIG_PATH=""
LOG_PATH=""
RUN_PATH=""
PID_PATH=""
LOCK_PATH=""
DIGEST_PATH=""
LOCK_HELD=0

TMP="$(mktemp -d)"

cleanup() {
  rm -rf "$TMP"
  if [ "$LOCK_HELD" -eq 1 ] && [ -n "$LOCK_PATH" ]; then
    rm -f "${LOCK_PATH}/pid"
    rmdir "$LOCK_PATH" 2>/dev/null || true
  fi
}
trap cleanup EXIT

log() {
  printf 'container-init: %s\n' "$*" >&2
}

die() {
  log "ERROR: $*"
  exit 1
}

die_code() {
  local code="$1"
  shift
  log "ERROR: $*"
  exit "$code"
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

require_env() {
  local name="$1"
  if [ -z "${!name:-}" ]; then
    die "required environment variable ${name} is not set"
  fi
}

validate_sha256() {
  local name="$1" value="$2"
  [ -n "$value" ] || return 0
  case "$value" in
    *[!0-9a-fA-F]*) die "${name} must be a 64-character hex SHA-256" ;;
  esac
  if [ "${#value}" -ne 64 ]; then
    die "${name} must be a 64-character hex SHA-256"
  fi
}

validate_http_url() {
  local name="$1" value="$2" unsafe="${3:-0}"
  case "$value" in
    http://*|https://*) ;;
    *) die "${name} must start with http:// or https://" ;;
  esac
  case "$value" in
    *" "*|*$'\t'*|*$'\n'*|*$'\r'*)
      die "${name} must not contain whitespace" ;;
  esac
  if [ "$unsafe" = 1 ]; then
    case "$value" in
      *'"'*|*'\'*|*"?"*|*"#"*|*"@"*)
        die "${name} contains an unsupported character" ;;
    esac
  fi
}

validate_env() {
  local key

  for key in STATIC_URL MANAGER_GRPC SCHEDULER_CLUSTER_ID \
    CA_CRT_B64 CLIENT_CRT_B64 CLIENT_KEY_B64; do
    require_env "$key"
  done

  validate_http_url STATIC_URL "$STATIC_URL" 1
  STATIC_URL="${STATIC_URL%/}"

  case "$MANAGER_GRPC" in
    https://*) ;;
    *) die "MANAGER_GRPC must use https://" ;;
  esac
  case "$MANAGER_GRPC" in
    *" "*|*$'\t'*|*$'\n'*|*$'\r'*)
      die "MANAGER_GRPC must not contain whitespace" ;;
  esac

  case "$SCHEDULER_CLUSTER_ID" in
    ""|*[!0-9]*) die "SCHEDULER_CLUSTER_ID must be a non-negative integer" ;;
  esac

  case "$DRAGONFLY_CLIENT_VERSION" in
    v[A-Za-z0-9._-][A-Za-z0-9._-]*) ;;
    *) die "DRAGONFLY_CLIENT_VERSION must look like v1.5.14" ;;
  esac
  case "$NYDUS_VERSION" in
    v[A-Za-z0-9._-][A-Za-z0-9._-]*) ;;
    *) die "NYDUS_VERSION must look like v2.4.5" ;;
  esac

  if [ -n "${SMART_PROXY_URL:-}" ]; then
    validate_http_url SMART_PROXY_URL "$SMART_PROXY_URL" 1
    SMART_PROXY_URL="${SMART_PROXY_URL%/}"
  fi
  if [ -n "${NYDUS_MIRROR_URL:-}" ]; then
    validate_http_url NYDUS_MIRROR_URL "$NYDUS_MIRROR_URL" 1
    if [[ ! "$NYDUS_MIRROR_URL" =~ ^https?://[^/]+/.+ ]]; then
      die "NYDUS_MIRROR_URL must include a registry mirror path (for example /docker.io)"
    fi
    NYDUS_MIRROR_URL="${NYDUS_MIRROR_URL%/}"
  fi

  if [ -n "${SCHEDULER_IPS:-}" ]; then
    case "$SCHEDULER_IPS" in
      *$'\n'*|*$'\r'*|*$'\t'*)
        die "SCHEDULER_IPS must not contain tabs or newlines" ;;
    esac
  fi

  validate_sha256 DFDAEMON_SHA256 "${DFDAEMON_SHA256:-}"
  validate_sha256 DFGET_SHA256 "${DFGET_SHA256:-}"
  validate_sha256 NYDUS_SHA256 "${NYDUS_SHA256:-}"
  DFDAEMON_SHA256="$(printf '%s' "${DFDAEMON_SHA256:-}" | tr 'A-F' 'a-f')"
  DFGET_SHA256="$(printf '%s' "${DFGET_SHA256:-}" | tr 'A-F' 'a-f')"
  NYDUS_SHA256="$(printf '%s' "${NYDUS_SHA256:-}" | tr 'A-F' 'a-f')"

  case "${REGISTRY_USER:-}" in
    *$'\n'*|*$'\r'*) die "REGISTRY_USER must not contain newlines" ;;
  esac
  case "${REGISTRY_PASSWORD:-}" in
    *$'\n'*|*$'\r'*) die "REGISTRY_PASSWORD must not contain newlines" ;;
  esac
  if [ -n "${REGISTRY_AUTH_B64:-}" ]; then
    case "$REGISTRY_AUTH_B64" in
      *$'\n'*|*$'\r'*) die "REGISTRY_AUTH_B64 must not contain newlines" ;;
    esac
  fi

  case "$SKIP_PREFLIGHT" in
    0|1) ;;
    *) die "SKIP_PREFLIGHT must be 0 or 1" ;;
  esac
  case "$DFDAEMON_HEALTH_TIMEOUT" in
    ""|*[!0-9]*) die "DFDAEMON_HEALTH_TIMEOUT must be a positive integer" ;;
  esac
  if [ "$DFDAEMON_HEALTH_TIMEOUT" -le 0 ]; then
    die "DFDAEMON_HEALTH_TIMEOUT must be a positive integer"
  fi
  case "$P2P_PREFLIGHT_TIMEOUT" in
    ""|*[!0-9]*) die "P2P_PREFLIGHT_TIMEOUT must be a positive integer" ;;
  esac
  if [ "$P2P_PREFLIGHT_TIMEOUT" -le 0 ]; then
    die "P2P_PREFLIGHT_TIMEOUT must be a positive integer"
  fi
}

resolve_manager() {
  local hostport
  hostport="${MANAGER_GRPC#https://}"
  hostport="${hostport%%/*}"
  case "$hostport" in
    ""|*[!A-Za-z0-9.:\[\]-]*)
      die "MANAGER_GRPC has an invalid host" ;;
  esac
  case "$hostport" in
    \[*\]:*)
      MANAGER_HOST="${hostport%:*}"
      MANAGER_PORT="${hostport##*:}"
      ;;
    \[*\])
      MANAGER_HOST="$hostport"
      MANAGER_PORT=443
      ;;
    *:*)
      MANAGER_HOST="${hostport%:*}"
      MANAGER_PORT="${hostport##*:}"
      ;;
    *)
      MANAGER_HOST="$hostport"
      MANAGER_PORT=443
      ;;
  esac
  if [ -z "$MANAGER_HOST" ]; then
    die "MANAGER_GRPC has an invalid host"
  fi
  case "$MANAGER_PORT" in
    ""|*[!0-9]*) die "MANAGER_GRPC has an invalid port" ;;
  esac
  if [ "$MANAGER_PORT" -lt 1 ] || [ "$MANAGER_PORT" -gt 65535 ]; then
    die "MANAGER_GRPC has an invalid port"
  fi
}

resolve_arch() {
  local machine="${TARGET_ARCH:-$(uname -m)}"
  case "$machine" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *)
      die "unsupported architecture ${machine}: expected x86_64/amd64 or aarch64/arm64"
      ;;
  esac
}

setup_paths() {
  case "$P2P_ROOT" in
    /*) ;;
    *) die "P2P_ROOT must be an absolute path" ;;
  esac
  case "$P2P_ROOT" in
    *" "*|*$'\t'*|*$'\n'*|*$'\r'*|*'"'*|*'\'*)
      die "P2P_ROOT contains an unsupported character" ;;
  esac
  case "$P2P_BIN_DIR" in
    /*) ;;
    *) die "P2P_BIN_DIR must be an absolute path" ;;
  esac
  case "$P2P_BIN_DIR" in
    *" "*|*$'\t'*|*$'\n'*|*$'\r'*|*'"'*|*'\'*)
      die "P2P_BIN_DIR contains an unsupported character" ;;
  esac

  ROOT="${P2P_ROOT%/}"
  case "$P2P_BIN_DIR" in
    "$ROOT"/*) BIN_PATH="$P2P_BIN_DIR" ;;
    *) BIN_PATH="${ROOT}${P2P_BIN_DIR}" ;;
  esac

  CERT_ROOT="${ROOT}${CERT_DIR}"
  DRAGONFLY_ROOT="${ROOT}${DRAGONFLY_DIR}"
  DFDAEMON_CONFIG_PATH="${ROOT}${DFDAEMON_CONFIG}"
  DYNCONFIG_PATH="${ROOT}${DYNCONFIG}"
  NYDUS_CONFIG_PATH="${ROOT}${NYDUS_CONFIG}"
  LOG_PATH="${ROOT}${LOG_FILE}"
  RUN_PATH="${ROOT}${RUN_DIR}"
  PID_PATH="${ROOT}${PID_FILE}"
  LOCK_PATH="${ROOT}${LOCK_DIR}"
  DIGEST_PATH="${RUN_PATH}/dfdaemon.config.sha256"
}

create_dirs() {
  if ! mkdir -p \
    "$BIN_PATH" \
    "$CERT_ROOT" \
    "$DRAGONFLY_ROOT" \
    "${ROOT}/etc/nydus" \
    "${ROOT}/var/cache/nydus" \
    "${ROOT}/var/log" \
    "$RUN_PATH"; then
    die "cannot create required directories under ${P2P_ROOT}: run as root inside the container or point P2P_ROOT/P2P_BIN_DIR at writable paths"
  fi
}

decode_b64() {
  local name="$1" value="$2" out="$3"
  if ! printf '%s' "$value" | base64 -d > "$out" 2>/dev/null; then
    die "${name} is not valid base64"
  fi
  if [ ! -s "$out" ]; then
    die "${name} decoded to an empty value"
  fi
}

install_certs() {
  decode_b64 CA_CRT_B64 "$CA_CRT_B64" "$TMP/ca.crt"
  decode_b64 CLIENT_CRT_B64 "$CLIENT_CRT_B64" "$TMP/tls.crt"
  decode_b64 CLIENT_KEY_B64 "$CLIENT_KEY_B64" "$TMP/tls.key"
  if [ -n "${REGISTRY_CA_B64:-}" ]; then
    decode_b64 REGISTRY_CA_B64 "$REGISTRY_CA_B64" "$TMP/registry-ca.crt"
  fi

  write_file "${CERT_ROOT}/ca.crt" 0644 < "$TMP/ca.crt"
  write_file "${CERT_ROOT}/tls.crt" 0644 < "$TMP/tls.crt"
  write_file "${CERT_ROOT}/tls.key" 0600 < "$TMP/tls.key"
  if [ -f "$TMP/registry-ca.crt" ]; then
    write_file "${CERT_ROOT}/registry-ca.crt" 0644 < "$TMP/registry-ca.crt"
    REGISTRY_CA_PRESENT=1
  else
    rm -f "${CERT_ROOT}/registry-ca.crt"
    REGISTRY_CA_PRESENT=0
  fi
}

prepare_registry_auth() {
  local value
  AUTH_B64=""
  if [ -n "${REGISTRY_AUTH_B64:-}" ]; then
    value="$(printf '%s' "$REGISTRY_AUTH_B64" | tr -d '[:space:]')"
    decode_b64 REGISTRY_AUTH_B64 "$value" "$TMP/registry-auth"
    if ! grep -q ':' "$TMP/registry-auth"; then
      die "REGISTRY_AUTH_B64 must decode to a username:password pair"
    fi
    AUTH_B64="$value"
    return 0
  fi
  if [ -n "${REGISTRY_USER:-}" ] || [ -n "${REGISTRY_PASSWORD:-}" ]; then
    if [ -z "${REGISTRY_USER:-}" ] || [ -z "${REGISTRY_PASSWORD:-}" ]; then
      die "REGISTRY_USER and REGISTRY_PASSWORD must be set together (or use REGISTRY_AUTH_B64)"
    fi
    AUTH_B64="$(printf '%s:%s' "$REGISTRY_USER" "$REGISTRY_PASSWORD" | base64 | tr -d '\r\n')"
  fi
}

sha256_of() {
  local out
  if command -v sha256sum >/dev/null 2>&1; then
    out="$(sha256sum "$1")"
  elif command -v shasum >/dev/null 2>&1; then
    out="$(shasum -a 256 "$1")"
  else
    die "sha256sum or shasum is required for checksum verification"
  fi
  printf '%s' "${out%% *}"
}

verify_sha256() {
  local file="$1" expected="$2" actual
  actual="$(sha256_of "$file")"
  [ "$actual" = "$expected" ]
}

# Digest of the dfdaemon inputs that must be applied on rerun.
# Nydus/dynconfig changes are intentionally not part of it: nydus reads them
# per mount, dfdaemon reads its own config and TLS material only at startup.
compute_config_digest() {
  local combined="$TMP/config-digest.bin" file
  local -a files=(
    "$DFDAEMON_CONFIG_PATH"
    "${CERT_ROOT}/ca.crt"
    "${CERT_ROOT}/tls.crt"
    "${CERT_ROOT}/tls.key"
  )
  if [ -f "${CERT_ROOT}/registry-ca.crt" ]; then
    files+=("${CERT_ROOT}/registry-ca.crt")
  fi
  : > "$combined"
  for file in "${files[@]}"; do
    [ -f "$file" ] || die "missing file for config digest: ${file}"
    cat "$file" >> "$combined"
  done
  sha256_of "$combined"
}

fetch() {
  local url="$1" dest="$2" expected="${3:-}" tmp
  tmp="${dest}.tmp.$$"
  log "downloading ${url}"
  if ! curl -fsSL --retry 3 --connect-timeout 10 --max-time 300 -o "$tmp" "$url"; then
    rm -f "$tmp"
    die "download failed: ${url}"
  fi
  if [ ! -s "$tmp" ]; then
    rm -f "$tmp"
    die "download produced an empty file: ${url}"
  fi
  if [ -n "$expected" ]; then
    if ! verify_sha256 "$tmp" "$expected"; then
      rm -f "$tmp"
      die "SHA-256 mismatch for ${url}"
    fi
  else
    log "no SHA-256 provided for ${url}; skipping checksum verification"
  fi
  chmod 0755 "$tmp"
  mv -f "$tmp" "$dest"
}

install_binaries() {
  local client_base nydus_base
  client_base="${STATIC_URL}/client/${DRAGONFLY_CLIENT_VERSION}/${ARCH}"
  nydus_base="${STATIC_URL}/nydus/${NYDUS_VERSION}/${ARCH}"

  fetch "${client_base}/dfdaemon" "${BIN_PATH}/dfdaemon" "${DFDAEMON_SHA256:-}"
  fetch "${client_base}/dfget" "${BIN_PATH}/dfget" "${DFGET_SHA256:-}"
  fetch "${nydus_base}/nydusd" "${BIN_PATH}/nydusd" "${NYDUS_SHA256:-}"
}

write_dfdaemon_config() {
  write_file "$DFDAEMON_CONFIG_PATH" 0644 <<EOF
host:
  schedulerClusterID: ${SCHEDULER_CLUSTER_ID}
manager:
  addr: ${MANAGER_GRPC}
  caCert: "${CERT_DIR}/ca.crt"
  cert: "${CERT_DIR}/tls.crt"
  key: "${CERT_DIR}/tls.key"
scheduler:
  caCert: "${CERT_DIR}/ca.crt"
  cert: "${CERT_DIR}/tls.crt"
  key: "${CERT_DIR}/tls.key"
proxy:
  server:
    ip: 127.0.0.1
    port: ${PROXY_PORT}
  rules:
    - regex: blobs/sha256.*
      schedulingPolicy: always
    - regex: manifests/sha256.*
      schedulingPolicy: always
seedPeer:
  enable: false
health:
  server:
    ip: 127.0.0.1
    port: ${HEALTH_PORT}
metrics:
  server:
    ip: 127.0.0.1
    port: ${METRICS_PORT}
EOF
}

write_dynconfig() {
  local addrs="" item old_ifs
  if [ -n "${SCHEDULER_IPS:-}" ]; then
    old_ifs="$IFS"
    IFS=','
    # Word splitting on commas is intentional here.
    # shellcheck disable=SC2086
    for item in $SCHEDULER_IPS; do
      item="${item//[[:space:]]/}"
      [ -n "$item" ] || continue
      case "$item" in
        *[!A-Za-z0-9.:\[\]-]*)
          IFS="$old_ifs"
          die "SCHEDULER_IPS contains an invalid address: ${item}"
          ;;
      esac
      addrs="${addrs}${addrs:+, }\"${item}:${SCHEDULER_PORT}\""
    done
    IFS="$old_ifs"
  fi

  write_file "$DYNCONFIG_PATH" 0644 <<EOF
scheduler:
  addrs: [${addrs}]
EOF
}

write_nydus_config() {
  local extra="" mode=0644
  if [ -n "$AUTH_B64" ]; then
    extra=",
            \"auth\": \"${AUTH_B64}\""
    mode=0600
  fi
  if [ "$REGISTRY_CA_PRESENT" -eq 1 ]; then
    extra="${extra},
            \"ca_cert_files\": [\"${CERT_DIR}/registry-ca.crt\"]"
  fi

  write_file "$NYDUS_CONFIG_PATH" "$mode" <<JSON
{
  "device": {
    "backend": {
      "type": "registry",
      "config": {
        "proxy": {
          "url": "http://127.0.0.1:${PROXY_PORT}",
          "ping_url": "http://127.0.0.1:${HEALTH_PORT}/healthy",
          "fallback": true,
          "check_interval": 5,
          "use_http": true
        }${extra}
      }
    },
    "cache": {
      "type": "blobcache",
      "config": {
        "work_dir": "${ROOT}/var/cache/nydus"
      }
    }
  },
  "mode": "direct",
  "digest_validate": true,
  "iostats_files": false,
  "enable_xattr": true,
  "fs_prefetch": {
    "enable": true,
    "threads_count": 8,
    "merging_size": 1048576,
    "prefetch_all": false
  }
}
JSON
}

preflight_manager() {
  local host
  if [ "$SKIP_PREFLIGHT" = "1" ]; then
    log "manager preflight skipped (SKIP_PREFLIGHT=1)"
    return 0
  fi

  host="$MANAGER_HOST"
  host="${host#\[}"
  host="${host%\]}"
  log "manager preflight: ${host}:${MANAGER_PORT} (timeout ${P2P_PREFLIGHT_TIMEOUT}s)"

  if command -v timeout >/dev/null 2>&1; then
    if timeout "$P2P_PREFLIGHT_TIMEOUT" bash -c "exec 3<>/dev/tcp/${host}/${MANAGER_PORT}" 2>/dev/null; then
      log "manager preflight ok"
      return 0
    fi
  elif (exec 3<>"/dev/tcp/${host}/${MANAGER_PORT}") 2>/dev/null; then
    log "manager preflight ok"
    return 0
  fi

  die_code 3 "manager preflight failed: cannot connect to ${host}:${MANAGER_PORT} within ${P2P_PREFLIGHT_TIMEOUT}s; check network/VPN and MANAGER_GRPC"
}

pid_alive() {
  local pid="$1"
  case "$pid" in
    ""|*[!0-9]*) return 1 ;;
  esac
  kill -0 "$pid" 2>/dev/null
}

# Like pid_alive but treats a zombie as gone. Needed before kill/wait loops:
# a reaped-later child stays visible to kill -0 and would stall the loop.
pid_running() {
  local pid="$1" stat=""
  pid_alive "$pid" || return 1
  if [ -r "/proc/${pid}/stat" ]; then
    stat="$(cat "/proc/${pid}/stat" 2>/dev/null || true)"
    stat="${stat##*) }"
    case "$stat" in
      Z*) return 1 ;;
    esac
  fi
  return 0
}

is_our_dfdaemon() {
  local pid="$1" cmdline=""
  pid_alive "$pid" || return 1
  if [ -r "/proc/${pid}/cmdline" ]; then
    cmdline="$(tr '\0' ' ' < "/proc/${pid}/cmdline" 2>/dev/null || true)"
  elif command -v ps >/dev/null 2>&1; then
    cmdline="$(ps -p "$pid" -o args= 2>/dev/null || true)"
  fi
  case "$cmdline" in
    *dfdaemon*) return 0 ;;
  esac
  return 1
}

health_ok() {
  local rc=0
  curl -fsS --max-time=2 "http://127.0.0.1:${HEALTH_PORT}/healthy" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    return 0
  fi
  if [ "$rc" -eq 2 ]; then
    # Some minimal curl builds do not know --max-time; keep the retry bounded.
    if command -v timeout >/dev/null 2>&1; then
      timeout 2 curl -fsS "http://127.0.0.1:${HEALTH_PORT}/healthy" >/dev/null 2>&1
    else
      curl -fsS "http://127.0.0.1:${HEALTH_PORT}/healthy" >/dev/null 2>&1
    fi
    return
  fi
  return 1
}

port_open() {
  local port="$1"
  (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null
}

check_ports() {
  local port
  for port in "$PROXY_PORT" "$METRICS_PORT" "$HEALTH_PORT"; do
    if port_open "$port"; then
      die "port ${port} is already in use by a foreign process; refusing to start dfdaemon or kill it"
    fi
  done
}

wait_health() {
  local pid="$1" deadline=$((SECONDS + DFDAEMON_HEALTH_TIMEOUT))
  while :; do
    if health_ok; then
      return 0
    fi
    if [ -n "$pid" ] && ! pid_alive "$pid"; then
      return 1
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      return 1
    fi
    sleep 1
  done
}

log_tail() {
  if [ -s "$LOG_PATH" ]; then
    log "last lines of ${LOG_PATH}:"
    tail -n 20 "$LOG_PATH" >&2 || true
  else
    log "log file ${LOG_PATH} is empty or missing"
  fi
}

lock_try_create() {
  if mkdir "$LOCK_PATH" 2>/dev/null; then
    printf '%s\n' "$$" > "${LOCK_PATH}/pid"
    LOCK_HELD=1
    return 0
  fi
  return 1
}

acquire_lock() {
  local owner="" cmdline=""
  if lock_try_create; then
    return 0
  fi
  owner="$(cat "${LOCK_PATH}/pid" 2>/dev/null || true)"
  if [ -z "$owner" ]; then
    sleep 1
    owner="$(cat "${LOCK_PATH}/pid" 2>/dev/null || true)"
  fi
  if [ -n "$owner" ] && pid_alive "$owner"; then
    if [ -r "/proc/${owner}/cmdline" ]; then
      cmdline="$(tr '\0' ' ' < "/proc/${owner}/cmdline" 2>/dev/null || true)"
    elif command -v ps >/dev/null 2>&1; then
      cmdline="$(ps -p "$owner" -o args= 2>/dev/null || true)"
    fi
    case "$cmdline" in
      *container-init*) return 1 ;;
    esac
  fi
  # Owner is dead, empty, or a reused PID that is not a container-init run.
  rm -rf "$LOCK_PATH"
  lock_try_create
}

# Stop only a dfdaemon whose PID file we own. Re-checks the cmdline first so a
# stale PID file never turns into a signal to an unrelated process.
stop_own_dfdaemon() {
  local pid="$1" i=0
  if ! is_our_dfdaemon "$pid"; then
    rm -f "$PID_PATH"
    return 0
  fi
  kill "$pid" 2>/dev/null || true
  while pid_running "$pid" && [ "$i" -lt 50 ]; do
    sleep 0.1
    i=$((i + 1))
  done
  if pid_running "$pid" && is_our_dfdaemon "$pid"; then
    kill -9 "$pid" 2>/dev/null || true
    i=0
    while pid_running "$pid" && [ "$i" -lt 20 ]; do
      sleep 0.1
      i=$((i + 1))
    done
  fi
  wait "$pid" 2>/dev/null || true
  rm -f "$PID_PATH"
}

start_dfdaemon() {
  local pid="" digest=""
  if [ -f "$PID_PATH" ]; then
    pid="$(cat "$PID_PATH" 2>/dev/null || true)"
  fi

  if ! acquire_lock; then
    if is_our_dfdaemon "$pid" && health_ok; then
      log "dfdaemon is already running and healthy (pid ${pid}); no-op"
      return 0
    fi
    if is_our_dfdaemon "$pid"; then
      log "waiting for dfdaemon (pid ${pid}) started by a concurrent init"
      if wait_health "$pid"; then
        log "dfdaemon is healthy (pid ${pid}); no-op"
        return 0
      fi
      log_tail
      die_code 2 "dfdaemon (pid ${pid}) did not become healthy within ${DFDAEMON_HEALTH_TIMEOUT}s"
    fi
    die "another container-init run is in progress (lock ${LOCK_PATH})"
  fi

  digest="$(compute_config_digest)"

  if is_our_dfdaemon "$pid"; then
    if ! health_ok; then
      log "dfdaemon is already running (pid ${pid}); waiting for health"
      if ! wait_health "$pid"; then
        stop_own_dfdaemon "$pid"
        log_tail
        die_code 2 "dfdaemon (pid ${pid}) did not become healthy within ${DFDAEMON_HEALTH_TIMEOUT}s"
      fi
    fi
    if [ "$(cat "$DIGEST_PATH" 2>/dev/null || true)" = "$digest" ]; then
      log "dfdaemon is healthy (pid ${pid}); no-op"
      return 0
    fi
    log "config changed; restarting own dfdaemon (pid ${pid})"
    stop_own_dfdaemon "$pid"
    pid=""
  fi

  check_ports

  rm -f "$PID_PATH"
  : > "$LOG_PATH"
  log "starting dfdaemon --config ${DFDAEMON_CONFIG_PATH} --console"
  # Everything the daemon needs is already written to disk; do not leak
  # registry credentials or certificate material into its environment.
  unset CA_CRT_B64 CLIENT_CRT_B64 CLIENT_KEY_B64 REGISTRY_CA_B64 \
    REGISTRY_USER REGISTRY_PASSWORD REGISTRY_AUTH_B64 AUTH_B64
  nohup "${BIN_PATH}/dfdaemon" --config "$DFDAEMON_CONFIG_PATH" --console >> "$LOG_PATH" 2>&1 &
  pid=$!
  printf '%s\n' "$pid" | write_file "$PID_PATH" 0644

  if wait_health "$pid"; then
    printf '%s\n' "$digest" | write_file "$DIGEST_PATH" 0644
    log "dfdaemon is healthy (pid ${pid})"
    return 0
  fi
  stop_own_dfdaemon "$pid"
  log_tail
  die_code 2 "dfdaemon did not become healthy within ${DFDAEMON_HEALTH_TIMEOUT}s (pid ${pid}); see ${LOG_PATH}"
}

main() {
  validate_env
  resolve_manager
  resolve_arch
  setup_paths
  create_dirs
  install_certs
  prepare_registry_auth
  install_binaries
  write_dfdaemon_config
  write_dynconfig
  write_nydus_config
  preflight_manager
  start_dfdaemon
  log "initialization complete"
}

main "$@"
