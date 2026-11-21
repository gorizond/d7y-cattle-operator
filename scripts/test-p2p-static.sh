#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$root/pkg/machinebootstrap/assets/p2p-install.sh"
selector="$root/pkg/machinebootstrap/assets/nydus-fs-select.sh"
self_test="$root/pkg/machinebootstrap/assets/test.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

bash -n "$script" || fail "p2p-install.sh has a syntax error"
bash -n "$selector" || fail "nydus-fs-select.sh has a syntax error"
if command -v shellcheck >/dev/null 2>&1; then
  shellcheck "$script" "$selector" "$self_test" || fail "shellcheck failed"
else
  echo "shellcheck not installed; skipping shellcheck"
fi

bash "$self_test" || fail "embedded p2p-install self-test failed"

chart="$root/charts/p2p-static"
cmp -s "$chart/files/nydus-fs-select.sh" "$selector" || fail "chart nydus-fs-select.sh differs from the embedded asset"
bash -n "$chart/files/populate-static.sh" || fail "populate-static.sh has a syntax error"
bash -n "$chart/files/nydus-fs-select.sh" || fail "chart nydus-fs-select.sh has a syntax error"

helm template p2p-static "$chart" --namespace dragonfly-system >"$tmp/p2p-static.yaml"
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
grep -q 'nydus-snapshotter-linux-amd64' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the snapshotter mirror"
grep -q '8f4f68f306ba391cd294cdcb273267732cb82f4ae466564013204198dd003b0b' "$tmp/p2p-static.yaml" || fail "p2p-static snapshotter checksum is not asserted"
grep -q 'client|v1.5.14|arm64|' "$tmp/p2p-static.yaml" || fail "p2p-static populate plan is missing the arm64 client layout"
grep -q 'nydus|v2.4.5|arm64|' "$tmp/p2p-static.yaml" || fail "p2p-static populate plan is missing the arm64 nydus layout"
grep -q 'location /nydus/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the versioned nydus asset route"
grep -q 'location /bin/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the /bin asset route"
grep -q 'listen 80;' "$tmp/p2p-static.yaml" || fail "p2p-static nginx must listen on port 80"
grep -q 'listen \[::\]:80;' "$tmp/p2p-static.yaml" || fail "p2p-static nginx must listen on IPv6 localhost too"
if grep -q 'p2p-install.sh' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not serve p2p-install.sh"
fi

helm template p2p-static "$chart" --namespace dragonfly-system \
  --set manager.enabled=true --set manager.host=manager.example.com \
  --set manager.tlsSecret=manager-tls >"$tmp/p2p-static-manager.yaml"
grep -q 'kind: IngressRoute' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager IngressRoute missing"
grep -q 'PathPrefix(`/bin`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /bin path missing"
grep -q 'PathPrefix(`/nydus`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /nydus path missing"
if grep -q 'p2p-install.sh' "$tmp/p2p-static-manager.yaml"; then
  fail "p2p-static manager route must not include p2p-install.sh"
fi

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
printf '#!/bin/sh\nexit 0\n' > "$tmp/snapshotter"
sha_cli_amd="$(sha256sum "$tmp/cli-amd.tar.gz" | cut -d' ' -f1)"
sha_cli_arm="$(sha256sum "$tmp/cli-arm.tar.gz" | cut -d' ' -f1)"
sha_nyd_amd="$(sha256sum "$tmp/nyd-amd.tgz" | cut -d' ' -f1)"
sha_nyd_arm="$(sha256sum "$tmp/nyd-arm.tgz" | cut -d' ' -f1)"
sha_snap="$(sha256sum "$tmp/snapshotter" | cut -d' ' -f1)"
cat > "$populate_plan" <<EOF
client|v1.5.14|amd64|https://example.test/cli-amd.tar.gz|${sha_cli_amd}|dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu
client|v1.5.14|arm64|https://example.test/cli-arm.tar.gz|${sha_cli_arm}|dragonfly-client-v1.5.14-aarch64-unknown-linux-gnu
compat-client|v1.5.14
nydus|v2.4.5|amd64|https://example.test/nyd-amd.tgz|${sha_nyd_amd}|nydus-static
nydus|v2.4.5|arm64|https://example.test/nyd-arm.tgz|${sha_nyd_arm}|nydus-static
snapshotter|v0.16.0-p2p.2|amd64|https://example.test/snapshotter|${sha_snap}|nydus-snapshotter-linux-amd64
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
case "$url" in
  *cli-amd.tar.gz) cp "${POPULATE_CLI_AMD:?}" "$out" ;;
  *cli-arm.tar.gz) cp "${POPULATE_CLI_ARM:?}" "$out" ;;
  *nyd-amd.tgz) cp "${POPULATE_NYD_AMD:?}" "$out" ;;
  *nyd-arm.tgz) cp "${POPULATE_NYD_ARM:?}" "$out" ;;
  *snapshotter) cp "${POPULATE_SNAPSHOTTER:?}" "$out" ;;
  *) exit 22 ;;
esac
POPULATECURL
chmod 0755 "$populate_bin/curl"
STATIC_DIR="$static_dir" SELECTOR_SRC="$chart/files/nydus-fs-select.sh" \
  POPULATE_PLAN="$populate_plan" DOWNLOAD_RETRIES=1 SELECTOR_ENABLED=1 TARGET_ARCH=x86_64 \
  NYDUS_BINARIES="nydusd nydus-image nydus-overlayfs" \
  POPULATE_CLI_AMD="$tmp/cli-amd.tar.gz" POPULATE_CLI_ARM="$tmp/cli-arm.tar.gz" \
  POPULATE_NYD_AMD="$tmp/nyd-amd.tgz" POPULATE_NYD_ARM="$tmp/nyd-arm.tgz" \
  POPULATE_SNAPSHOTTER="$tmp/snapshotter" \
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
[ -s "$static_dir/snapshotter/v0.16.0-p2p.2/amd64/nydus-snapshotter-linux-amd64" ] || fail "populate-static did not mirror the amd64 snapshotter"
for binary in dfget nydusd nydus-image nydus-overlayfs nydus-fs-select nydus-snapshotter-linux-amd64; do
  [ -s "$static_dir/bin/$binary" ] || fail "populate-static did not create the /bin compatibility $binary"
done
[ -s "$static_dir/nydus/v2.4.5/nydusd" ] || fail "populate-static did not create the legacy nydus/v2.4.5/nydusd"
[ -s "$static_dir/nydus/v2.4.5/nydus-static.tgz" ] || fail "populate-static did not create the legacy nydus/v2.4.5/nydus-static.tgz"
grep -q 'preserving existing' "$tmp/populate.log" || fail "populate-static did not preserve the existing dfdaemon"
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
  POPULATE_SNAPSHOTTER="$tmp/snapshotter" \
  PATH="$populate_bin:$PATH" \
  sh "$populate" >"$tmp/populate-arm.log" 2>&1
[ "$(cat "$static_dir_arm/bin/dfdaemon")" = "arm64-dfdaemon" ] || fail "populate-static did not fill /bin from the arm64 layout"
[ "$(cat "$static_dir_arm/bin/nydusd")" = "arm64-nydusd" ] || fail "populate-static did not fill /bin nydusd from the arm64 layout"
[ -s "$static_dir_arm/client/v1.5.14/arm64/dfget" ] || fail "populate-static did not create the arm64 client layout"

echo "p2p-static render checks passed"
