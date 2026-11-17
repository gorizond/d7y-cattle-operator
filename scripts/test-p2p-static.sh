#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$root/charts/p2p-static/files/p2p-install.sh"
self_test="$root/charts/p2p-static/files/test.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

bash -n "$script" || fail "p2p-install.sh has a syntax error"
if command -v shellcheck >/dev/null 2>&1; then
  shellcheck "$script" "$self_test" || fail "shellcheck failed"
else
  echo "shellcheck not installed; skipping shellcheck"
fi

bash "$self_test" || fail "p2p-install.sh self-test failed"

helm template p2p-static "$root/charts/p2p-static" --namespace dragonfly-system >"$tmp/p2p-static.yaml"
grep -q 'p2p-install.sh.tmpl' "$tmp/p2p-static.yaml" || fail "p2p-static ConfigMap is missing the install template"
grep -q 'location /nydus/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the versioned nydus asset route"
grep -q 'fallback_direct' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the fail-open fallback"
grep -q 'SCHEDULER_HOST' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the scheduler host payload"
if grep -q 'enable_oci_tar' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not assume enable_oci_tar"
fi
grep -q 'Direct-registry fallback' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the direct-registry template"
grep -q 'v0.16.0' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the pinned nydus version"
grep -q 'nydus-overlayfs' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing nydus-overlayfs"
grep -q 'nydus-updater' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the host nydus updater"
grep -q 'use_local_image_pull' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the global nydus drop-in"
grep -q 'enable_referrer_detect = true' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing enable_referrer_detect"
grep -q 'enable_index_detect = true' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing enable_index_detect"
grep -q '"fs_prefetch": {' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing nydusd fs_prefetch"
grep -q '"threads_count": 8' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing nydusd prefetch threads"
grep -q '"type": "blobcache"' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing nydusd blobcache"
grep -q 'dfdaemon-bootstrap-switch.timer' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the host bootstrap switch timer"
grep -q 'nydus-resolver.service' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the host resolver unit"
grep -q 'ExecStart=/usr/local/bin/nydus-resolver --config /etc/nydus-resolver/config.yaml' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the resolver exec"
grep -q 'Environment=HTTPS_PROXY=http://127.0.0.1:3999' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the snapshotter HTTPS_PROXY"
grep -q 'Environment=SSL_CERT_FILE=/var/lib/nydus-resolver/ca-bundle.pem' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the snapshotter CA bundle"
grep -q 'After=network-online.target nydus-resolver.service' "$tmp/p2p-static.yaml" || fail "snapshotter does not order after nydus-resolver"
grep -q 'Wants=network-online.target nydus-resolver.service' "$tmp/p2p-static.yaml" || fail "snapshotter does not want nydus-resolver"
grep -q 'RESOLVER_UPSTREAM' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the resolver upstream payload"
grep -q 'RESOLVER_VERSION=' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the resolver version"
grep -q 'RESOLVER_ASSET=' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the resolver asset name"
grep -q 'GH_REPO=' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the release repository"
grep -q "download/%s/%s" "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the pinned release URL pattern"
grep -q "latest/download/%s" "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the latest release URL pattern"
grep -q 'nydus-resolver release asset' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the release asset fallback"
if grep -q 'registry_map' "$tmp/p2p-static.yaml"; then
  fail "p2p-static script must not write a registry map"
fi
template="$root/charts/p2p-static/files/nydus-resolver-config.yaml.tmpl"
[ -f "$template" ] || fail "p2p-static resolver config template is missing"
grep -q 'ca_bundle:' "$template" || fail "resolver config template is missing ca_bundle"
if grep -q 'registry_map' "$template"; then
  fail "resolver config template must not contain a registry map"
fi
if [ -d "$root/charts/nydus-resolver" ]; then
  fail "nydus-resolver must be delivered by p2p-static, not a runtime chart"
fi
grep -q 'dfdaemon-bootstrap-boot.service' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the host bootstrap boot unit"
if grep -q 'kind: DaemonSet' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not render a DaemonSet"
fi
if grep -q 'kind: PersistentVolumeClaim' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not render a PVC"
fi

echo "p2p-static render checks passed"
