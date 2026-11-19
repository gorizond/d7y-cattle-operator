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
  --set proxy.domain=proxy.example.test >"$tmp/admission.yaml"

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
grep -q 'proxy.example.test' "$tmp/admission.yaml" || fail "proxy.domain fixture is missing"
if grep -q 'ADMISSION_REGISTRIES' "$tmp/admission.yaml"; then
  fail "admission-webhook must not render a registry allowlist"
fi
grep -q 'value: "true"' "$tmp/admission.yaml" || fail "networkOnly default is missing"
grep -q 'containerPort: 8443' "$tmp/admission.yaml" || fail "webhook port is missing"
grep -q 'readOnlyRootFilesystem: true' "$tmp/admission.yaml" || fail "webhook rootfs is not read-only"

# The generated serving certificate must be valid for the Service that the MWC
# calls (.Release.Name), while keeping the TLS Secret-name SANs.
release="c1-admission-webhook"
namespace="dragonfly-system"
awk '/^  tls.crt: /{print $2; exit}' "$tmp/admission.yaml" | base64 -d >"$tmp/tls.crt"
openssl x509 -in "$tmp/tls.crt" -noout -ext subjectAltName >"$tmp/sans.txt"
for san in \
  "$release" \
  "$release.$namespace" \
  "$release.$namespace.svc" \
  "$release.$namespace.svc.cluster.local" \
  "$release-tls" \
  "$release-tls.$namespace" \
  "$release-tls.$namespace.svc" \
  "$release-tls.$namespace.svc.cluster.local"; do
  grep -q "DNS:$san" "$tmp/sans.txt" || fail "TLS SAN $san is missing"
done

helm template "$release" "$chart" --namespace "$namespace" \
  --set proxy.domain=proxy.example.test --show-only templates/webhook.yaml >"$tmp/mwc.yaml"
grep -A4 'service:' "$tmp/mwc.yaml" | grep -q "name: $release" || \
  fail "MWC clientConfig.service.name does not match the Service"
helm template "$release" "$chart" --namespace "$namespace" \
  --show-only templates/service.yaml >"$tmp/service.yaml"
grep -q "name: $release$" "$tmp/service.yaml" || fail "Service name is not .Release.Name"
if grep -q "name: $release-tls" "$tmp/service.yaml"; then
  fail "admission-webhook must not render an alias TLS Service"
fi

# The release namespace is always excluded, even when it is not in values.
grep -q -- "- \"$namespace\"" "$tmp/mwc.yaml" || \
  fail "release namespace is not excluded from the pod webhook"
helm template "$release" "$chart" --namespace custom-ns \
  --set proxy.domain=proxy.example.test --show-only templates/webhook.yaml >"$tmp/mwc-custom.yaml"
grep -q -- '- "custom-ns"' "$tmp/mwc-custom.yaml" || \
  fail "release namespace exclusion is not dynamic"

# Default: only the system NotIn and the release-namespace NotIn.
count="$(grep -c 'operator: NotIn' "$tmp/mwc.yaml" || true)"
[ "$count" -eq 2 ] || fail "default namespaceSelector has $count NotIn expressions, want 2"

# Operator-driven annotation list: one extra NotIn containing both namespaces.
helm template "$release" "$chart" --namespace "$namespace" \
  --set proxy.domain=proxy.example.test \
  --set "namespaceSelector.excludedNamespaces={ns-a,ns-b}" \
  --show-only templates/webhook.yaml >"$tmp/mwc-exclude.yaml"
count="$(grep -c 'operator: NotIn' "$tmp/mwc-exclude.yaml" || true)"
[ "$count" -eq 3 ] || fail "custom namespaceSelector has $count NotIn expressions, want 3"
awk '
  /- key: kubernetes.io\/metadata.name/ {
    if (inValues && hasA && hasB) found = 1
    inValues = 0; hasA = 0; hasB = 0; next
  }
  /operator: NotIn/ { inValues = 1; next }
  inValues && /^            - / {
    if ($0 ~ /ns-a/) hasA = 1
    if ($0 ~ /ns-b/) hasB = 1
    next
  }
  END { if (inValues && hasA && hasB) found = 1; exit(found ? 0 : 1) }
' "$tmp/mwc-exclude.yaml" || fail "ns-a and ns-b are not in one custom NotIn expression"
grep -q 'kube-system' "$tmp/mwc-exclude.yaml" || fail "system exclusions are not preserved"
grep -q -- "- \"$namespace\"" "$tmp/mwc-exclude.yaml" || \
  fail "release namespace exclusion is not preserved with the custom list"

if grep -q 'kind: PersistentVolumeClaim' "$tmp/admission.yaml"; then
  fail "admission-webhook must not render a PVC"
fi

# No proxy domain is hardcoded: the default render is empty and opt-in.
helm template c1-admission-webhook "$chart" --namespace dragonfly-system \
  --show-only templates/deployment.yaml | grep -q 'value: ""' || \
  fail "proxy.domain is not empty by default"

# External TLS Secret + CA bundle override the generated Secret.
helm template c1-admission-webhook "$chart" --namespace dragonfly-system \
  --set proxy.domain=proxy.example.test --set tls.secretName=external-tls \
  --set tls.caBundle=CA-PEM >"$tmp/external.yaml"
grep -q 'secretName: external-tls' "$tmp/external.yaml" || fail "external TLS Secret is not used"
if grep -q 'kind: Secret' "$tmp/external.yaml"; then
  fail "external TLS Secret must not be generated"
fi

# Credential injection is opt-in and off in network-only mode.
helm template c1-admission-webhook "$chart" --namespace dragonfly-system \
  --set proxy.domain=proxy.example.test --set networkOnly=false \
  --set inject.imagePullSecret=proxy-pull --set inject.serviceAccountName=proxy-sa \
  --show-only templates/deployment.yaml >"$tmp/inject.yaml"
grep -q 'proxy-pull' "$tmp/inject.yaml" || fail "imagePullSecret value is missing"
grep -q 'proxy-sa' "$tmp/inject.yaml" || fail "serviceAccountName value is missing"

# The operator chart exposes its own effective image to the process so the
# admission webhook can run the same binary.
operator_chart="$root/charts/d7y-cattle-operator"
operator_version="$(awk '/^version:/{print $2; exit}' "$operator_chart/Chart.yaml")"
helm template d7y-cattle-operator "$operator_chart" \
  --show-only templates/deployment.yaml >"$tmp/operator.yaml"
grep -q 'name: OPERATOR_IMAGE_REPOSITORY' "$tmp/operator.yaml" || \
  fail "operator chart does not expose OPERATOR_IMAGE_REPOSITORY"
grep -q 'name: OPERATOR_IMAGE_TAG' "$tmp/operator.yaml" || \
  fail "operator chart does not expose OPERATOR_IMAGE_TAG"
grep -q 'value: "ghcr.io/gorizond/d7y-cattle-operator"' "$tmp/operator.yaml" || \
  fail "operator image repository env is not sourced from values"
grep -q "value: \"v$operator_version\"" "$tmp/operator.yaml" || \
  fail "operator image tag env does not default to v$operator_version"

# An explicit image.tag must win over the chart appVersion fallback.
admission_app_version="$(awk '/^appVersion:/{gsub(/"/,"",$2); print $2; exit}' "$chart/Chart.yaml")"
helm template c1-admission-webhook "$chart" --namespace dragonfly-system \
  --set proxy.domain=proxy.example.test --set image.tag=v9.9.9 \
  --show-only templates/deployment.yaml >"$tmp/admission-image.yaml"
grep -q 'image: "ghcr.io/gorizond/d7y-cattle-operator:v9.9.9"' "$tmp/admission-image.yaml" || \
  fail "explicit admission image.tag is not used"
if grep -q "v$admission_app_version" "$tmp/admission-image.yaml"; then
  fail "admission image still falls back to the chart appVersion"
fi

echo "admission-webhook render checks passed"
