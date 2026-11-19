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
  --set ingress.tls.secretName=demo-manager-public-tls >"$tmp/nginx.yaml"
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
echo "$rest_ingress" | grep -q 'secretName: "demo-manager-public-tls"' || fail "nginx REST TLS Secret missing"
echo "$rest_ingress" | grep -q 'nginx.ingress.kubernetes.io/backend-protocol: HTTPS' || fail "nginx REST backend-protocol missing"

# Traefik: manager gRPC is an exact-SNI TCP TLS passthrough to the manager
# mTLS port. There must be no HTTP/2 router, ServersTransport, TLSOption, or
# CA-only Secret competing on the same SNI. REST remains an HTTPS IngressRoute
# with the public certificate.
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
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=demo-manager-public-tls \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-manager.yaml"
manager_tcp=$(doc IngressRouteTCP e-manager-grpc "$tmp/traefik-manager.yaml")
rest_route=$(doc IngressRoute e-manager-api "$tmp/traefik-manager.yaml")
rest_transport=$(doc ServersTransport e-manager-rest "$tmp/traefik-manager.yaml")
[ -n "$manager_tcp" ] || fail "manager gRPC IngressRouteTCP missing"
[ -n "$rest_route" ] || fail "Traefik REST IngressRoute missing"
[ -n "$rest_transport" ] || fail "Traefik REST ServersTransport missing"
if [ "$(grep -c '^kind: IngressRouteTCP$' "$tmp/traefik-manager.yaml")" -ne 1 ]; then
  fail "manager gRPC must render exactly one IngressRouteTCP"
fi
if [ "$(grep -c '^kind: IngressRoute$' "$tmp/traefik-manager.yaml")" -ne 1 ]; then
  fail "manager gRPC must not render an HTTP IngressRoute"
fi
if grep -q 'kind: TLSOption' "$tmp/traefik-manager.yaml"; then fail "manager gRPC must not render a TLSOption"; fi
if [ "$(grep -c '^kind: ServersTransport$' "$tmp/traefik-manager.yaml")" -ne 1 ]; then
  fail "manager gRPC must not render a gRPC ServersTransport"
fi
if grep -q 'kind: Secret' "$tmp/traefik-manager.yaml"; then fail "manager gRPC must not render a CA-only Secret"; fi
echo "$manager_tcp" | grep -Fq 'HostSNI(`grpc.example.com`)' || fail "manager gRPC exact HostSNI missing"
echo "$manager_tcp" | grep -q 'HostSNI(`\*`)' && fail "manager gRPC must use exact SNI, not catch-all"
echo "$manager_tcp" | grep -q 'name: "c1-manager"' || fail "manager gRPC backend Service missing"
echo "$manager_tcp" | grep -q 'port: 65003' || fail "manager gRPC backend port must be 65003"
echo "$manager_tcp" | grep -q 'passthrough: true' || fail "manager gRPC TLS passthrough missing"
if echo "$manager_tcp" | grep -q 'secretName:'; then fail "manager gRPC passthrough must not reference a TLS Secret"; fi
if grep -Fq 'Host(`grpc.example.com`)' "$tmp/traefik-manager.yaml"; then fail "HTTP router competes with manager gRPC TCP SNI"; fi
if grep -Fq 'demo-manager.dragonfly-system.svc' "$tmp/traefik-manager.yaml" || grep -Fq 'manager.dragonfly-system.svc' "$tmp/traefik-manager.yaml"; then
  fail "manager gRPC render contains an internal manager DNS address"
fi
echo "$rest_route" | grep -q 'scheme: https' || fail "Traefik REST backend scheme missing"
echo "$rest_route" | grep -q 'serversTransport: "e-manager-rest"' || fail "Traefik REST ServersTransport reference missing"
echo "$rest_route" | grep -q 'secretName: "demo-manager-public-tls"' || fail "Traefik REST route must keep the public TLS Secret"
echo "$rest_transport" | grep -q 'insecureSkipVerify: true' || fail "REST ServersTransport must keep insecureSkipVerify"
if grep -q 'nginx.ingress.kubernetes.io' "$tmp/traefik-manager.yaml"; then fail "Traefik output contains nginx annotations"; fi

# grpc.enabled=false suppresses the manager gRPC TCP route but keeps REST.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.grpc.enabled=false \
  --set ingress.rest.enabled=true \
  --set ingress.rest.host=manager-api.example.com \
  --set ingress.rest.service=demo-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=demo-manager-public-tls \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-rest-only.yaml"
if grep -q 'kind: IngressRouteTCP' "$tmp/traefik-rest-only.yaml"; then fail "grpc.enabled=false rendered a manager gRPC TCP route"; fi
if grep -q 'name: e-manager-grpc' "$tmp/traefik-rest-only.yaml"; then fail "grpc.enabled=false rendered a manager gRPC route"; fi
grep -q 'kind: IngressRoute' "$tmp/traefik-rest-only.yaml" || fail "REST IngressRoute missing with grpc disabled"

# REST ServersTransport can still be disabled independently.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.grpc.enabled=false \
  --set ingress.rest.enabled=true \
  --set ingress.traefik.serversTransport.enabled=false \
  --api-versions traefik.io/v1alpha1 >"$tmp/traefik-no-transport.yaml"
if grep -q 'kind: ServersTransport' "$tmp/traefik-no-transport.yaml"; then fail "REST ServersTransport rendered while disabled"; fi
if grep -q 'serversTransport:' "$tmp/traefik-no-transport.yaml"; then fail "REST ServersTransport reference rendered while disabled"; fi

# Scheduler front: the extras chart owns only the Traefik TCP TLS-passthrough
# route. The scheduler terminates mTLS itself and no LE certificate is attached.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=false \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set scheduler.enabled=true \
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
if echo "$scheduler_route" | grep -q 'external-dns.alpha.kubernetes.io/target:'; then
  fail "scheduler external-dns target must be derived, not pinned"
fi

# Fleet renders unquoted numeric cluster IDs as numbers, not strings. The alias
# must format them without fmt's %!s(...) marker.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=false \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set scheduler.enabled=true \
  --set scheduler.clusterID=2 \
  --set scheduler.domain=example.com \
  --set scheduler.service=c1-control-scheduler \
  --set scheduler.port=8002 \
  --api-versions traefik.io/v1alpha1 \
  --show-only templates/ingressroute-scheduler-tcp.yaml >"$tmp/scheduler-numeric.yaml"
grep -q 'external-dns.alpha.kubernetes.io/hostname: "d7y-scheduler.2.example.com"' "$tmp/scheduler-numeric.yaml" || fail "numeric scheduler clusterID must render the external-dns hostname"
if grep -q 'external-dns.alpha.kubernetes.io/target:' "$tmp/scheduler-numeric.yaml"; then
  fail "numeric scheduler render must not pin an external-dns target"
fi
if grep -q '%!' "$tmp/scheduler-numeric.yaml"; then fail "scheduler external-dns alias contains a fmt error marker"; fi
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
