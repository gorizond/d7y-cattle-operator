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

helm template p2p-static "$root/charts/p2p-static" --namespace dragonfly-system >"$tmp/p2p-static.yaml"
grep -q 'location /nydus/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the versioned nydus asset route"
grep -q 'location /bin/' "$tmp/p2p-static.yaml" || fail "p2p-static is missing the /bin asset route"
grep -q 'listen 80;' "$tmp/p2p-static.yaml" || fail "p2p-static nginx must listen on port 80"
grep -q 'listen \[::\]:80;' "$tmp/p2p-static.yaml" || fail "p2p-static nginx must listen on IPv6 localhost too"
if grep -q 'p2p-install.sh' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not serve p2p-install.sh"
fi

helm template p2p-static "$root/charts/p2p-static" --namespace dragonfly-system \
  --set manager.enabled=true --set manager.host=manager.example.com \
  --set manager.tlsSecret=manager-tls >"$tmp/p2p-static-manager.yaml"
grep -q 'kind: IngressRoute' "$tmp/p2p-static-manager.yaml" || fail "p2p-static manager IngressRoute missing"
grep -q 'PathPrefix(`/bin`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /bin path missing"
grep -q 'PathPrefix(`/nydus`)' "$tmp/p2p-static-manager.yaml" || fail "p2p-static /nydus path missing"
if grep -q 'p2p-install.sh' "$tmp/p2p-static-manager.yaml"; then
  fail "p2p-static manager route must not include p2p-install.sh"
fi

echo "p2p-static render checks passed"
