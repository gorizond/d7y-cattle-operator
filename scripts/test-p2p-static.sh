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
grep -q 'fallback_direct' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the fail-open fallback"
grep -q 'SCHEDULER_HOST' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the scheduler host payload"
if grep -q 'enable_oci_tar' "$tmp/p2p-static.yaml"; then
  fail "p2p-static must not assume enable_oci_tar"
fi
grep -q 'Direct-registry fallback' "$tmp/p2p-static.yaml" || fail "p2p-static script is missing the direct-registry template"

echo "p2p-static render checks passed"
