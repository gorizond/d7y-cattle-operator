#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/charts/p2p-scheduler-cert"
values="$root/scripts/testdata/p2p-scheduler-cert-values.yaml"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "p2p-scheduler-cert: $*" >&2
  exit 1
}

helm lint "$chart" --values "$values" >/dev/null || fail "helm lint failed"
helm template c1-p2p-scheduler-cert "$chart" --namespace dragonfly-system --values "$values" > "$tmp/render.yaml"

grep -q '^kind: Secret$' "$tmp/render.yaml" || fail "Secret missing"
grep -q '^  name: dragonfly-p2p-scheduler$' "$tmp/render.yaml" || fail "Secret name changed"
grep -q '^  namespace: dragonfly-system$' "$tmp/render.yaml" || fail "Secret namespace changed"
grep -q '^type: kubernetes.io/tls$' "$tmp/render.yaml" || fail "Secret type changed"
for key in tls.crt tls.key ca.crt; do
  grep -q "^  ${key//./\\.}:" "$tmp/render.yaml" || fail "Secret key $key missing"
done

# The chart is intentionally minimal: only the Secret may render.
if grep -Eq '^kind: (Deployment|DaemonSet|Service|ConfigMap|Ingress|HelmOp|Bundle)$' "$tmp/render.yaml"; then
  fail "chart rendered a resource other than the Secret"
fi

if helm template c1-p2p-scheduler-cert "$chart" --namespace dragonfly-system >"$tmp/missing.yaml" 2>&1; then
  fail "chart rendered without required TLS values"
fi
grep -q 'tls.crt is required' "$tmp/missing.yaml" || fail "missing-value error for tls.crt not reported"

echo "p2p-scheduler-cert render checks passed"
