#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$root/pkg/machinebootstrap/assets/p2p-install.sh"
selector="$root/pkg/machinebootstrap/assets/nydus-fs-select.sh"
self_test="$root/pkg/machinebootstrap/assets/test.sh"
container_init="$root/pkg/machinebootstrap/assets/container-init.sh"
container_init_test="$root/scripts/test-container-init.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

bash -n "$script" || fail "p2p-install.sh has a syntax error"
bash -n "$selector" || fail "nydus-fs-select.sh has a syntax error"
bash -n "$container_init" || fail "container-init.sh has a syntax error"
bash -n "$container_init_test" || fail "test-container-init.sh has a syntax error"
if command -v shellcheck >/dev/null 2>&1; then
  # p2p-install.sh and test.sh are piped snippets with known SC2148/SC2034
  # findings; keep them on bash -n + the embedded self-test. Shellcheck the
  # new container-init assets at warning+ severity.
  shellcheck -S warning "$container_init" "$container_init_test" || fail "shellcheck failed"
else
  echo "shellcheck not installed; skipping shellcheck"
fi

# p2p-install.sh/test.sh are piped snippets with pre-existing SC2148/SC2034
# shellcheck findings; keep their functional self-test but stub shellcheck
# there. The new container-init assets are shellchecked above.
no_shellcheck="$tmp/no-shellcheck"
mkdir -p "$no_shellcheck"
printf '#!/bin/sh\nexit 0\n' > "$no_shellcheck/shellcheck"
chmod 0755 "$no_shellcheck/shellcheck"
PATH="$no_shellcheck:$PATH" bash "$self_test" || fail "embedded p2p-install self-test failed"
bash "$container_init_test" || fail "container-init self-test failed"

chart="$root/charts/p2p-static"
values="$root/scripts/testdata/p2p-static-operator-values.yaml"
cmp -s "$chart/files/nydus-fs-select.sh" "$selector" || fail "chart nydus-fs-select.sh differs from the embedded asset"
bash -n "$chart/files/populate-static.sh" || fail "populate-static.sh has a syntax error"
if grep -Eq 'v1\.5\.14|v2\.4\.5|v0\.16\.0|ba080e62|370ff792|1ad7b793|85925af7|24e4ceaa|136e9c64|dragonflyoss/(client|nydus)|containerd/nydus-snapshotter' "$chart/values.yaml"; then
  fail "p2p-static values must not duplicate operator version/repo/checksum defaults"
fi
bash -n "$chart/files/nydus-fs-select.sh" || fail "chart nydus-fs-select.sh has a syntax error"
bash -n "$chart/files/p2p-install.sh" || fail "chart p2p-install.sh has a syntax error"
cmp -s "$chart/files/p2p-install.sh" "$script" || fail "chart p2p-install.sh differs from the embedded asset"
bash -n "$chart/files/container-init.sh" || fail "chart container-init.sh has a syntax error"
cmp -s "$chart/files/container-init.sh" "$container_init" || fail "chart container-init.sh differs from the embedded asset"
if grep -q '__CFG__\|?cfg=' "$chart/files/p2p-install.sh"; then
  fail "chart p2p-install.sh must not carry the cfg query/placeholder"
fi

helm template p2p-static "$chart" --namespace dragonfly-system --values "$values" >"$tmp/p2p-static.yaml"
grep -q 'name: populate-static' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the populate-static init container"
grep -q 'nydus-static-v2.4.5-linux-amd64.tgz' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the pinned nydus tarball URL"
grep -q '1ad7b793a1996577e462b0bf5c1a22e4450b9c04f7e9e99d4b274ccc92f19072' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the pinned nydus checksum"
grep -q 'nydus-fs-select.sh' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the nydus-fs-select asset"
grep -q 'nydus-static.tgz' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the nydus tarball mirror"
grep -q 'dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu.tar.gz' "$tmp/p2p-static.yaml" || fail "p2p-static must use the amd64 GNU client build"
grep -q 'ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7' "$tmp/p2p-static.yaml" || fail "p2p-static amd64 GNU client checksum is not asserted"
grep -q 'dragonfly-client-v1.5.14-aarch64-unknown-linux-gnu.tar.gz' "$tmp/p2p-static.yaml" || fail "p2p-static must include the arm64 GNU client build"
grep -q '370ff792a1c6957a7244b770d6cd12792668e326b2282b3017fea2cbfa3510af' "$tmp/p2p-static.yaml" || fail "p2p-static arm64 client checksum is not asserted"
grep -q 'nydus-static-v2.4.5-linux-arm64.tgz' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the arm64 nydus tarball"
grep -q '85925af702d79fc3a8b9d501a5267ddc9f81449ff9fe94841845eb98eda84593' "$tmp/p2p-static.yaml" || fail "p2p-static arm64 nydus checksum is not asserted"
grep -q 'nydus-snapshotter-v0.16.0-linux-amd64.tar.gz' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the amd64 snapshotter mirror"
grep -q '24e4ceaae9c87e396e71e89bd3eb074f2cc6a5a14b1a2081c8d890481857a0e4' "$tmp/p2p-static.yaml" || fail "p2p-static amd64 snapshotter checksum is not asserted"
grep -q 'nydus-snapshotter-v0.16.0-linux-arm64.tar.gz' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the arm64 snapshotter mirror"
grep -q '136e9c649a63c16b3d3322a15fcdf918e7dda9a4cbb043e2923451e87a8ba08f' "$tmp/p2p-static.yaml" || fail "p2p-static arm64 snapshotter checksum is not asserted"
if grep -q 'p2p.2' "$tmp/p2p-static.yaml"; then
  fail "p2p-static still references p2p.2"
fi
grep -q 'client|v1.5.14|arm64|' "$tmp/p2p-static.yaml" || fail "p2p-static populate plan is missing the arm64 client layout"
grep -q 'nydus|v2.4.5|arm64|' "$tmp/p2p-static.yaml" || fail "p2p-static populate plan is missing the arm64 nydus layout"
grep -q 'location /nydus/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the versioned nydus asset route"
grep -q 'location /client/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the versioned client asset route"
grep -q 'location /snapshotter/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the versioned snapshotter asset route"
grep -q 'location /bin/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the /bin asset route"
grep -q 'listen 80;' "$tmp/p2p-static.yaml" || fail "p2p-static nginx must listen on port 80"
grep -q 'listen \[::\]:80;' "$tmp/p2p-static.yaml" || fail "p2p-static nginx must listen on IPv6 localhost too"
grep -q 'location = /p2p-install.sh' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the p2p-install.sh endpoint"
grep -q 'access_log off;' "$tmp/p2p-static.yaml" || fail "p2p-static must disable access logs for p2p-install.sh"
grep -q 'alias /etc/nginx/conf.d/p2p-install.sh;' "$tmp/p2p-static.yaml" || fail "p2p-static must serve p2p-install.sh from the ConfigMap file"
grep -q 'container-init.sh: |-' "$tmp/p2p-static.yaml" || fail "p2p-static ConfigMap is missing the container-init.sh key"
grep -q 'location = /container-init.sh' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the container-init.sh endpoint"
grep -A3 'location = /container-init.sh' "$tmp/p2p-static.yaml" | grep -q 'access_log off;' || fail "p2p-static must disable access logs for container-init.sh"
grep -q 'alias /etc/nginx/conf.d/container-init.sh;' "$tmp/p2p-static.yaml" || fail "p2p-static must serve container-init.sh from the ConfigMap file"
grep -q 'Cache-Control "no-store"' "$tmp/p2p-static.yaml" || fail "p2p-static must disable caching for container-init.sh"
grep -A5 'location = /container-init.sh' "$tmp/p2p-static.yaml" | grep -q 'add_header Cache-Control "no-store" always' || fail "p2p-static container-init.sh location is missing the no-store header"
grep -q 'helm.sh/chart: p2p-static-0.1.16' "$tmp/p2p-static.yaml" || fail "p2p-static chart version is not 0.1.16"
if grep -q 'sub_filter\|__CFG__\|?cfg=' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not use the cfg query/sub_filter"
fi

helm template p2p-static "$chart" --namespace dragonfly-system --values "$values" \
  --set manager.enabled=true --set manager.host=manager.example.com \
  --set manager.tlsSecret=manager-tls >"$tmp/p2p-static-manager.yaml"
grep -q 'kind: IngressRoute' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager IngressRoute missing"
grep -q 'PathPrefix(`/bin`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /bin path missing"
grep -q 'PathPrefix(`/nydus`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /nydus path missing"
grep -q 'PathPrefix(`/client`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /client path missing"
grep -q 'PathPrefix(`/snapshotter`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /snapshotter path missing"
grep -q 'PathPrefix(`/p2p-install.sh`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager route is missing /p2p-install.sh"
grep -q 'PathPrefix(`/container-init.sh`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager route is missing /container-init.sh"

# Exercise the populate script with a fake downloader: versioned/arch layouts
# for both architectures, legacy /bin from the node arch, existing dfdaemon kept.
populate="$chart/files/populate-static.sh"
static_dir="$tmp/static"
populate_bin="$tmp/populate-bin"
populate_plan="$tmp/populate-plan.txt"
cli_amd_src="$tmp/cli-amd/dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu"
cli_arm_src="$tmp/cli-arm/dragonfly-client-v1.5.14-aarch64-unknown-linux-gnu"
nyd_amd_src="$tmp/nyd-amd/nydus-static"
nyd_arm_src="$tmp/nyd-arm/nydus-static"
mkdir -p "$static_dir/bin" "$populate_bin" "$cli_amd_src" "$cli_arm_src" "$nyd_amd_src" "$nyd_arm_src"
printf 'existing-dfdaemon\n' > "$static_dir/bin/dfdaemon"
for binary in nydusd nydus-image nydus-overlayfs; do
  printf 'amd64-%s\n' "$binary" > "$nyd_amd_src/$binary"
  printf 'arm64-%s\n' "$binary" > "$nyd_arm_src/$binary"
done
for binary in dfdaemon dfget; do
  printf 'amd64-%s\n' "$binary" > "$cli_amd_src/$binary"
  printf 'arm64-%s\n' "$binary" > "$cli_arm_src/$binary"
done
tar -czf "$tmp/cli-amd.tar.gz" -C "$tmp/cli-amd" dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu
tar -czf "$tmp/cli-arm.tar.gz" -C "$tmp/cli-arm" dragonfly-client-v1.5.14-aarch64-unknown-linux-gnu
tar -czf "$tmp/nyd-amd.tgz" -C "$tmp/nyd-amd" nydus-static
tar -czf "$tmp/nyd-arm.tgz" -C "$tmp/nyd-arm" nydus-static
printf '#!/bin/sh\nexit 0\n' > "$tmp/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz"
printf '#!/bin/sh\nexit 0\n' > "$tmp/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz"
sha_cli_amd="$(sha256sum "$tmp/cli-amd.tar.gz" | cut -d' ' -f1)"
sha_cli_arm="$(sha256sum "$tmp/cli-arm.tar.gz" | cut -d' ' -f1)"
sha_nyd_amd="$(sha256sum "$tmp/nyd-amd.tgz" | cut -d' ' -f1)"
sha_nyd_arm="$(sha256sum "$tmp/nyd-arm.tgz" | cut -d' ' -f1)"
sha_snap_amd="$(sha256sum "$tmp/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz" | cut -d' ' -f1)"
sha_snap_arm="$(sha256sum "$tmp/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz" | cut -d' ' -f1)"
cat > "$populate_plan" <<EOF
client|v1.5.14|amd64|https://example.test/cli-amd.tar.gz|${sha_cli_amd}|dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu
client|v1.5.14|arm64|https://example.test/cli-arm.tar.gz|${sha_cli_arm}|dragonfly-client-v1.5.14-aarch64-unknown-linux-gnu
compat-client|v1.5.14
nydus|v2.4.5|amd64|https://example.test/nyd-amd.tgz|${sha_nyd_amd}|nydus-static
nydus|v2.4.5|arm64|https://example.test/nyd-arm.tgz|${sha_nyd_arm}|nydus-static
snapshotter|v0.16.0|amd64|https://example.test/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz|${sha_snap_amd}|nydus-snapshotter-v0.16.0-linux-amd64.tar.gz
snapshotter|v0.16.0|arm64|https://example.test/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz|${sha_snap_arm}|nydus-snapshotter-v0.16.0-linux-arm64.tar.gz
EOF
cat > "$populate_bin/curl" <<'POPULATECURL'
#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    --retry|--retry-delay|--connect-timeout|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [ -n "${POPULATE_CURL_LOG:-}" ]; then
  printf '%s\n' "$url" >> "$POPULATE_CURL_LOG"
fi
case "$url" in
  *cli-amd.tar.gz.sha256*|*cli-amd.tar.gz.sha256sum)
    sha="$(sha256sum "${POPULATE_CLI_AMD:?}")"; sha="${sha%% *}"
    printf '%s  cli-amd.tar.gz\n' "$sha" > "$out" ;;
  *cli-arm.tar.gz.sha256*|*cli-arm.tar.gz.sha256sum)
    sha="$(sha256sum "${POPULATE_CLI_ARM:?}")"; sha="${sha%% *}"
    printf '%s  cli-arm.tar.gz\n' "$sha" > "$out" ;;
  *nyd-amd.tgz.sha256*|*nyd-amd.tgz.sha256sum)
    sha="$(sha256sum "${POPULATE_NYD_AMD:?}")"; sha="${sha%% *}"
    printf '%s  nyd-amd.tgz\n' "$sha" > "$out" ;;
  *nyd-arm.tgz.sha256*|*nyd-arm.tgz.sha256sum)
    sha="$(sha256sum "${POPULATE_NYD_ARM:?}")"; sha="${sha%% *}"
    printf '%s  nyd-arm.tgz\n' "$sha" > "$out" ;;
  *nydus-snapshotter-v0.16.0-linux-amd64.tar.gz.sha256*|*nydus-snapshotter-v0.16.0-linux-amd64.tar.gz.sha256sum)
    sha="$(sha256sum "${POPULATE_SNAPSHOTTER_AMD:?}")"; sha="${sha%% *}"
    printf '%s  snapshotter\n' "$sha" > "$out" ;;
  *nydus-snapshotter-v0.16.0-linux-arm64.tar.gz.sha256*|*nydus-snapshotter-v0.16.0-linux-arm64.tar.gz.sha256sum)
    sha="$(sha256sum "${POPULATE_SNAPSHOTTER_ARM:?}")"; sha="${sha%% *}"
    printf '%s  snapshotter\n' "$sha" > "$out" ;;
  *cli-amd.tar.gz) cp "${POPULATE_CLI_AMD:?}" "$out" ;;
  *cli-arm.tar.gz) cp "${POPULATE_CLI_ARM:?}" "$out" ;;
  *nyd-amd.tgz) cp "${POPULATE_NYD_AMD:?}" "$out" ;;
  *nyd-arm.tgz) cp "${POPULATE_NYD_ARM:?}" "$out" ;;
  *nydus-snapshotter-v0.16.0-linux-amd64.tar.gz) cp "${POPULATE_SNAPSHOTTER_AMD:?}" "$out" ;;
  *nydus-snapshotter-v0.16.0-linux-arm64.tar.gz) cp "${POPULATE_SNAPSHOTTER_ARM:?}" "$out" ;;
  *) exit 22 ;;
esac
POPULATECURL
chmod 0755 "$populate_bin/curl"
STATIC_DIR="$static_dir" SELECTOR_SRC="$chart/files/nydus-fs-select.sh" \
  POPULATE_PLAN="$populate_plan" DOWNLOAD_RETRIES=1 SELECTOR_ENABLED=1 TARGET_ARCH=x86_64 \
  NYDUS_BINARIES="nydusd nydus-image nydus-overlayfs" \
  POPULATE_CLI_AMD="$tmp/cli-amd.tar.gz" POPULATE_CLI_ARM="$tmp/cli-arm.tar.gz" \
  POPULATE_NYD_AMD="$tmp/nyd-amd.tgz" POPULATE_NYD_ARM="$tmp/nyd-arm.tgz" \
  POPULATE_SNAPSHOTTER_AMD="$tmp/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz" \
  POPULATE_SNAPSHOTTER_ARM="$tmp/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz" \
  POPULATE_CURL_LOG="$tmp/populate-curl.log" \
  PATH="$populate_bin:$PATH" \
  sh "$populate" >"$tmp/populate.log" 2>&1
for arch in amd64 arm64; do
  for binary in dfdaemon dfget; do
    [ -s "$static_dir/client/v1.5.14/$arch/$binary" ] || fail "populate-static did not create client/v1.5.14/$arch/$binary"
  done
  for binary in nydusd nydus-image nydus-overlayfs; do
    [ -s "$static_dir/nydus/v2.4.5/$arch/$binary" ] || fail "populate-static did not create nydus/v2.4.5/$arch/$binary"
  done
  [ -s "$static_dir/nydus/v2.4.5/$arch/nydus-static.tgz" ] || fail "populate-static did not mirror nydus/v2.4.5/$arch/nydus-static.tgz"
done
[ -s "$static_dir/snapshotter/v0.16.0/amd64/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz" ] || fail "populate-static did not mirror the amd64 snapshotter"
[ -s "$static_dir/snapshotter/v0.16.0/arm64/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz" ] || fail "populate-static did not mirror the arm64 snapshotter"
for binary in dfget nydusd nydus-image nydus-overlayfs nydus-fs-select nydus-snapshotter-v0.16.0-linux-amd64.tar.gz; do
  [ -s "$static_dir/bin/$binary" ] || fail "populate-static did not create the /bin compatibility $binary"
done
[ -s "$static_dir/nydus/v2.4.5/nydusd" ] || fail "populate-static did not create the legacy nydus/v2.4.5/nydusd"
[ -s "$static_dir/nydus/v2.4.5/nydus-static.tgz" ] || fail "populate-static did not create the legacy nydus/v2.4.5/nydus-static.tgz"
grep -q 'preserving existing' "$tmp/populate.log" || fail "populate-static did not preserve the existing dfdaemon"
if grep -q 'cli-amd.tar.gz.sha256' "$tmp/populate-curl.log"; then
  fail "populate-static probed a nonexistent client checksum sidecar"
fi
grep -q 'nyd-amd.tgz.sha256' "$tmp/populate-curl.log" || fail "populate-static did not fetch the nydus checksum sidecar"
[ "$(cat "$static_dir/bin/dfdaemon")" = "existing-dfdaemon" ] || fail "populate-static overwrote the existing dfdaemon"
[ "$(cat "$static_dir/bin/nydusd")" = "amd64-nydusd" ] || fail "populate-static did not fill /bin from the amd64 layout"

# A second run on an arm64 node uses the arm64 compatibility layout.
static_dir_arm="$tmp/static-arm"
mkdir -p "$static_dir_arm"
STATIC_DIR="$static_dir_arm" SELECTOR_SRC="$chart/files/nydus-fs-select.sh" \
  POPULATE_PLAN="$populate_plan" DOWNLOAD_RETRIES=1 SELECTOR_ENABLED=1 TARGET_ARCH=aarch64 \
  NYDUS_BINARIES="nydusd nydus-image nydus-overlayfs" \
  POPULATE_CLI_AMD="$tmp/cli-amd.tar.gz" POPULATE_CLI_ARM="$tmp/cli-arm.tar.gz" \
  POPULATE_NYD_AMD="$tmp/nyd-amd.tgz" POPULATE_NYD_ARM="$tmp/nyd-arm.tgz" \
  POPULATE_SNAPSHOTTER_AMD="$tmp/nydus-snapshotter-v0.16.0-linux-amd64.tar.gz" \
  POPULATE_SNAPSHOTTER_ARM="$tmp/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz" \
  PATH="$populate_bin:$PATH" \
  sh "$populate" >"$tmp/populate-arm.log" 2>&1
[ "$(cat "$static_dir_arm/bin/dfdaemon")" = "arm64-dfdaemon" ] || fail "populate-static did not fill /bin from the arm64 layout"
[ "$(cat "$static_dir_arm/bin/nydusd")" = "arm64-nydusd" ] || fail "populate-static did not fill /bin nydusd from the arm64 layout"
[ -s "$static_dir_arm/client/v1.5.14/arm64/dfget" ] || fail "populate-static did not create the arm64 client layout"
[ -s "$static_dir_arm/bin/nydus-snapshotter-v0.16.0-linux-arm64.tar.gz" ] || fail "populate-static did not fill the arm64 /bin snapshotter"

echo "p2p-static render checks passed"
