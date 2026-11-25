#!/usr/bin/env bash
# Offline self-test for pkg/machinebootstrap/assets/container-init.sh.
# Uses a fake root, a stub curl and a stub dfdaemon: no network, no root,
# no privileged operations. Covers US1 (container init) assertions.
set -Eeuo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
core="$root/pkg/machinebootstrap/assets/container-init.sh"
TEST_SCOPE="${TEST_SCOPE:-all}"

case "$TEST_SCOPE" in
  us1|all) ;;
  *)
    echo "container-init self-test: unknown TEST_SCOPE '${TEST_SCOPE}' (want us1 or all)" >&2
    exit 1
    ;;
esac

if [ ! -f "$core" ]; then
  echo "container-init self-test: missing ${core}" >&2
  exit 1
fi
bash -n "$core" || {
  echo "container-init self-test: ${core} has a syntax error" >&2
  exit 1
}

WORK="$(mktemp -d)"
P2P_ROOT="$WORK/root"
FAKE_BIN="$WORK/bin"
ASSETS="$WORK/assets"
LOGS="$WORK/logs"
CURL_LOG="$WORK/curl.log"
FOREIGN_PID=""

cleanup() {
  local pidfile pid
  if [ -n "$FOREIGN_PID" ]; then
    kill "$FOREIGN_PID" 2>/dev/null || true
  fi
  for pidfile in "$WORK"/dfdaemon-*.pids; do
    [ -f "$pidfile" ] || continue
    while IFS= read -r pid; do
      case "$pid" in
        ""|*[!0-9]*) continue ;;
      esac
      kill "$pid" 2>/dev/null || true
    done < "$pidfile"
  done
  rm -rf "$WORK"
}
trap cleanup EXIT

mkdir -p "$P2P_ROOT" "$FAKE_BIN" "$ASSETS" "$LOGS"

fail() {
  echo "container-init self-test: FAIL: $*" >&2
  exit 1
}

assert_file() {
  [ -f "$1" ] || fail "missing file $1"
}

assert_dir() {
  [ -d "$1" ] || fail "missing directory $1"
}

assert_absent() {
  [ ! -e "$1" ] || fail "unexpected path $1"
}

assert_mode() {
  local file="$1" want="$2" actual
  [ -f "$file" ] || fail "missing file $file"
  actual="$(stat -c '%a' "$file")"
  [ "$actual" = "$want" ] || fail "$file has mode $actual, want $want"
}

assert_contains() {
  grep -Fq -- "$2" "$1" || fail "$1 does not contain '$2'"
}

assert_not_contains() {
  if grep -Fq -- "$2" "$1"; then
    fail "$1 unexpectedly contains '$2'"
  fi
}

assert_count() {
  local file="$1" pattern="$2" expected="$3" actual
  actual="$(grep -Fc -- "$pattern" "$file" || true)"
  [ "$actual" = "$expected" ] || fail "$file has $actual occurrences of '$pattern', want $expected"
}

wait_for_pids() {
  local file="$1" want="${2:-1}" i=0 count=0
  while [ "$i" -lt 50 ]; do
    if [ -f "$file" ]; then
      count="$(grep -c . "$file" || true)"
    else
      count=0
    fi
    if [ "$count" -ge "$want" ]; then
      return 0
    fi
    sleep 0.1
    i=$((i + 1))
  done
  fail "timed out waiting for ${want} dfdaemon pid(s) in ${file}"
}

assert_start_count() {
  local file="$1" want="$2" count
  count="$(grep -c . "$file" || true)"
  [ "$count" -eq "$want" ] || fail "dfdaemon start count in ${file} is ${count}, want ${want}"
}

# Mirror the core's compute_config_digest: dfdaemon.yaml + mTLS material,
# plus registry-ca.crt when it is installed. Nydus/dynconfig are excluded.
expected_digest() {
  local r="$1" combined="$WORK/digest-combined.bin"
  : > "$combined"
  cat "$r/etc/dragonfly/dfdaemon.yaml" \
    "$r/etc/dragonfly/certs/ca.crt" \
    "$r/etc/dragonfly/certs/tls.crt" \
    "$r/etc/dragonfly/certs/tls.key" >> "$combined"
  if [ -f "$r/etc/dragonfly/certs/registry-ca.crt" ]; then
    cat "$r/etc/dragonfly/certs/registry-ca.crt" >> "$combined"
  fi
  sha256sum "$combined" | cut -d' ' -f1
}

assert_digest() {
  local r="$1" file want actual
  file="${r}/var/run/dragonfly/dfdaemon.config.sha256"
  assert_file "$file"
  assert_mode "$file" 644
  want="$(expected_digest "$r")"
  actual="$(cat "$file")"
  [ "$actual" = "$want" ] || fail "config digest in ${file} is ${actual}, want ${want}"
}

assert_environ_clean() {
  local pid="$1" name environ
  [ -r "/proc/${pid}/environ" ] || fail "cannot read /proc/${pid}/environ"
  environ="$(tr '\0' '\n' < "/proc/${pid}/environ")"
  for name in CA_CRT_B64 CLIENT_CRT_B64 CLIENT_KEY_B64 REGISTRY_CA_B64 \
    REGISTRY_USER REGISTRY_PASSWORD REGISTRY_AUTH_B64; do
    if printf '%s\n' "$environ" | grep -Eq "^${name}="; then
      fail "dfdaemon environment leaked ${name}"
    fi
  done
}

CERT_CA="$(printf 'dummy-ca\n' | base64 | tr -d '\n')"
CERT_CA_ROTATED="$(printf 'rotated-ca\n' | base64 | tr -d '\n')"
CERT_CRT="$(printf 'dummy-cert\n' | base64 | tr -d '\n')"
CERT_KEY="$(printf 'dummy-key\n' | base64 | tr -d '\n')"
REGISTRY_CA="$(printf 'dummy-registry-ca\n' | base64 | tr -d '\n')"
AUTH_USER_PASS="$(printf 'robot:secretpass' | base64 | tr -d '\n')"
AUTH_EXPLICIT="$(printf 'robot:otherpass' | base64 | tr -d '\n')"
BAD_SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

# Fixtures are installed by the core and must get 0755 from it, hence 0644 here.
cat > "$ASSETS/dfdaemon" <<'FAKEDFDAEMON'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$$" >> "${FAKE_DFDAEMON_PIDLOG:?}"
while :; do
  sleep 1
done
FAKEDFDAEMON
cat > "$ASSETS/dfget" <<'FAKEBIN'
#!/usr/bin/env bash
exit 0
FAKEBIN
cat > "$ASSETS/nydusd" <<'FAKEBIN'
#!/usr/bin/env bash
exit 0
FAKEBIN
chmod 0644 "$ASSETS/dfdaemon" "$ASSETS/dfget" "$ASSETS/nydusd"

cat > "$FAKE_BIN/curl" <<'FAKECURL'
#!/usr/bin/env bash
set -Eeuo pipefail
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    --retry|--connect-timeout|--max-time)
      shift 2
      ;;
    --max-time=*)
      shift
      ;;
    -*)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done
if [ -z "$url" ]; then
  echo "fake curl: missing URL" >&2
  exit 2
fi
printf '%s\n' "$url" >> "${FAKE_CURL_LOG:?}"
case "$url" in
  http://127.0.0.1:4003/healthy)
    if [ "${FAKE_CURL_HEALTH_FAIL:-0}" = "1" ]; then
      exit 7
    fi
    exit 0
    ;;
esac
if [ -z "$out" ]; then
  echo "fake curl: missing -o for ${url}" >&2
  exit 2
fi
case "$url" in
  */client/*/*/dfdaemon)
    cp "${FAKE_DFDAEMON_SRC:?}" "$out"
    ;;
  */client/*/*/dfget)
    cp "${FAKE_DFGET_SRC:?}" "$out"
    ;;
  */nydus/*/*/nydusd)
    cp "${FAKE_NYDUSD_SRC:?}" "$out"
    ;;
  *)
    echo "fake curl: 404 ${url}" >&2
    exit 22
    ;;
esac
FAKECURL
chmod 0755 "$FAKE_BIN/curl"

# Run the core with a hermetic environment. $1 is the fake root, $2 is a
# variable to leave unset (or __NONE__), the rest are extra NAME=VALUE pairs.
run_init() {
  local root="$1" skip="$2"
  shift 2
  local label="${root%/}"
  label="${label##*/}"
  local -a args=(
    "P2P_ROOT=$root"
    "SKIP_PREFLIGHT=1"
    "STATIC_URL=http://static.example.test"
    "MANAGER_GRPC=https://manager-grpc.example.test:8443/dragonfly"
    "SCHEDULER_CLUSTER_ID=42"
    "SCHEDULER_IPS=10.9.8.7,10.9.8.8"
    "CA_CRT_B64=$CERT_CA"
    "CLIENT_CRT_B64=$CERT_CRT"
    "CLIENT_KEY_B64=$CERT_KEY"
    "TARGET_ARCH="
    "DRAGONFLY_CLIENT_VERSION="
    "NYDUS_VERSION="
    "DFDAEMON_SHA256="
    "DFGET_SHA256="
    "NYDUS_SHA256="
    "REGISTRY_USER="
    "REGISTRY_PASSWORD="
    "REGISTRY_AUTH_B64="
    "REGISTRY_CA_B64="
    "SMART_PROXY_URL="
    "NYDUS_MIRROR_URL="
    "P2P_BIN_DIR="
    "DFDAEMON_HEALTH_TIMEOUT="
    "P2P_PREFLIGHT_TIMEOUT="
    "FAKE_CURL_LOG=$CURL_LOG"
    "FAKE_CURL_HEALTH_FAIL="
    "FAKE_DFDAEMON_SRC=$ASSETS/dfdaemon"
    "FAKE_DFGET_SRC=$ASSETS/dfget"
    "FAKE_NYDUSD_SRC=$ASSETS/nydusd"
    "FAKE_DFDAEMON_PIDLOG=$WORK/dfdaemon-${label}.pids"
    "PATH=$FAKE_BIN:$PATH"
  )
  if [ "$skip" != "__NONE__" ]; then
    local -a kept=()
    local item
    for item in "${args[@]}"; do
      case "$item" in
        "$skip="*) continue ;;
      esac
      kept+=("$item")
    done
    args=("${kept[@]}")
    env -u "$skip" "${args[@]}" "$@" bash "$core"
  else
    env "${args[@]}" "$@" bash "$core"
  fi
}

run_ok() {
  local log="$1"
  shift
  if ! "$@" > "$log" 2>&1; then
    echo "container-init self-test: command failed; last lines of ${log}:" >&2
    tail -n 20 "$log" >&2 || true
    exit 1
  fi
}

# --- required env: every missing variable fails with its name --------------
for required in STATIC_URL MANAGER_GRPC SCHEDULER_CLUSTER_ID \
  CA_CRT_B64 CLIENT_CRT_B64 CLIENT_KEY_B64; do
  : > "$CURL_LOG"
  rc=0
  run_init "$WORK/root-missing-$required" "$required" > "$LOGS/missing-$required.log" 2>&1 || rc=$?
  [ "$rc" -eq 1 ] || fail "missing ${required}: exit ${rc}, want 1"
  assert_contains "$LOGS/missing-$required.log" "required environment variable ${required} is not set"
  if [ -s "$CURL_LOG" ]; then
    fail "missing ${required}: downloads started before validation"
  fi
done

# --- architecture -----------------------------------------------------------
: > "$CURL_LOG"
rc=0
run_init "$WORK/root-arch-bad" __NONE__ TARGET_ARCH=riscv64 > "$LOGS/arch-bad.log" 2>&1 || rc=$?
[ "$rc" -eq 1 ] || fail "unsupported arch: exit ${rc}, want 1"
assert_contains "$LOGS/arch-bad.log" 'unsupported architecture riscv64'
if [ -s "$CURL_LOG" ]; then
  fail "unsupported arch: downloads started before the arch check"
fi

: > "$CURL_LOG"
run_ok "$LOGS/arm64.log" run_init "$WORK/root-arm64" __NONE__ TARGET_ARCH=arm64
wait_for_pids "$WORK/dfdaemon-root-arm64.pids" 1
assert_contains "$CURL_LOG" 'http://static.example.test/client/v1.5.14/arm64/dfdaemon'
assert_contains "$CURL_LOG" 'http://static.example.test/client/v1.5.14/arm64/dfget'
assert_contains "$CURL_LOG" 'http://static.example.test/nydus/v2.4.5/arm64/nydusd'
if grep -Fq '/amd64/' "$CURL_LOG"; then
  fail "arm64 run fetched an amd64 asset"
fi
assert_file "$WORK/root-arm64/usr/local/bin/dfdaemon"

# --- successful init (amd64) ------------------------------------------------
: > "$CURL_LOG"
run_ok "$LOGS/init.log" run_init "$P2P_ROOT" __NONE__
wait_for_pids "$WORK/dfdaemon-root.pids" 1

BIN="$P2P_ROOT/usr/local/bin"
CERTS="$P2P_ROOT/etc/dragonfly/certs"
DFDAEMON_YAML="$P2P_ROOT/etc/dragonfly/dfdaemon.yaml"
DYNCONFIG="$P2P_ROOT/etc/dragonfly/dynconfig.yaml"
NYDUS_JSON="$P2P_ROOT/etc/nydus/config.json"
PID_FILE="$P2P_ROOT/var/run/dragonfly/dfdaemon.pid"
DIGEST_FILE="$P2P_ROOT/var/run/dragonfly/dfdaemon.config.sha256"

assert_file "$PID_FILE"
base_pid="$(cat "$PID_FILE")"
daemon_pid="$(grep -m1 -E '^[0-9]+$' "$WORK/dfdaemon-root.pids" || true)"
[ -n "$daemon_pid" ] || fail "fake dfdaemon did not record its pid"
[ "$base_pid" = "$daemon_pid" ] || fail "dfdaemon.pid is ${base_pid}, fake dfdaemon pid is ${daemon_pid}"
kill -0 "$base_pid" 2>/dev/null || fail "dfdaemon pid ${base_pid} is not alive"
assert_contains "$LOGS/init.log" 'starting dfdaemon --config'
assert_contains "$LOGS/init.log" "dfdaemon is healthy (pid ${base_pid})"
assert_start_count "$WORK/dfdaemon-root.pids" 1

installed=""
for path in "$BIN"/*; do
  [ -e "$path" ] || continue
  installed="${installed}${path##*/} "
done
[ "$installed" = "dfdaemon dfget nydusd " ] || fail "installed binaries are '${installed}', want exactly dfdaemon dfget nydusd"
for binary in dfdaemon dfget nydusd; do
  assert_mode "$BIN/$binary" 755
done
assert_absent "$BIN/nydus-mount"
assert_absent "$BIN/nydus-image"
assert_absent "$BIN/nydus-overlayfs"
assert_absent "$BIN/containerd-nydus-grpc"
if find "$BIN" -name '*.tmp.*' -print -quit | grep -q .; then
  fail "temporary download files left in ${BIN}"
fi
assert_contains "$CURL_LOG" 'http://static.example.test/client/v1.5.14/amd64/dfdaemon'
assert_contains "$CURL_LOG" 'http://static.example.test/client/v1.5.14/amd64/dfget'
assert_contains "$CURL_LOG" 'http://static.example.test/nydus/v2.4.5/amd64/nydusd'

assert_mode "$CERTS/ca.crt" 644
assert_mode "$CERTS/tls.crt" 644
assert_mode "$CERTS/tls.key" 600
assert_contains "$CERTS/ca.crt" 'dummy-ca'
assert_contains "$CERTS/tls.crt" 'dummy-cert'
assert_contains "$CERTS/tls.key" 'dummy-key'
assert_absent "$CERTS/registry-ca.crt"
if grep -Fq 'dummy-key' "$LOGS/init.log"; then
  fail "private key material leaked to the init log"
fi

assert_file "$DFDAEMON_YAML"
assert_contains "$DFDAEMON_YAML" 'addr: https://manager-grpc.example.test:8443/dragonfly'
assert_contains "$DFDAEMON_YAML" 'schedulerClusterID: 42'
assert_count "$DFDAEMON_YAML" '  caCert: "/etc/dragonfly/certs/ca.crt"' 2
assert_count "$DFDAEMON_YAML" '  cert: "/etc/dragonfly/certs/tls.crt"' 2
assert_count "$DFDAEMON_YAML" '  key: "/etc/dragonfly/certs/tls.key"' 2
assert_contains "$DFDAEMON_YAML" 'blobs/sha256.*'
assert_contains "$DFDAEMON_YAML" 'manifests/sha256.*'
assert_count "$DFDAEMON_YAML" 'schedulingPolicy: always' 2
assert_contains "$DFDAEMON_YAML" 'seedPeer:'
assert_contains "$DFDAEMON_YAML" '  enable: false'
assert_count "$DFDAEMON_YAML" '    ip: 127.0.0.1' 3
assert_contains "$DFDAEMON_YAML" '    port: 4001'
assert_contains "$DFDAEMON_YAML" '    port: 4003'
assert_contains "$DFDAEMON_YAML" '    port: 4002'
assert_not_contains "$DFDAEMON_YAML" 'tls:'
assert_not_contains "$DFDAEMON_YAML" 'dummy-key'

assert_file "$DYNCONFIG"
assert_contains "$DYNCONFIG" '  addrs: ["10.9.8.7:8002", "10.9.8.8:8002"]'

assert_file "$NYDUS_JSON"
assert_mode "$NYDUS_JSON" 644
for want in '"type": "registry"' '"type": "blobcache"' \
  '"digest_validate": true' '"enable": true' '"prefetch_all": false' \
  '"url": "http://127.0.0.1:4001"' '"ping_url": "http://127.0.0.1:4003/healthy"' \
  '"fallback": true' '"use_http": true' '"mode": "direct"' '"enable_xattr": true'; do
  assert_contains "$NYDUS_JSON" "$want"
done
assert_contains "$NYDUS_JSON" "\"work_dir\": \"${P2P_ROOT}/var/cache/nydus\""
assert_dir "$P2P_ROOT/var/cache/nydus"
assert_not_contains "$NYDUS_JSON" '"host"'
assert_not_contains "$NYDUS_JSON" '"repo"'
assert_not_contains "$NYDUS_JSON" '"auth"'
assert_not_contains "$NYDUS_JSON" '"ca_cert_files"'

assert_digest "$P2P_ROOT"
digest_base="$(cat "$DIGEST_FILE")"

# --- same env: no-op, same pid, unchanged configs and digest ---------------
sha256sum "$DFDAEMON_YAML" "$DYNCONFIG" "$NYDUS_JSON" > "$LOGS/configs-before.sha"
before_pid="$(cat "$PID_FILE")"
run_ok "$LOGS/init-second.log" run_init "$P2P_ROOT" __NONE__
after_pid="$(cat "$PID_FILE")"
[ "$before_pid" = "$after_pid" ] || fail "second init changed the dfdaemon pid (${before_pid} -> ${after_pid})"
sha256sum "$DFDAEMON_YAML" "$DYNCONFIG" "$NYDUS_JSON" > "$LOGS/configs-after.sha"
cmp -s "$LOGS/configs-before.sha" "$LOGS/configs-after.sha" || fail "second init changed generated configs"
assert_contains "$LOGS/init-second.log" "dfdaemon is healthy (pid ${after_pid})"
assert_digest "$P2P_ROOT"
[ "$(cat "$DIGEST_FILE")" = "$digest_base" ] || fail "second init changed the config digest"
sleep 0.3
assert_start_count "$WORK/dfdaemon-root.pids" 1

# Empty scheduler list changes dynconfig only; the dfdaemon digest is exempt.
run_ok "$LOGS/init-empty-sched.log" run_init "$P2P_ROOT" __NONE__ SCHEDULER_IPS=
assert_contains "$DYNCONFIG" '  addrs: []'
[ "$(cat "$PID_FILE")" = "$after_pid" ] || fail "empty-scheduler run restarted dfdaemon"
assert_digest "$P2P_ROOT"
[ "$(cat "$DIGEST_FILE")" = "$digest_base" ] || fail "empty-scheduler run changed the config digest"
sleep 0.3
assert_start_count "$WORK/dfdaemon-root.pids" 1

# --- changed dfdaemon inputs: own daemon is restarted ----------------------
sched_old_pid="$(cat "$PID_FILE")"
run_ok "$LOGS/scheduler-id.log" run_init "$P2P_ROOT" __NONE__ SCHEDULER_CLUSTER_ID=43
wait_for_pids "$WORK/dfdaemon-root.pids" 2
assert_start_count "$WORK/dfdaemon-root.pids" 2
assert_contains "$LOGS/scheduler-id.log" 'config changed; restarting own dfdaemon'
sched_pid="$(cat "$PID_FILE")"
[ "$sched_pid" != "$sched_old_pid" ] || fail "SCHEDULER_CLUSTER_ID change kept the dfdaemon pid ${sched_pid}"
kill -0 "$sched_pid" 2>/dev/null || fail "restarted dfdaemon pid ${sched_pid} is not alive"
if kill -0 "$sched_old_pid" 2>/dev/null; then
  fail "old dfdaemon pid ${sched_old_pid} is still running after the restart"
fi
assert_contains "$DFDAEMON_YAML" 'schedulerClusterID: 43'
assert_digest "$P2P_ROOT"
[ "$(cat "$DIGEST_FILE")" != "$digest_base" ] || fail "SCHEDULER_CLUSTER_ID change did not update the config digest"

cert_old_pid="$(cat "$PID_FILE")"
run_ok "$LOGS/cert-change.log" run_init "$P2P_ROOT" __NONE__ "CA_CRT_B64=$CERT_CA_ROTATED"
wait_for_pids "$WORK/dfdaemon-root.pids" 3
assert_start_count "$WORK/dfdaemon-root.pids" 3
assert_contains "$LOGS/cert-change.log" 'config changed; restarting own dfdaemon'
cert_pid="$(cat "$PID_FILE")"
[ "$cert_pid" != "$cert_old_pid" ] || fail "cert change kept the dfdaemon pid ${cert_pid}"
kill -0 "$cert_pid" 2>/dev/null || fail "restarted dfdaemon pid ${cert_pid} is not alive"
if kill -0 "$cert_old_pid" 2>/dev/null; then
  fail "old dfdaemon pid ${cert_old_pid} is still running after the cert restart"
fi
assert_contains "$CERTS/ca.crt" 'rotated-ca'
assert_digest "$P2P_ROOT"

# --- registry auth / CA in the base nydus config ----------------------------
auth_old_pid="$(cat "$PID_FILE")"
run_ok "$LOGS/auth-user.log" run_init "$P2P_ROOT" __NONE__ \
  REGISTRY_USER=robot REGISTRY_PASSWORD=secretpass REGISTRY_CA_B64="$REGISTRY_CA"
wait_for_pids "$WORK/dfdaemon-root.pids" 4
assert_start_count "$WORK/dfdaemon-root.pids" 4
assert_contains "$LOGS/auth-user.log" 'config changed; restarting own dfdaemon'
auth_pid="$(cat "$PID_FILE")"
[ "$auth_pid" != "$auth_old_pid" ] || fail "registry CA change kept the dfdaemon pid ${auth_pid}"
kill -0 "$auth_pid" 2>/dev/null || fail "restarted dfdaemon pid ${auth_pid} is not alive"
if kill -0 "$auth_old_pid" 2>/dev/null; then
  fail "old dfdaemon pid ${auth_old_pid} is still running after the registry CA restart"
fi
assert_contains "$NYDUS_JSON" "\"auth\": \"${AUTH_USER_PASS}\""
assert_contains "$NYDUS_JSON" '"ca_cert_files": ["/etc/dragonfly/certs/registry-ca.crt"]'
assert_mode "$CERTS/registry-ca.crt" 644
assert_contains "$CERTS/registry-ca.crt" 'dummy-registry-ca'
assert_mode "$NYDUS_JSON" 600
assert_digest "$P2P_ROOT"
if grep -Fq 'secretpass' "$LOGS/auth-user.log" || grep -Fq "$AUTH_USER_PASS" "$LOGS/auth-user.log"; then
  fail "registry credentials leaked to the auth log"
fi

# REGISTRY_AUTH_B64 wins over user/password; removing the registry CA changes
# the digest again and restarts the daemon.
explicit_old_pid="$(cat "$PID_FILE")"
run_ok "$LOGS/auth-explicit.log" run_init "$P2P_ROOT" __NONE__ \
  REGISTRY_USER=robot REGISTRY_PASSWORD=secretpass REGISTRY_AUTH_B64="$AUTH_EXPLICIT"
wait_for_pids "$WORK/dfdaemon-root.pids" 5
assert_start_count "$WORK/dfdaemon-root.pids" 5
assert_contains "$LOGS/auth-explicit.log" 'config changed; restarting own dfdaemon'
explicit_pid="$(cat "$PID_FILE")"
[ "$explicit_pid" != "$explicit_old_pid" ] || fail "registry CA removal kept the dfdaemon pid ${explicit_pid}"
kill -0 "$explicit_pid" 2>/dev/null || fail "restarted dfdaemon pid ${explicit_pid} is not alive"
if kill -0 "$explicit_old_pid" 2>/dev/null; then
  fail "old dfdaemon pid ${explicit_old_pid} is still running after the registry CA removal restart"
fi
assert_contains "$NYDUS_JSON" "\"auth\": \"${AUTH_EXPLICIT}\""
assert_not_contains "$NYDUS_JSON" "$AUTH_USER_PASS"
assert_not_contains "$NYDUS_JSON" '"ca_cert_files"'
assert_absent "$CERTS/registry-ca.crt"
assert_mode "$NYDUS_JSON" 600
assert_digest "$P2P_ROOT"
if grep -Fq 'otherpass' "$LOGS/auth-explicit.log" || grep -Fq "$AUTH_EXPLICIT" "$LOGS/auth-explicit.log"; then
  fail "registry auth token leaked to the auth log"
fi

# --- daemon environment must not carry cert/registry material ---------------
environ_root="$WORK/root-environ"
: > "$CURL_LOG"
run_ok "$LOGS/environ.log" run_init "$environ_root" __NONE__ \
  REGISTRY_USER=robot REGISTRY_PASSWORD=secretpass \
  REGISTRY_AUTH_B64="$AUTH_EXPLICIT" REGISTRY_CA_B64="$REGISTRY_CA"
wait_for_pids "$WORK/dfdaemon-root-environ.pids" 1
environ_pid="$(cat "$environ_root/var/run/dragonfly/dfdaemon.pid")"
kill -0 "$environ_pid" 2>/dev/null || fail "environ-test dfdaemon pid ${environ_pid} is not alive"
assert_environ_clean "$environ_pid"

# --- STATIC_URL must not embed credentials ----------------------------------
: > "$CURL_LOG"
rc=0
run_init "$WORK/root-static-user" __NONE__ \
  'STATIC_URL=https://user:pass@static.example.test' > "$LOGS/static-user.log" 2>&1 || rc=$?
[ "$rc" -eq 1 ] || fail "credentialed STATIC_URL: exit ${rc}, want 1"
assert_contains "$LOGS/static-user.log" 'STATIC_URL contains an unsupported character'
if [ -s "$CURL_LOG" ]; then
  fail "credentialed STATIC_URL: downloads started before validation"
fi

# --- certificates: invalid base64 fails before writing targets -------------
for var in CA_CRT_B64 CLIENT_CRT_B64 CLIENT_KEY_B64; do
  rc=0
  run_init "$WORK/root-b64-$var" __NONE__ "$var=%%%" > "$LOGS/b64-$var.log" 2>&1 || rc=$?
  [ "$rc" -eq 1 ] || fail "invalid ${var}: exit ${rc}, want 1"
  assert_contains "$LOGS/b64-$var.log" "${var} is not valid base64"
  assert_absent "$WORK/root-b64-$var/etc/dragonfly/certs/ca.crt"
  assert_absent "$WORK/root-b64-$var/etc/dragonfly/certs/tls.crt"
  assert_absent "$WORK/root-b64-$var/etc/dragonfly/certs/tls.key"
done

# --- per-binary checksum mismatch -------------------------------------------
for pair in "DFDAEMON_SHA256 dfdaemon" "DFGET_SHA256 dfget" "NYDUS_SHA256 nydusd"; do
  var="${pair%% *}"
  binary="${pair##* }"
  rc=0
  run_init "$WORK/root-sha-$binary" __NONE__ "${var}=${BAD_SHA}" > "$LOGS/sha-$binary.log" 2>&1 || rc=$?
  [ "$rc" -eq 1 ] || fail "${var} mismatch: exit ${rc}, want 1"
  assert_contains "$LOGS/sha-$binary.log" 'SHA-256 mismatch'
  assert_absent "$WORK/root-sha-$binary/usr/local/bin/$binary"
done

# --- daemon lifecycle: health timeout cleans up its own daemon --------------
timeout_root="$WORK/root-timeout"
rc=0
run_init "$timeout_root" __NONE__ DFDAEMON_HEALTH_TIMEOUT=1 FAKE_CURL_HEALTH_FAIL=1 \
  > "$LOGS/timeout.log" 2>&1 || rc=$?
[ "$rc" -eq 2 ] || fail "health timeout: exit ${rc}, want 2"
assert_contains "$LOGS/timeout.log" 'did not become healthy within 1s'
if ! grep -Fq 'last lines of' "$LOGS/timeout.log" && ! grep -Fq 'is empty or missing' "$LOGS/timeout.log"; then
  fail "health timeout did not report the dfdaemon log tail"
fi
assert_absent "$timeout_root/var/run/dragonfly/dfdaemon.pid"
assert_absent "$timeout_root/var/run/dragonfly/dfdaemon.config.sha256"
wait_for_pids "$WORK/dfdaemon-root-timeout.pids" 1
timeout_pid="$(grep -m1 -E '^[0-9]+$' "$WORK/dfdaemon-root-timeout.pids" || true)"
[ -n "$timeout_pid" ] || fail "timed-out dfdaemon did not record its pid"
if kill -0 "$timeout_pid" 2>/dev/null; then
  fail "timed-out dfdaemon pid ${timeout_pid} is still running after cleanup"
fi

# --- P2P_ROOT trailing slash + P2P_BIN_DIR inside it ------------------------
slash_root="$WORK/root-slash"
slash_bin="$slash_root/custom-bin"
: > "$CURL_LOG"
run_ok "$LOGS/slash-bin.log" run_init "$slash_root/" __NONE__ "P2P_BIN_DIR=$slash_bin"
wait_for_pids "$WORK/dfdaemon-root-slash.pids" 1
assert_start_count "$WORK/dfdaemon-root-slash.pids" 1
for binary in dfdaemon dfget nydusd; do
  assert_file "$slash_bin/$binary"
  assert_mode "$slash_bin/$binary" 755
done
assert_absent "$slash_root/usr/local/bin/dfdaemon"
assert_absent "${slash_root}${slash_root}"
assert_absent "${slash_root}${slash_bin}"
assert_contains "$slash_root/etc/nydus/config.json" "\"work_dir\": \"${slash_root}/var/cache/nydus\""
assert_dir "$slash_root/var/cache/nydus"
assert_digest "$slash_root"

# --- foreign listeners on proxy/metrics/health ports ------------------------
start_foreign_listener() {
  local port="$1" i=0
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$port" > "$LOGS/foreign-listener-${port}.log" 2>&1 <<'PY' &
import socket
import sys

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
sock.bind(("127.0.0.1", int(sys.argv[1])))
sock.listen(8)
while True:
    conn, _ = sock.accept()
    conn.close()
PY
    FOREIGN_PID=$!
  elif command -v nc >/dev/null 2>&1; then
    nc -lk 127.0.0.1 "$port" > "$LOGS/foreign-listener-${port}.log" 2>&1 &
    FOREIGN_PID=$!
  else
    fail "python3 or nc is required for the foreign-listener test"
  fi
  while [ "$i" -lt 50 ]; do
    if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
      return 0
    fi
    sleep 0.1
    i=$((i + 1))
  done
  fail "foreign listener did not start on 127.0.0.1:${port}"
}

for port in 4001 4002 4003; do
  start_foreign_listener "$port"
  foreign_root="$WORK/root-foreign-$port"
  rc=0
  run_init "$foreign_root" __NONE__ > "$LOGS/foreign-$port.log" 2>&1 || rc=$?
  [ "$rc" -eq 1 ] || fail "foreign listener on ${port}: exit ${rc}, want 1"
  assert_contains "$LOGS/foreign-$port.log" "port ${port} is already in use by a foreign process"
  kill -0 "$FOREIGN_PID" 2>/dev/null || fail "foreign listener on ${port} was killed by container-init"
  assert_absent "$WORK/dfdaemon-root-foreign-$port.pids"
  kill "$FOREIGN_PID" 2>/dev/null || true
  wait "$FOREIGN_PID" 2>/dev/null || true
  FOREIGN_PID=""
done

# --- TEST_SCOPE=all: source and log negative controls ------------------------
if [ "$TEST_SCOPE" = "all" ]; then
  if grep -nE '(^|[;&|[:space:]])(systemctl|modprobe|iptables|tailscale|privileged|nydus-mount|nydusify|/dev/fuse)|MODEL_FALLBACK_URL|--bootstrap|registry/v2' "$core" | grep -vE ':[0-9]+:[[:space:]]*#'; then
    fail "container-init.sh references forbidden host/system/network features"
  fi
  for log in "$LOGS"/*.log; do
    [ -f "$log" ] || continue
    if grep -Fq -e 'dummy-key' -e 'secretpass' -e 'otherpass' -e 'Authorization' "$log"; then
      fail "secret material leaked to ${log}"
    fi
  done
fi

echo "container-init self-test: OK"
