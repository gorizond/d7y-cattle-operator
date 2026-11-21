#!/bin/sh
# Populate the p2p-static volume from pinned release artifacts.
#
# The nginx container serves this volume read-only. Nydus assets are always
# written atomically to both the versioned (/nydus/<version>/) and legacy
# (/bin/) layouts so old and new host scripts find them. An existing
# /bin/dfdaemon is preserved: the live volume already carries it, and the chart
# only fills it in when the volume is fresh.
set -eu

static_dir="${STATIC_DIR:-/usr/share/nginx/static}"
selector_src="${SELECTOR_SRC:-/scripts/nydus-fs-select.sh}"
retries="${DOWNLOAD_RETRIES:-5}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

log() { printf 'p2p-static: %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

fetch() {
  url="$1"
  out="$2"
  log "downloading ${url}"
  curl -fL --retry "$retries" --retry-delay 5 --retry-all-errors \
    --connect-timeout 10 --max-time 600 -o "$out" "$url" \
    || die "download failed: ${url}"
}

verify() {
  file="$1"
  expected="$2"
  label="$3"
  [ -n "$expected" ] || die "missing SHA256 for ${label}"
  actual="$(sha256sum "$file")"
  actual="${actual%% *}"
  [ "$actual" = "$expected" ] \
    || die "checksum mismatch for ${label}: got ${actual} want ${expected}"
}

install_file() {
  src="$1"
  dst="$2"
  mkdir -p "$(dirname "$dst")"
  cp "$src" "$dst.tmp.$$"
  chmod 0755 "$dst.tmp.$$"
  mv -f "$dst.tmp.$$" "$dst"
}

need_nydus=0
for binary in $NYDUS_BINARIES; do
  if [ ! -s "$static_dir/nydus/$NYDUS_VERSION/$binary" ] || [ ! -s "$static_dir/bin/$binary" ]; then
    need_nydus=1
  fi
done

if [ "$need_nydus" = 1 ]; then
  fetch "$NYDUS_TARBALL_URL" "$tmp/nydus.tgz"
  verify "$tmp/nydus.tgz" "$NYDUS_TARBALL_SHA256" "nydus $NYDUS_VERSION"
  mkdir -p "$tmp/nydus"
  tar -xzf "$tmp/nydus.tgz" -C "$tmp/nydus" \
    || die "invalid nydus tarball: ${NYDUS_TARBALL_URL}"
  for binary in $NYDUS_BINARIES; do
    src="$tmp/nydus/$NYDUS_TARBALL_ROOT/$binary"
    [ -s "$src" ] || die "nydus tarball is missing ${binary}"
    install_file "$src" "$static_dir/nydus/$NYDUS_VERSION/$binary"
    install_file "$src" "$static_dir/bin/$binary"
  done
else
  log "nydus $NYDUS_VERSION assets already present"
fi

if [ "${SELECTOR_ENABLED:-1}" = "1" ]; then
  [ -s "$selector_src" ] || die "missing nydus-fs-select script: ${selector_src}"
  install_file "$selector_src" "$static_dir/nydus/$NYDUS_VERSION/nydus-fs-select"
  install_file "$selector_src" "$static_dir/bin/nydus-fs-select"
fi

if [ "${DFDAEMON_ENABLED:-1}" = "1" ]; then
  if [ -s "$static_dir/bin/dfdaemon" ]; then
    log "preserving existing ${static_dir}/bin/dfdaemon"
  else
    fetch "$DFDAEMON_TARBALL_URL" "$tmp/dfdaemon.tgz"
    verify "$tmp/dfdaemon.tgz" "$DFDAEMON_TARBALL_SHA256" "dfdaemon $DFDAEMON_VERSION"
    mkdir -p "$tmp/dfdaemon"
    tar -xzf "$tmp/dfdaemon.tgz" -C "$tmp/dfdaemon" \
      || die "invalid dfdaemon tarball: ${DFDAEMON_TARBALL_URL}"
    src="$tmp/dfdaemon/$DFDAEMON_TARBALL_ROOT/$DFDAEMON_BINARY"
    [ -s "$src" ] || die "dfdaemon tarball is missing ${DFDAEMON_BINARY}"
    install_file "$src" "$static_dir/bin/dfdaemon"
  fi
fi

log "static assets ready in ${static_dir}"
