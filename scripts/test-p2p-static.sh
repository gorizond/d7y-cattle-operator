#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$root/charts/p2p-static/files/p2p-install.sh"
selector="$root/charts/p2p-static/files/nydus-fs-select.sh"
self_test="$root/charts/p2p-static/files/test.sh"
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
grep -q 'nydus-fs-select' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the filesystem selector"
grep -q 'fs_driver = "fscache"' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the fscache fs_driver"
grep -q 'fs_driver = "fusedev"' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the fusedev fs_driver"
grep -q 'threads_number = 8' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing fscache threads_number"
grep -q 'enable_kubeconfig_keychain = true' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the kubeconfig keychain"
grep -q 'CONFIG_EROFS_FS_ONDEMAND' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the EROFS on-demand config check"
grep -q 'CONFIG_CACHEFILES_ONDEMAND' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the cachefiles on-demand config check"
grep -q 'modules-load.d/nydus.conf' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing modules-load.d"
grep -q 'CACHEFILES_DEVICE="${CACHEFILES_DEVICE:-/dev/cachefiles}"' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the /dev/cachefiles check"
grep -q 'cachefilesd.service' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the cachefilesd mask"
grep -q 'stop cachefilesd.service' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the cachefilesd stop"
grep -q 'mask cachefilesd.service' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the cachefilesd mask call"
grep -q 'ExecStartPre=/usr/local/bin/nydus-fs-select' "$tmp/p2p-static.yaml" || fail "p2p-static unit is missing the selector ExecStartPre"
if grep -q -- '--nydusd-config' "$tmp/p2p-static.yaml"; then
  fail "p2p-static unit must let the selector choose --nydusd-config via config.toml"
fi
grep -q 'fs_version=6' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the RAFS v6 prerequisite note"
if grep -q '__NYDUS_FS_DRIVER__' "$tmp/p2p-static.yaml" || grep -q '__NYDUS_CLUSTER_ID__' "$tmp/p2p-static.yaml"; then
  fail "p2p-static script still contains unrendered nydus placeholders"
fi
if grep -q 'NYDUS_FS_DRIVER' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not pin the filesystem driver"
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

if grep -q 'kind: IngressRoute' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not render a manager route by default"
fi

helm template p2p-static "$root/charts/p2p-static" --namespace dragonfly-system \
  --set manager.enabled=true --set manager.host=manager.example.com \
  --set manager.tlsSecret=manager-tls >"$tmp/p2p-static-manager.yaml"
grep -q 'kind: IngressRoute' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager IngressRoute missing"
grep -q 'Host(`manager.example.com`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager host match missing"
grep -q 'PathPrefix(`/p2p-install.sh`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static install path missing"
grep -q 'PathPrefix(`/bin`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /bin path missing"
grep -q 'PathPrefix(`/nydus`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /nydus path missing"
grep -q 'kind: Rule' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager route kind missing"
grep -q 'name: "p2p-static"' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager service missing"
grep -q 'secretName: "manager-tls"' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager TLS Secret missing"

helm template p2p-static "$root/charts/p2p-static" --namespace dragonfly-system \
  --set nydus.clusterId=example-node >"$tmp/p2p-static-values.yaml"
grep -q 'NYDUS_CLUSTER_ID_DEFAULT="example-node"' "$tmp/p2p-static-values.yaml" || fail "p2p-static chart value nydus.clusterId did not render"
grep -q 'nydus-fs-select' "$tmp/p2p-static-values.yaml" || fail "p2p-static selector did not render"

echo "p2p-static render checks passed"
