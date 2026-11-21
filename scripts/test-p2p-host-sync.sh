#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/charts/p2p-host-sync"
install="$root/pkg/machinebootstrap/assets/p2p-install.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

bash -n "$chart/files/nydus-updater" || fail "nydus-updater has a syntax error"
bash -n "$chart/files/nydus-fs-select.sh" || fail "nydus-fs-select.sh has a syntax error"
cmp -s "$chart/files/nydus-fs-select.sh" "$root/pkg/machinebootstrap/assets/nydus-fs-select.sh" \
  || fail "host-sync nydus-fs-select.sh differs from the embedded asset"
{
  sed -n "/^write_file .*nydus-updater.*<<'UPDATER'$/,/^UPDATER$/p" "$install" | sed '1d;$d'
} > "$tmp/embedded-updater"
cmp -s "$chart/files/nydus-updater" "$tmp/embedded-updater" \
  || fail "host-sync nydus-updater differs from the embedded updater"

helm template p2p-host-sync "$chart" --namespace dragonfly-system \
  --set 'schedulerIPs[0]=203.0.113.10:8002' \
  --set staticURL=https://static.example.test \
  --set versions.client=v1.5.14 --set versions.nydus=v2.4.5 --set versions.snapshotter=v0.16.0 \
  > "$tmp/host-sync.yaml"
grep -q 'hostPID: true' "$tmp/host-sync.yaml" || fail "host-sync must use hostPID for nsenter"
grep -q 'nsenter -t 1 -m -u -i -n -p -- systemctl start nydus-updater.service' "$tmp/host-sync.yaml" \
  || fail "host-sync does not trigger nydus-updater via nsenter"
grep -q 'STATIC_URL=https://static.example.test' "$tmp/host-sync.yaml" || fail "versions.env misses STATIC_URL"
grep -q 'CLIENT_VERSION=v1.5.14' "$tmp/host-sync.yaml" || fail "versions.env misses client version"
grep -q 'NYDUS_VERSION=v2.4.5' "$tmp/host-sync.yaml" || fail "versions.env misses nydus version"
grep -q 'NYDUS_SNAPSHOTTER_VERSION=v0.16.0' "$tmp/host-sync.yaml" || fail "versions.env misses snapshotter version"
grep -q 'NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64=9bd24308ef51139310bf58927a97eec49567d0154606bc5774ad69d2ee492ff0' "$tmp/host-sync.yaml" || fail "versions.env misses the verified snapshotter binary checksum"
grep -q 'nydus-snapshotter' "$tmp/host-sync.yaml" || fail "updater does not carry snapshotter update"
if grep -q 'p2p.2' "$tmp/host-sync.yaml"; then
  fail "host-sync still references p2p.2"
fi

awk '
  /^  sync\.sh: \|$/ { capture=1; next }
  capture && /^  [A-Za-z0-9_.-]+: / { capture=0 }
  capture && /^---$/ { capture=0 }
  capture { sub(/^    /, ""); print }
' "$tmp/host-sync.yaml" > "$tmp/sync.sh"
bash -n "$tmp/sync.sh" || fail "rendered sync.sh has a syntax error"
grep -q 'versions.env' "$tmp/sync.sh" || fail "sync.sh does not write versions.env"
grep -q 'nydus-updater' "$tmp/sync.sh" || fail "sync.sh does not write nydus-updater"
grep -q 'host-sync-revision' "$tmp/sync.sh" || fail "sync.sh lacks the trigger marker"
grep -q 'systemctl start nydus-updater.service' "$tmp/sync.sh" || fail "sync.sh lacks the updater trigger"

echo "p2p-host-sync render checks passed"
