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

# Print one YAML document by kind and metadata.name.
doc() {
  awk -v kind="$1" -v name="name: $2" 'BEGIN{RS="---"} $0 ~ "kind: "kind && $0 ~ name {print}' "$3"
}

# nginx terminates client mTLS at the edge, serves the P2P certificate for the
# gRPC host, and uses a GRPCS backend with the P2P client certificate. REST
# keeps the public certificate and the HTTPS backend.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.grpc.tlsSecret=dragonfly-p2p-mtls \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=poligon-manager-public-tls >"$tmp/nginx.yaml"
grpc_ingress=$(doc Ingress e-manager-grpc "$tmp/nginx.yaml")
rest_ingress=$(doc Ingress e-manager-api "$tmp/nginx.yaml")
[ -n "$grpc_ingress" ] || fail "nginx gRPC Ingress missing"
[ -n "$rest_ingress" ] || fail "nginx REST Ingress missing"
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/backend-protocol: GRPCS' || fail "nginx gRPC backend-protocol must be GRPCS"
if echo "$grpc_ingress" | grep -q 'backend-protocol: GRPC$'; then fail "nginx gRPC must not use plaintext GRPC"; fi
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/proxy-ssl-secret: "dragonfly-system/dragonfly-p2p-mtls"' || fail "nginx gRPC proxy-ssl-secret missing"
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/proxy-ssl-verify: "on"' || fail "nginx gRPC proxy-ssl-verify missing"
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/proxy-ssl-name: "grpc.example.com"' || fail "nginx gRPC proxy-ssl-name missing"
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/auth-tls-verify-client: "on"' || fail "nginx gRPC auth-tls missing"
echo "$grpc_ingress" | grep -q 'secretName: "dragonfly-p2p-mtls"' || fail "nginx gRPC edge must serve the P2P TLS Secret"
echo "$rest_ingress" | grep -q 'secretName: "poligon-manager-public-tls"' || fail "nginx REST TLS Secret missing"
echo "$rest_ingress" | grep -q 'nginx.ingress.kubernetes.io/backend-protocol: HTTPS' || fail "nginx REST backend-protocol missing"

# Manager-only Traefik canary: REST only, class traefik, TLS from the mTLS Secret,
# and a CRD ServersTransport. grpc.enabled=false must suppress the gRPC Ingress.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set ingress.grpc.enabled=false \
  --set ingress.rest.enabled=true \
  --set ingress.rest.host=manager-api.example.com \
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

# The REST ServersTransport and its IngressRoute reference disappear when the
# toggle is off.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.grpc.enabled=false \
  --set ingress.rest.enabled=true \
  --set ingress.traefik.serversTransport.enabled=false \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-no-transport.yaml"
if grep -q 'kind: ServersTransport' "$tmp/traefik-no-transport.yaml"; then fail "ServersTransport rendered while disabled"; fi
if grep -q 'serversTransport:' "$tmp/traefik-no-transport.yaml"; then fail "ServersTransport reference rendered while disabled"; fi

# Traefik terminates edge mTLS with the P2P certificate and reaches the manager
# over HTTPS with the gRPC ServersTransport (P2P CA + client certificate).
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
  --set ingress.grpc.tlsSecret=dragonfly-p2p-mtls \
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
grpc_route=$(doc IngressRoute e-manager-grpc "$tmp/traefik-grpc.yaml")
rest_route=$(doc IngressRoute e-manager-api "$tmp/traefik-grpc.yaml")
grpc_transport=$(doc ServersTransport e-manager-grpc "$tmp/traefik-grpc.yaml")
rest_transport=$(doc ServersTransport e-manager-rest "$tmp/traefik-grpc.yaml")
[ -n "$grpc_route" ] || fail "Traefik gRPC IngressRoute missing"
[ -n "$rest_route" ] || fail "Traefik REST IngressRoute missing"
[ -n "$grpc_transport" ] || fail "Traefik gRPC ServersTransport missing"
[ -n "$rest_transport" ] || fail "Traefik REST ServersTransport missing"
echo "$grpc_route" | grep -q 'Host(`grpc.example.com`)' || fail "Traefik gRPC Host match missing"
echo "$grpc_route" | grep -q 'scheme: https' || fail "Traefik gRPC backend must use scheme https"
if echo "$grpc_route" | grep -q 'scheme: h2c'; then fail "Traefik gRPC backend must not use h2c"; fi
echo "$grpc_route" | grep -q 'serversTransport: "e-manager-grpc"' || fail "Traefik gRPC ServersTransport reference missing"
echo "$grpc_route" | grep -q 'secretName: "dragonfly-p2p-mtls"' || fail "Traefik gRPC route must serve the P2P TLS Secret"
echo "$rest_route" | grep -q 'scheme: https' || fail "Traefik REST backend scheme missing"
echo "$rest_route" | grep -q 'serversTransport: "e-manager-rest"' || fail "Traefik REST ServersTransport reference missing"
echo "$rest_route" | grep -q 'secretName: "poligon-manager-public-tls"' || fail "Traefik REST route must keep the public TLS Secret"
echo "$grpc_transport" | grep -q 'serverName: "grpc.example.com"' || fail "gRPC ServersTransport serverName missing"
grep -A 2 'rootCAs:' <<<"$grpc_transport" | grep -q -- '- secret: "dragonfly-p2p-mtls"' || fail "gRPC ServersTransport rootCAs missing"
grep -A 2 'certificatesSecrets:' <<<"$grpc_transport" | grep -q -- '- "dragonfly-p2p-mtls"' || fail "gRPC ServersTransport client certificate missing"
if echo "$grpc_transport" | grep -q '^  insecureSkipVerify:'; then fail "gRPC ServersTransport must not set insecureSkipVerify"; fi
echo "$rest_transport" | grep -q 'insecureSkipVerify: true' || fail "REST ServersTransport must keep insecureSkipVerify"
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

# Empty grpc.tlsSecret falls back to the public Secret for the edge only; the
# gRPC ServersTransport is not rendered without P2P material.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set ingress.grpc.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.rest.enabled=true \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=poligon-manager-public-tls \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-fallback.yaml"
grpc_route=$(doc IngressRoute e-manager-grpc "$tmp/traefik-fallback.yaml")
rest_route=$(doc IngressRoute e-manager-api "$tmp/traefik-fallback.yaml")
echo "$grpc_route" | grep -q 'secretName: "poligon-manager-public-tls"' || fail "Traefik gRPC edge must fall back to the public TLS Secret"
echo "$rest_route" | grep -q 'secretName: "poligon-manager-public-tls"' || fail "Traefik REST fallback Secret missing"
if [ -n "$(doc ServersTransport e-manager-grpc "$tmp/traefik-fallback.yaml")" ]; then
  fail "gRPC ServersTransport rendered without a P2P secret"
fi

# A gRPC-specific Secret alone enables the gRPC TLS block and transport; REST
# keeps no TLS.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set ingress.grpc.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.rest.enabled=true \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=false \
  --set ingress.grpc.tlsSecret=dragonfly-p2p-mtls \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-grpc-only.yaml"
grpc_route=$(doc IngressRoute e-manager-grpc "$tmp/traefik-grpc-only.yaml")
rest_route=$(doc IngressRoute e-manager-api "$tmp/traefik-grpc-only.yaml")
echo "$grpc_route" | grep -q 'secretName: "dragonfly-p2p-mtls"' || fail "gRPC TLS Secret must render without public TLS"
[ -n "$(doc ServersTransport e-manager-grpc "$tmp/traefik-grpc-only.yaml")" ] || fail "gRPC ServersTransport must render with the P2P secret"
if echo "$rest_route" | grep -q 'secretName:'; then fail "REST TLS block rendered while public TLS is disabled"; fi

# Scheduler front: the extras chart owns only the Traefik TCP TLS-passthrough
# route. The scheduler terminates mTLS itself and no LE certificate is attached.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=false \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set scheduler.enabled=true \
  --set scheduler.publicIP=203.0.113.10 \
  --set scheduler.clusterID=c-m-example \
  --set scheduler.domain=example.com \
  --set scheduler.service=c1-control-scheduler \
  --set scheduler.port=8002 \
  --api-versions traefik.io/v1alpha1 >"$tmp/scheduler-tcp.yaml"
scheduler_route=$(doc IngressRouteTCP e-scheduler "$tmp/scheduler-tcp.yaml")
[ -n "$scheduler_route" ] || fail "scheduler IngressRouteTCP missing"
if grep -q 'name: c1-control-scheduler-client' "$tmp/scheduler-tcp.yaml"; then
  fail "extras must not render a scheduler client ClusterIP Service"
fi
echo "$scheduler_route" | grep -q 'HostSNI(`\*`)' || fail "scheduler HostSNI catch-all missing"
echo "$scheduler_route" | grep -q 'name: "c1-control-scheduler"' || fail "scheduler headless backend Service missing"
echo "$scheduler_route" | grep -q 'port: 8002' || fail "scheduler backend port missing"
echo "$scheduler_route" | grep -q 'passthrough: true' || fail "scheduler TLS passthrough missing"
echo "$scheduler_route" | grep -q 'external-dns.alpha.kubernetes.io/hostname: "d7y-scheduler.c-m-example.example.com"' || fail "scheduler external-dns hostname missing"
echo "$scheduler_route" | grep -q 'external-dns.alpha.kubernetes.io/target: "203.0.113.10"' || fail "scheduler external-dns target missing"
if echo "$scheduler_route" | grep -q 'secretName:'; then fail "scheduler passthrough must not reference a TLS Secret"; fi
if grep -q 'name: e-scheduler' "$tmp/scheduler-tcp.yaml" && grep -q 'kind: TLSOption' "$tmp/scheduler-tcp.yaml"; then
  fail "scheduler passthrough must not render a TLSOption"
fi
if grep -q 'name: e-scheduler' "$tmp/scheduler-tcp.yaml" && grep -q 'kind: ServersTransport' "$tmp/scheduler-tcp.yaml"; then
  fail "scheduler passthrough must not render a ServersTransport"
fi
if grep -q 'name: e-manager-grpc' "$tmp/scheduler-tcp.yaml"; then fail "scheduler-only render emitted the manager gRPC route"; fi

# An explicit HostSNI keeps working when operators share the entry point.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=false \
  --set ingress.controller=traefik \
  --set scheduler.enabled=true \
  --set scheduler.publicIP=203.0.113.10 \
  --set scheduler.clusterID=c1 \
  --set scheduler.domain=example.com \
  --set scheduler.hostSNI=203.0.113.10 \
  --set scheduler.service=c1-control-scheduler \
  --api-versions traefik.io/v1alpha1 >"$tmp/scheduler-tcp-ip.yaml"
grep -q 'HostSNI(`203.0.113.10`)' "$tmp/scheduler-tcp-ip.yaml" || fail "scheduler explicit HostSNI missing"

# The operator chart must not own Ingress, VPA, or PVC resources.
helm template t "$operator" >"$tmp/operator.yaml"
for env_name in EXTRAS_INGRESS_CONTROLLER EXTRAS_INGRESS_CLASS_NAME EXTRAS_INGRESS_GRPC_ENABLED EXTRAS_INGRESS_REST_ENABLED EXTRAS_INGRESS_TLS_SECRET EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED P2P_DOMAIN P2P_CLUSTER_ID; do
  grep -q "name: $env_name" "$tmp/operator.yaml" || fail "operator deployment missing $env_name"
done
if grep -Eq '^kind: (Ingress|VerticalPodAutoscaler|PersistentVolumeClaim)$' "$tmp/operator.yaml"; then
  fail "operator chart rendered Ingress, VPA, or PVC"
fi

echo "extras ingress render checks passed"
