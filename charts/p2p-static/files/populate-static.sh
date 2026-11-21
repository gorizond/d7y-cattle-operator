#!/bin/sh
# Populate the p2p-static volume from pinned release artifacts.
#
# Versioned/architecture layouts are authoritative:
#   /client/<version>/<arch>/{dfdaemon,dfget}
#   /nydus/<version>/<arch>/{nydusd,nydus-image,nydus-overlayfs,nydus-static.tgz}
#   /snapshotter/<version>/<arch>/<asset>
# The legacy /bin and /nydus/<version> compatibility paths are filled from the
# node architecture only when absent. The chart uses emptyDir, never a PVC.
set -eu

static_dir="${STATIC_DIR:-/usr/share/nginx/static}"
selector_src="${SELECTOR_SRC:-/scripts/nydus-fs-select.sh}"
plan="${POPULATE_PLAN:-/scripts/populate-plan.txt}"
retries="${DOWNLOAD_RETRIES:-5}"
nydus_binaries="${NYDUS_BINARIES:-nydusd nydus-image nydus-overlayfs}"
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

# Prefer the release-provided checksum sidecar. A pinned fallback exists only
# for a client release that publishes no checksum asset (v1.5.14).
resolve_checksum() {
  url="$1"
  expected="$2"
  kind="$3"
  version="$4"
  arch="$5"
  for suffix in .sha256sum .sha256; do
    sum_file="${tmp}/checksum.$$"
    if curl -fsSL --retry 3 --connect-timeout 10 --max-time 60 -o "$sum_file" "${url}${suffix}" && [ -s "$sum_file" ]; then
      sum="$(cut -d' ' -f1 < "$sum_file")"
      rm -f "$sum_file"
      if [ -n "$sum" ]; then
        printf '%s' "$sum"
        return 0
      fi
    fi
    rm -f "$sum_file"
  done
  if [ -n "$expected" ]; then
    printf '%s' "$expected"
    return 0
  fi
  case "${kind}:${version}:${arch}" in
    client:v1.5.14:amd64) printf '%s' 'ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7' ;;
    client:v1.5.14:arm64) printf '%s' '370ff792a1c6957a7244b770d6cd12792668e326b2282b3017fea2cbfa3510af' ;;
    *) return 1 ;;
  esac
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

install_file_mode() {
  src="$1"
  dst="$2"
  mode="$3"
  mkdir -p "$(dirname "$dst")"
  cp "$src" "$dst.tmp.$$"
  chmod "$mode" "$dst.tmp.$$"
  mv -f "$dst.tmp.$$" "$dst"
}

install_file() {
  install_file_mode "$1" "$2" 0755
}

node_arch=""
case "${TARGET_ARCH:-$(uname -m)}" in
  x86_64|amd64) node_arch=amd64 ;;
  aarch64|arm64) node_arch=arm64 ;;
  armv7l|armhf|armv6l)
    log "unsupported node architecture ${TARGET_ARCH:-$(uname -m)}: populating versioned layouts only"
    ;;
  *)
    log "unsupported node architecture ${TARGET_ARCH:-$(uname -m)}: populating versioned layouts only"
    ;;
esac

[ -s "$plan" ] || die "missing populate plan: ${plan}"

compat_client=""
nydus_version=""
snapshotter_version=""
snapshotter_asset=""
snapshotter_arch=""

while IFS='|' read -r kind version arch url sha root; do
  [ -n "${kind:-}" ] || continue
  case "$kind" in
    client)
      [ -n "$version" ] && [ -n "$arch" ] || die "invalid client plan line: ${kind}|${version}|${arch}"
      dest="$static_dir/client/$version/$arch"
      if [ -s "$dest/dfdaemon" ] && [ -s "$dest/dfget" ]; then
        log "client $version/$arch already present"
        continue
      fi
      fetch "$url" "$tmp/client.tgz"
      resolved="$(resolve_checksum "$url" "$sha" client "$version" "$arch")" \
        || die "no checksum available for client $version/$arch: ${url}"
      verify "$tmp/client.tgz" "$resolved" "client $version/$arch"
      extract="$tmp/client-$version-$arch"
      mkdir -p "$extract"
      tar -xzf "$tmp/client.tgz" -C "$extract" \
        || die "invalid client tarball: ${url}"
      for binary in dfdaemon dfget; do
        if [ -s "$dest/$binary" ]; then
          log "preserving existing $dest/$binary"
          continue
        fi
        src="$extract/$root/$binary"
        [ -s "$src" ] || die "client tarball is missing ${binary} for $version/$arch"
        install_file "$src" "$dest/$binary"
      done
      ;;
    compat-client)
      compat_client="$version"
      ;;
    nydus)
      [ -n "$version" ] && [ -n "$arch" ] || die "invalid nydus plan line: ${kind}|${version}|${arch}"
      nydus_version="$version"
      dest="$static_dir/nydus/$version/$arch"
      need=0
      for binary in $nydus_binaries; do
        [ -s "$dest/$binary" ] || need=1
      done
      [ -s "$dest/nydus-static.tgz" ] || need=1
      if [ "$need" = 1 ]; then
        fetch "$url" "$tmp/nydus-$version-$arch.tgz"
        resolved="$(resolve_checksum "$url" "$sha" nydus "$version" "$arch")" \
          || die "no checksum available for nydus $version/$arch: ${url}"
        verify "$tmp/nydus-$version-$arch.tgz" "$resolved" "nydus $version/$arch"
        extract="$tmp/nydus-extract-$version-$arch"
        mkdir -p "$extract"
        tar -xzf "$tmp/nydus-$version-$arch.tgz" -C "$extract" \
          || die "invalid nydus tarball: ${url}"
        for binary in $nydus_binaries; do
          if [ -s "$dest/$binary" ]; then
            log "preserving existing $dest/$binary"
            continue
          fi
          src="$extract/$root/$binary"
          [ -s "$src" ] || die "nydus tarball is missing ${binary} for $version/$arch"
          install_file "$src" "$dest/$binary"
        done
        install_file_mode "$tmp/nydus-$version-$arch.tgz" "$dest/nydus-static.tgz" 0644
      else
        log "nydus $version/$arch already present"
      fi
      ;;
    snapshotter)
      [ -n "$version" ] && [ -n "$arch" ] && [ -n "$root" ] || die "invalid snapshotter plan line: ${kind}|${version}|${arch}"
      dest="$static_dir/snapshotter/$version/$arch"
      if [ -s "$dest/$root" ]; then
        log "snapshotter $version/$arch/$root already present"
      else
        fetch "$url" "$tmp/snapshotter-$version-$arch"
        resolved="$(resolve_checksum "$url" "$sha" snapshotter "$version" "$arch")" \
          || die "no checksum available for snapshotter $version/$arch/$root: ${url}"
        verify "$tmp/snapshotter-$version-$arch" "$resolved" "snapshotter $version/$arch/$root"
        install_file "$tmp/snapshotter-$version-$arch" "$dest/$root"
      fi
      if [ "$arch" = "$node_arch" ]; then
        snapshotter_version="$version"
        snapshotter_asset="$root"
        snapshotter_arch="$arch"
      fi
      ;;
    *)
      die "unknown populate plan kind: ${kind}"
      ;;
  esac
done < "$plan"

# Legacy compatibility layouts. Never overwrite an existing /bin binary.
if [ -n "$node_arch" ]; then
  if [ -n "$compat_client" ]; then
    for binary in dfdaemon dfget; do
      if [ -s "$static_dir/bin/$binary" ]; then
        log "preserving existing ${static_dir}/bin/$binary"
        continue
      fi
      src="$static_dir/client/$compat_client/$node_arch/$binary"
      if [ -s "$src" ]; then
        install_file "$src" "$static_dir/bin/$binary"
      else
        log "compat client $compat_client/$node_arch/$binary is not populated"
      fi
    done
  fi

  if [ -n "$nydus_version" ]; then
    for binary in $nydus_binaries; do
      src="$static_dir/nydus/$nydus_version/$node_arch/$binary"
      if [ -s "$static_dir/bin/$binary" ]; then
        log "preserving existing ${static_dir}/bin/$binary"
      elif [ -s "$src" ]; then
        install_file "$src" "$static_dir/bin/$binary"
      fi
      if [ ! -s "$static_dir/nydus/$nydus_version/$binary" ] && [ -s "$src" ]; then
        install_file "$src" "$static_dir/nydus/$nydus_version/$binary"
      fi
    done
    if [ ! -s "$static_dir/nydus/$nydus_version/nydus-static.tgz" ]; then
      src="$static_dir/nydus/$nydus_version/$node_arch/nydus-static.tgz"
      [ -s "$src" ] && install_file_mode "$src" "$static_dir/nydus/$nydus_version/nydus-static.tgz" 0644
    fi
  fi

  if [ "${SELECTOR_ENABLED:-1}" = "1" ]; then
    [ -s "$selector_src" ] || die "missing nydus-fs-select script: ${selector_src}"
    if [ -n "$nydus_version" ]; then
      install_file "$selector_src" "$static_dir/nydus/$nydus_version/$node_arch/nydus-fs-select"
      install_file "$selector_src" "$static_dir/nydus/$nydus_version/nydus-fs-select"
    fi
    install_file "$selector_src" "$static_dir/bin/nydus-fs-select"
  fi

  if [ -n "$snapshotter_version" ] && [ -n "$snapshotter_asset" ]; then
    if [ ! -s "$static_dir/bin/$snapshotter_asset" ]; then
      src="$static_dir/snapshotter/$snapshotter_version/$snapshotter_arch/$snapshotter_asset"
      [ -s "$src" ] && install_file "$src" "$static_dir/bin/$snapshotter_asset"
    fi
  fi
fi

log "static assets ready in ${static_dir} for node architecture ${node_arch:-unknown}"
