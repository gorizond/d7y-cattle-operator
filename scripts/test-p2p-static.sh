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
grep -q 'DFGET_BINARY' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the dfget populate input"
grep -q 'nydus-static.tgz' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the nydus tarball mirror"
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

# Exercise the populate script with a fake downloader: nydus assets land in
# both layouts, while an existing dfdaemon is preserved.
populate="$chart/files/populate-static.sh"
static_dir="$tmp/static"
populate_bin="$tmp/populate-bin"
populate_src="$tmp/populate-src/nydus-static"
populate_tgz="$tmp/populate-nydus.tgz"
populate_client_src="$tmp/populate-client/dragonfly-client-v1.5.14-x86_64-unknown-linux-musl"
populate_client_tgz="$tmp/populate-client.tgz"
mkdir -p "$static_dir/bin" "$populate_bin" "$populate_src" "$populate_client_src"
printf 'existing-dfdaemon\n' > "$static_dir/bin/dfdaemon"
for binary in nydusd nydus-image nydus-overlayfs; do
  printf '#!/bin/sh\nexit 0\n' > "$populate_src/$binary"
done
for binary in dfdaemon dfget; do
  printf '#!/bin/sh\nexit 0\n' > "$populate_client_src/$binary"
done
tar -czf "$populate_tgz" -C "$tmp/populate-src" nydus-static
tar -czf "$populate_client_tgz" -C "$tmp/populate-client" dragonfly-client-v1.5.14-x86_64-unknown-linux-musl
populate_sha="$(sha256sum "$populate_tgz" | cut -d' ' -f1)"
populate_client_sha="$(sha256sum "$populate_client_tgz" | cut -d' ' -f1)"
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
  *client*.tar.gz) cp "${POPULATE_CLIENT_TARBALL:?}" "$out" ;;
  *) cp "${POPULATE_TARBALL:?}" "$out" ;;
esac
POPULATECURL
chmod 0755 "$populate_bin/curl"
STATIC_DIR="$static_dir" SELECTOR_SRC="$chart/files/nydus-fs-select.sh" \
  DOWNLOAD_RETRIES=1 SELECTOR_ENABLED=1 \
  NYDUS_VERSION=v2.4.5 NYDUS_BINARIES="nydusd nydus-image nydus-overlayfs" \
  NYDUS_TARBALL_URL=https://example.test/nydus.tgz NYDUS_TARBALL_SHA256="$populate_sha" \
  NYDUS_TARBALL_ROOT=nydus-static \
  DFDAEMON_ENABLED=1 DFDAEMON_VERSION=1.5.14 \
  DFDAEMON_TARBALL_URL=https://example.test/dragonfly-client-v1.5.14-x86_64-unknown-linux-musl.tar.gz \
  DFDAEMON_TARBALL_SHA256="$populate_client_sha" \
  DFDAEMON_TARBALL_ROOT=dragonfly-client-v1.5.14-x86_64-unknown-linux-musl \
  DFDAEMON_BINARY=dfdaemon DFGET_BINARY=dfget \
  POPULATE_TARBALL="$populate_tgz" POPULATE_CLIENT_TARBALL="$populate_client_tgz" \
  PATH="$populate_bin:$PATH" \
  sh "$populate" >"$tmp/populate.log" 2>&1
for binary in nydusd nydus-image nydus-overlayfs nydus-fs-select; do
  [ -s "$static_dir/nydus/v2.4.5/$binary" ] || fail "populate-static did not create nydus/v2.4.5/$binary"
  [ -s "$static_dir/bin/$binary" ] || fail "populate-static did not create bin/$binary"
done
grep -q 'preserving existing' "$tmp/populate.log" || fail "populate-static did not preserve the existing dfdaemon"
[ "$(cat "$static_dir/bin/dfdaemon")" = "existing-dfdaemon" ] || fail "populate-static overwrote the existing dfdaemon"
[ -s "$static_dir/bin/dfget" ] || fail "populate-static did not create bin/dfget"
[ -s "$static_dir/nydus/v2.4.5/nydus-static.tgz" ] || fail "populate-static did not mirror nydus-static.tgz"

echo "p2p-static render checks passed"
