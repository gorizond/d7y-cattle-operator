#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/charts/admission-webhook"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

helm lint "$chart" >/dev/null || fail "admission-webhook chart lint failed"

helm template c1-admission-webhook "$chart" --namespace dragonfly-system \
  --set proxy.domain=proxy.example.com >"$tmp/admission.yaml"

grep -q 'kind: Deployment' "$tmp/admission.yaml" || fail "admission Deployment is missing"
grep -q 'kind: Service' "$tmp/admission.yaml" || fail "admission Service is missing"
grep -q 'kind: MutatingWebhookConfiguration' "$tmp/admission.yaml" || fail "MutatingWebhookConfiguration is missing"
grep -q 'kind: Secret' "$tmp/admission.yaml" || fail "generated TLS Secret is missing"
grep -q 'tls.crt:' "$tmp/admission.yaml" || fail "TLS certificate is missing"
grep -q 'tls.key:' "$tmp/admission.yaml" || fail "TLS key is missing"
grep -q 'ca.crt:' "$tmp/admission.yaml" || fail "CA certificate is missing"
grep -q 'caBundle: ' "$tmp/admission.yaml" || fail "caBundle is missing"
grep -q 'operations: \["CREATE"\]' "$tmp/admission.yaml" || fail "webhook operation is not CREATE"
grep -q 'resources: \["pods"\]' "$tmp/admission.yaml" || fail "webhook resource is not pods"
grep -q 'failurePolicy: Fail' "$tmp/admission.yaml" || fail "failurePolicy is not Fail"
grep -q 'timeoutSeconds: 5' "$tmp/admission.yaml" || fail "timeoutSeconds is not 5"
grep -q 'kubernetes.io/metadata.name' "$tmp/admission.yaml" || fail "namespaceSelector is missing"
grep -q 'kube-system' "$tmp/admission.yaml" || fail "kube-system exclusion is missing"
grep -q 'fleet-system' "$tmp/admission.yaml" || fail "fleet-system exclusion is missing"
grep -q 'command:' "$tmp/admission.yaml" || fail "webhook command is missing"
grep -q '/admission-webhook' "$tmp/admission.yaml" || fail "webhook command does not use /admission-webhook"
grep -q 'proxy.example.com' "$tmp/admission.yaml" || fail "proxy.domain fixture is missing"
grep -q 'value: "true"' "$tmp/admission.yaml" || fail "networkOnly default is missing"
grep -q 'containerPort: 8443' "$tmp/admission.yaml" || fail "webhook port is missing"
grep -q 'readOnlyRootFilesystem: true' "$tmp/admission.yaml" || fail "webhook rootfs is not read-only"

if grep -q 'kind: PersistentVolumeClaim' "$tmp/admission.yaml"; then
  fail "admission-webhook must not render a PVC"
fi

# No proxy domain is hardcoded: the default render is empty and opt-in.
helm template c1-admission-webhook "$chart" --namespace dragonfly-system \
  --show-only templates/deployment.yaml | grep -q 'value: ""' || \
  fail "proxy.domain is not empty by default"

# External TLS Secret + CA bundle override the generated Secret.
helm template c1-admission-webhook "$chart" --namespace dragonfly-system \
  --set proxy.domain=proxy.example.com --set tls.secretName=external-tls \
  --set tls.caBundle=CA-PEM >"$tmp/external.yaml"
grep -q 'secretName: external-tls' "$tmp/external.yaml" || fail "external TLS Secret is not used"
if grep -q 'kind: Secret' "$tmp/external.yaml"; then
  fail "external TLS Secret must not be generated"
fi

# Credential injection is opt-in and off in network-only mode.
helm template c1-admission-webhook "$chart" --namespace dragonfly-system \
  --set proxy.domain=proxy.example.com --set networkOnly=false \
  --set inject.imagePullSecret=proxy-pull --set inject.serviceAccountName=proxy-sa \
  --show-only templates/deployment.yaml >"$tmp/inject.yaml"
grep -q 'proxy-pull' "$tmp/inject.yaml" || fail "imagePullSecret value is missing"
grep -q 'proxy-sa' "$tmp/inject.yaml" || fail "serviceAccountName value is missing"

echo "admission-webhook render checks passed"
