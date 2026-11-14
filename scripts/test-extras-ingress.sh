#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
extras="$root/charts/dragonfly-extras"
operator="$root/charts/d7y-cattle-operator"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# nginx keeps client mTLS at the edge and uses the manager's plaintext h2c
# gRPC backend; REST keeps the HTTPS backend.
helm template e "$extras" \
  --set ingress.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=poligon-manager-public-tls >"$tmp/nginx.yaml"
grep -q 'name: e-manager-grpc' "$tmp/nginx.yaml" || fail "nginx gRPC Ingress missing"
grep -q 'name: e-manager-api' "$tmp/nginx.yaml" || fail "nginx REST Ingress missing"
grep -q 'nginx.ingress.kubernetes.io/backend-protocol: GRPC' "$tmp/nginx.yaml" || fail "nginx gRPC backend-protocol missing"
if grep -q 'backend-protocol: GRPCS' "$tmp/nginx.yaml"; then fail "nginx gRPC must not use GRPCS (manager is plaintext h2c)"; fi
grep -q 'nginx.ingress.kubernetes.io/auth-tls-verify-client: "on"' "$tmp/nginx.yaml" || fail "nginx gRPC auth-tls missing"
grep -q 'secretName: "poligon-manager-public-tls"' "$tmp/nginx.yaml" || fail "nginx TLS Secret missing"
grep -q 'nginx.ingress.kubernetes.io/backend-protocol: HTTPS' "$tmp/nginx.yaml" || fail "nginx REST backend-protocol missing"

# Manager-only Traefik canary: REST only, class traefik, TLS from the mTLS Secret,
# and a CRD ServersTransport. grpc.enabled=false must suppress the gRPC Ingress.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set ingress.grpc.enabled=false \
  --set ingress.rest.enabled=true \
  --set ingress.rest.host=manager-api.koldun.negash.ru \
  --set ingress.rest.service=poligon-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=dragonfly-p2p-mtls \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik.yaml"
if grep -q 'name: e-manager-grpc' "$tmp/traefik.yaml"; then fail "Traefik manager-only rendered a gRPC Ingress"; fi
if grep -q 'kind: IngressRouteTCP' "$tmp/traefik.yaml"; then fail "Traefik manager-only rendered a gRPC IngressRouteTCP"; fi
if grep -q 'kind: TLSOption' "$tmp/traefik.yaml"; then fail "Traefik manager-only rendered a gRPC TLSOption"; fi
grep -q 'kind: IngressRoute' "$tmp/traefik.yaml" || fail "Traefik REST IngressRoute missing"
if grep -q '^kind: Ingress$' "$tmp/traefik.yaml"; then fail "Traefik manager-only rendered a REST Ingress"; fi
grep -q 'ingressClassName: "traefik"' "$tmp/traefik.yaml" || fail "Traefik ingressClassName missing"
grep -q 'scheme: https' "$tmp/traefik.yaml" || fail "Traefik REST backend scheme missing"
grep -q 'serversTransport: "e-manager-rest"' "$tmp/traefik.yaml" || fail "Traefik ServersTransport reference missing"
grep -q 'secretName: "dragonfly-p2p-mtls"' "$tmp/traefik.yaml" || fail "Traefik TLS Secret missing"
grep -q 'kind: ServersTransport' "$tmp/traefik.yaml" || fail "ServersTransport missing"
grep -q 'name: e-manager-rest' "$tmp/traefik.yaml" || fail "ServersTransport name missing"
grep -q 'namespace: dragonfly-system' "$tmp/traefik.yaml" || fail "ServersTransport namespace missing"
grep -q 'insecureSkipVerify: true' "$tmp/traefik.yaml" || fail "ServersTransport insecureSkipVerify missing"
if grep -q 'nginx.ingress.kubernetes.io' "$tmp/traefik.yaml"; then fail "Traefik output contains nginx annotations"; fi

# The ServersTransport and its IngressRoute reference disappear when the toggle
# is off.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.grpc.enabled=false \
  --set ingress.rest.enabled=true \
  --set ingress.traefik.serversTransport.enabled=false \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-no-transport.yaml"
if grep -q 'kind: ServersTransport' "$tmp/traefik-no-transport.yaml"; then fail "ServersTransport rendered while disabled"; fi
if grep -q 'serversTransport:' "$tmp/traefik-no-transport.yaml"; then fail "ServersTransport reference rendered while disabled"; fi

# Traefik gRPC terminates TLS at the edge and proxies plaintext h2c to the
# manager. The nginx gRPC Ingress must not be rendered in Traefik mode.
cat >"$tmp/mtls-ca.yaml" <<'YAML'
ingress:
  mtlsCA: |
    -----BEGIN CERTIFICATE-----
    ZmFrZQ==
    -----END CERTIFICATE-----
YAML
helm template e "$extras" --namespace dragonfly-system \
  -f "$tmp/mtls-ca.yaml" \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set ingress.grpc.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.grpc.port=65003 \
  --set ingress.rest.enabled=true \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=poligon-manager-public-tls \
  --set ingress.mtlsCASecret=dragonfly-p2p-mtls-ca \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-grpc.yaml"
if grep -q 'kind: IngressRouteTCP' "$tmp/traefik-grpc.yaml"; then fail "Traefik gRPC must not render an IngressRouteTCP"; fi
if [ "$(grep -c '^kind: IngressRoute$' "$tmp/traefik-grpc.yaml")" -ne 2 ]; then
  fail "Traefik must render REST and gRPC IngressRoutes"
fi
grep -q 'name: e-manager-grpc' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC IngressRoute missing"
grep -q 'kind: IngressRoute' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC kind missing"
grep -q 'Host(`grpc.example.com`)' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC Host match missing"
grep -q 'scheme: h2c' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC h2c backend missing"
grep -q 'secretName: "poligon-manager-public-tls"' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC TLS Secret missing"
grep -A 2 'options:' "$tmp/traefik-grpc.yaml" | grep -q 'name: e-manager-grpc' || fail "Traefik gRPC TLSOption reference missing"
grep -B 2 'port: 65003' "$tmp/traefik-grpc.yaml" | grep -q 'name: "c1-manager"' || fail "Traefik gRPC service/port missing"
grep -q 'kind: TLSOption' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC TLSOption missing"
grep -q 'clientAuthType: RequireAndVerifyClientCert' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC TLSOption clientAuth missing"
grep -q 'secretNames:' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC TLSOption CA reference missing"
grep -q -- '- "dragonfly-p2p-mtls-ca"' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC TLSOption CA Secret missing"
grep -q 'minVersion: VersionTLS13' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC TLSOption minVersion missing"
if [ "$(grep -c '^kind: Secret$' "$tmp/traefik-grpc.yaml")" -ne 1 ]; then
  fail "Traefik gRPC must render exactly one CA Secret"
fi
grep -q 'name: "dragonfly-p2p-mtls-ca"' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC CA Secret missing"
ca_b64=$(grep 'tls.ca:' "$tmp/traefik-grpc.yaml" | awk '{print $2}' | tr -d '"')
if [ -z "$ca_b64" ]; then fail "Traefik gRPC CA Secret has no tls.ca"; fi
if [ "$(printf '%s' "$ca_b64" | base64 -d | head -n 1)" != "-----BEGIN CERTIFICATE-----" ]; then
  fail "Traefik gRPC tls.ca is not the fake PEM"
fi
if grep -q '^kind: Ingress$' "$tmp/traefik-grpc.yaml"; then fail "Traefik gRPC rendered a plain Ingress"; fi
if grep -q 'nginx.ingress.kubernetes.io' "$tmp/traefik-grpc.yaml"; then fail "Traefik gRPC output contains nginx annotations"; fi

# The operator chart must not own Ingress, VPA, or PVC resources.
helm template t "$operator" >"$tmp/operator.yaml"
for env_name in EXTRAS_INGRESS_CONTROLLER EXTRAS_INGRESS_CLASS_NAME EXTRAS_INGRESS_GRPC_ENABLED EXTRAS_INGRESS_REST_ENABLED EXTRAS_INGRESS_TLS_SECRET EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED; do
  grep -q "name: $env_name" "$tmp/operator.yaml" || fail "operator deployment missing $env_name"
done
if grep -Eq '^kind: (Ingress|VerticalPodAutoscaler|PersistentVolumeClaim)$' "$tmp/operator.yaml"; then
  fail "operator chart rendered Ingress, VPA, or PVC"
fi

echo "extras ingress render checks passed"
