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

# nginx keeps the existing gRPC mTLS annotations and REST HTTPS backend.
helm template e "$extras" \
  --set ingress.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager >"$tmp/nginx.yaml"
grep -q 'name: e-manager-grpc' "$tmp/nginx.yaml" || fail "nginx gRPC Ingress missing"
grep -q 'name: e-manager-api' "$tmp/nginx.yaml" || fail "nginx REST Ingress missing"
grep -q 'nginx.ingress.kubernetes.io/backend-protocol: GRPCS' "$tmp/nginx.yaml" || fail "nginx gRPC backend-protocol missing"
grep -q 'nginx.ingress.kubernetes.io/auth-tls-verify-client: "on"' "$tmp/nginx.yaml" || fail "nginx gRPC auth-tls missing"
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

# Traefik gRPC is exactly one IngressRouteTCP with HostSNI and TLS passthrough.
# The nginx gRPC Ingress must not be rendered in Traefik mode.
helm template e "$extras" --namespace dragonfly-system \
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
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-grpc.yaml"
if [ "$(grep -c '^kind: IngressRouteTCP$' "$tmp/traefik-grpc.yaml")" -ne 1 ]; then
  fail "Traefik gRPC must render exactly one IngressRouteTCP"
fi
grep -q 'name: e-manager-grpc' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC IngressRouteTCP missing"
grep -q 'kind: IngressRouteTCP' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC kind missing"
grep -q 'HostSNI' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC HostSNI match missing"
grep -q 'grpc.example.com' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC host missing"
grep -q 'passthrough: true' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC TLS passthrough missing"
grep -q -- '- websecure' "$tmp/traefik-grpc.yaml" || fail "Traefik gRPC entryPoint missing"
grep -B 2 'port: 65003' "$tmp/traefik-grpc.yaml" | grep -q 'name: "c1-manager"' || fail "Traefik gRPC service/port missing"
grep -q 'kind: IngressRoute' "$tmp/traefik-grpc.yaml" || fail "Traefik REST IngressRoute missing"
if grep -q '^kind: Ingress$' "$tmp/traefik-grpc.yaml"; then fail "Traefik gRPC rendered a plain Ingress"; fi
if grep -q 'nginx.ingress.kubernetes.io' "$tmp/traefik-grpc.yaml"; then fail "Traefik gRPC output contains nginx annotations"; fi

# The operator chart must not own Ingress, VPA, or PVC resources.
helm template t "$operator" >"$tmp/operator.yaml"
for env_name in MANAGER_PUBLIC_TLS_SECRET EXTRAS_INGRESS_CONTROLLER EXTRAS_INGRESS_CLASS_NAME EXTRAS_INGRESS_GRPC_ENABLED EXTRAS_INGRESS_REST_ENABLED EXTRAS_INGRESS_TLS_SECRET EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED; do
  grep -q "name: $env_name" "$tmp/operator.yaml" || fail "operator deployment missing $env_name"
done
if grep -Eq '^kind: (Ingress|VerticalPodAutoscaler|PersistentVolumeClaim)$' "$tmp/operator.yaml"; then
  fail "operator chart rendered Ingress, VPA, or PVC"
fi

echo "extras ingress render checks passed"
