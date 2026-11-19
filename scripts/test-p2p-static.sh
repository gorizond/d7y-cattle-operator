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
grep -q 'gorizond/nydus-snapshotter' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the nydus-snapshotter release repository"
grep -q 'v0.16.0-p2p.2' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the pinned nydus-snapshotter version"
grep -q 'nydus-snapshotter-linux-amd64' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the nydus-snapshotter release asset"
grep -q 'containerd-nydus-grpc' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the host snapshotter binary path"
grep -q 'containerd/certs.d' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the certs.d behavior"
grep -q 'NYDUS_MIRROR_URL' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the nydus mirror URL payload"
grep -q 'remote.mirrors_config' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the nydus mirrors config"
grep -q '/etc/nydus/mirrors' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the nydus mirrors directory"
grep -q 'nydus-overlayfs' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing nydus-overlayfs"
grep -q 'nydus-updater' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the host nydus updater"
grep -q 'use_local_image_pull' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the global nydus drop-in"
grep -q 'enable_referrer_detect = true' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing enable_referrer_detect"
grep -q 'enable_index_detect = true' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing enable_index_detect"
grep -q 'nydusd-config.fscache.json' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the fscache nydusd config"
grep -q 'fs_driver = "fscache"' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the fscache fs_driver"
grep -q 'threads_number = 8' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing fscache threads_number"
grep -q 'enable_kubeconfig_keychain = true' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the kubeconfig keychain"
grep -q 'NYDUS_FS_DRIVER="${NYDUS_FS_DRIVER:-fscache}"' "$tmp/p2p-static.yaml" || fail "p2p-static chart value nydus.fsDriver did not render"
if grep -q '__NYDUS_FS_DRIVER__' "$tmp/p2p-static.yaml" || grep -q '__NYDUS_CLUSTER_ID__' "$tmp/p2p-static.yaml"; then
  fail "p2p-static script still contains unrendered nydus placeholders"
fi
if grep -q 'k3s-gpu-erofs' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not hardcode a cluster name"
fi
if grep -q '"auth"' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not embed registry credentials (network-only or node keychain)"
fi
grep -q '"fs_prefetch": {' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing nydusd fs_prefetch"
grep -q '"threads_count": 8' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing nydusd prefetch threads"
grep -q '"type": "blobcache"' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing nydusd blobcache"
grep -q 'dfdaemon-bootstrap-switch.timer' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the host bootstrap switch timer"
grep -q 'dfdaemon-bootstrap-boot.service' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the host bootstrap boot unit"
if grep -q 'kind: DaemonSet' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not render a DaemonSet"
fi
if grep -q 'kind: PersistentVolumeClaim' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not render a PVC"
fi

helm template p2p-static "$root/charts/p2p-static" --namespace dragonfly-system \
  --set nydus.fsDriver=fusedev --set nydus.clusterId=example-node >"$tmp/p2p-static-values.yaml"
grep -q 'NYDUS_FS_DRIVER="${NYDUS_FS_DRIVER:-fusedev}"' "$tmp/p2p-static-values.yaml" || fail "p2p-static chart value nydus.fsDriver=fusedev did not render"
grep -q 'NYDUS_CLUSTER_ID_DEFAULT="example-node"' "$tmp/p2p-static-values.yaml" || fail "p2p-static chart value nydus.clusterId did not render"

echo "p2p-static render checks passed"
