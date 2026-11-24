#!/usr/bin/env bash
# The API server dials the webhook Service by its DNS name. The leaf
# certificate must carry that exact name as CN and SAN, or admission fails
# closed with "tls: bad certificate" (failurePolicy=Fail then holds every
# matching bootstrap Secret). Chart 0.3.37 generated the leaf for the
# operator name instead of the "-webhook" Service.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/charts/d7y-cattle-operator"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "webhook-cert-sans: $*" >&2
  exit 1
}

check_render() {
  local release="$1" ns="$2" override="${3:-}"
  local args=(--namespace "$ns" --set webhook.enabled=true)
  [[ -n "$override" ]] && args+=(--set "fullnameOverride=$override")

  local out="$tmp/${release}-${ns}-${override:-default}.yaml"
  helm template "$release" "$chart" "${args[@]}" > "$out"

  local expected_svc="${override:-$release}-webhook"
  local svc_name mwc_svc cert_b64 ca_b64 ca_bundle cert_dns
  svc_name="$(awk '/^kind: Service$/ { s=1; next } s && /^  name:/ { print $2; exit }' "$out")"
  mwc_svc="$(awk '/^      service:$/ { s=1; next } s && /^        name:/ { print $2; exit }' "$out")"

  [[ "$svc_name" == "$expected_svc" ]] || fail "Service name '$svc_name' != '$expected_svc'"
  [[ "$mwc_svc" == "$svc_name" ]] || fail "MWC clientConfig service '$mwc_svc' != Service '$svc_name'"

  # The MWC must trust the CA that signed the mounted leaf.
  cert_b64="$(awk '/^kind: Secret$/ { s=1; next } s && /^  tls\.crt:/ { print $2; exit }' "$out")"
  ca_b64="$(awk '/^kind: Secret$/ { s=1; next } s && /^  ca\.crt:/ { print $2; exit }' "$out")"
  ca_bundle="$(awk '/^      caBundle:/ { print $2; exit }' "$out")"
  [[ -n "$cert_b64" && -n "$ca_b64" && -n "$ca_bundle" ]] || fail "missing certificate data"
  [[ "$ca_b64" == "$ca_bundle" ]] || fail "MWC caBundle does not match Secret ca.crt"

  openssl base64 -d -A <<<"$cert_b64" > "$tmp/cert.pem"
  openssl x509 -in "$tmp/cert.pem" -noout -text > "$tmp/cert.txt"

  cert_dns="${svc_name}.${ns}.svc"
  grep -Fq "DNS:${cert_dns}" "$tmp/cert.txt" || fail "SAN missing DNS:${cert_dns}"
  grep -Fq "DNS:${cert_dns}.cluster.local" "$tmp/cert.txt" || fail "SAN missing DNS:${cert_dns}.cluster.local"
  openssl x509 -in "$tmp/cert.pem" -noout -subject -nameopt RFC2253 \
    | grep -Fq "CN=${cert_dns}" || fail "CN is not ${cert_dns}"
}

helm lint "$chart" >/dev/null
helm lint "$chart" --set webhook.enabled=true >/dev/null

# Default (webhook disabled): no admission resources at all.
helm template d7y-cattle-operator "$chart" > "$tmp/disabled.yaml"
if grep -q '^kind: MutatingWebhookConfiguration$' "$tmp/disabled.yaml"; then
  fail "MutatingWebhookConfiguration rendered while webhook.enabled=false"
fi

check_render d7y-cattle-operator d7y-system
# fullnameOverride must move the Service, the MWC target, and the SAN together.
check_render d7y-cattle-operator d7y-system op-custom

echo "webhook cert SAN checks passed"
