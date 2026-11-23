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

# Full Traefik CRD capability set the chart's auto-detection requires.
traefik_api=(
  --api-versions traefik.io/v1alpha1/IngressRoute
  --api-versions traefik.io/v1alpha1/IngressRouteTCP
  --api-versions traefik.io/v1alpha1/ServersTransport
)

# 1. Auto (default) without Traefik CRDs: nginx Ingresses only, no Traefik
# objects and no className (cluster default IngressClass).
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.grpc.tlsSecret=dragonfly-p2p-manager \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=demo-manager-public-tls >"$tmp/auto-nginx.yaml"
grep -q '^kind: Ingress$' "$tmp/auto-nginx.yaml" || fail "auto without Traefik CRDs must render nginx Ingresses"
if grep -q 'apiVersion: traefik.io' "$tmp/auto-nginx.yaml"; then fail "auto without Traefik CRDs rendered a Traefik object"; fi
if grep -q 'ingressClassName:' "$tmp/auto-nginx.yaml"; then fail "nginx output must not force an ingressClassName"; fi

# 2. Auto with the full Traefik CRD set: traefik objects, className defaults to
# "traefik" and the REST route references the ServersTransport.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.grpc.tlsSecret=dragonfly-p2p-manager \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=demo-manager-public-tls \
  "${traefik_api[@]}" >"$tmp/auto-traefik.yaml"
grep -q '^kind: IngressRouteTCP$' "$tmp/auto-traefik.yaml" || fail "auto with full Traefik CRDs must render IngressRouteTCP"
grep -q '^kind: IngressRoute$' "$tmp/auto-traefik.yaml" || fail "auto with full Traefik CRDs must render IngressRoute"
grep -q '^kind: ServersTransport$' "$tmp/auto-traefik.yaml" || fail "auto with full Traefik CRDs must render ServersTransport"
grep -q 'ingressClassName: "traefik"' "$tmp/auto-traefik.yaml" || fail "auto-traefik className must default to traefik"
if grep -Eq '^kind: Ingress$' "$tmp/auto-traefik.yaml"; then fail "auto-traefik rendered a nginx Ingress"; fi
if grep -q 'nginx.ingress.kubernetes.io' "$tmp/auto-traefik.yaml"; then fail "auto-traefik output contains nginx annotations"; fi

# 3. Explicit nginx with Traefik CRDs present: nginx path only.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=nginx \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=demo-manager-public-tls \
  "${traefik_api[@]}" >"$tmp/explicit-nginx.yaml"
[ -n "$(doc Ingress e-manager-grpc "$tmp/explicit-nginx.yaml")" ] || fail "explicit nginx gRPC Ingress missing"
[ -n "$(doc Ingress e-manager-api "$tmp/explicit-nginx.yaml")" ] || fail "explicit nginx REST Ingress missing"
if grep -q 'apiVersion: traefik.io' "$tmp/explicit-nginx.yaml"; then fail "explicit nginx rendered a Traefik object"; fi

# 4. Explicit traefik without Traefik CRDs: no unguarded/broken render. Every
# Traefik object requires its own kind, so the render is empty (and never
# silently falls back to nginx).
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager >"$tmp/explicit-traefik-missing.yaml"
if grep -Eq '^kind: (Ingress|IngressRoute|IngressRouteTCP|ServersTransport)$' "$tmp/explicit-traefik-missing.yaml"; then
  fail "explicit traefik without CRDs rendered an unguarded object"
fi

# 5. Explicit traefik with IngressRoute and IngressRouteTCP but without the
# ServersTransport kind: the REST route renders without the serversTransport
# reference, so the self-signed backend caveat applies instead of a broken link.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --api-versions traefik.io/v1alpha1/IngressRoute \
  --api-versions traefik.io/v1alpha1/IngressRouteTCP >"$tmp/explicit-traefik-no-transport-crd.yaml"
grep -q '^kind: IngressRoute$' "$tmp/explicit-traefik-no-transport-crd.yaml" || fail "REST IngressRoute must render without the ServersTransport CRD"
grep -q '^kind: IngressRouteTCP$' "$tmp/explicit-traefik-no-transport-crd.yaml" || fail "gRPC IngressRouteTCP must render"
if grep -q 'kind: ServersTransport' "$tmp/explicit-traefik-no-transport-crd.yaml"; then fail "ServersTransport rendered without its CRD"; fi
if grep -q 'serversTransport:' "$tmp/explicit-traefik-no-transport-crd.yaml"; then fail "REST route references a missing ServersTransport"; fi

# 6. Auto with IngressRoute/IngressRouteTCP but no ServersTransport kind falls
# back to nginx because REST+serversTransport is enabled by default.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.rest.host=api.example.com \
  --set ingress.rest.service=c1-manager \
  --api-versions traefik.io/v1alpha1/IngressRoute \
  --api-versions traefik.io/v1alpha1/IngressRouteTCP >"$tmp/auto-partial-traefik.yaml"
grep -Eq '^kind: Ingress$' "$tmp/auto-partial-traefik.yaml" || fail "auto without ServersTransport CRD must fall back to nginx"
if grep -q 'apiVersion: traefik.io' "$tmp/auto-partial-traefik.yaml"; then fail "auto fallback rendered a Traefik object"; fi

# 7. nginx terminates client mTLS at the edge, serves the P2P certificate for
# the gRPC host, and uses a GRPCS backend with the P2P client certificate.
# REST keeps the public certificate and the HTTPS backend.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=nginx \
  --set ingress.grpc.host=grpc.example.com \
  --set ingress.grpc.service=c1-manager \
  --set ingress.grpc.tlsSecret=dragonfly-p2p-manager \
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
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/proxy-ssl-secret: "dragonfly-system/dragonfly-p2p-manager"' || fail "nginx gRPC proxy-ssl-secret missing"
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/proxy-ssl-verify: "on"' || fail "nginx gRPC proxy-ssl-verify missing"
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/proxy-ssl-name: "grpc.example.com"' || fail "nginx gRPC proxy-ssl-name missing"
echo "$grpc_ingress" | grep -q 'nginx.ingress.kubernetes.io/auth-tls-verify-client: "on"' || fail "nginx gRPC auth-tls missing"
echo "$grpc_ingress" | grep -q 'secretName: "dragonfly-p2p-manager"' || fail "nginx gRPC edge must serve the P2P TLS Secret"
echo "$rest_ingress" | grep -q 'secretName: "demo-manager-public-tls"' || fail "nginx REST TLS Secret missing"
echo "$rest_ingress" | grep -q 'nginx.ingress.kubernetes.io/backend-protocol: HTTPS' || fail "nginx REST backend-protocol missing"

# 8. Traefik: manager gRPC is an exact-SNI TCP TLS passthrough to the manager
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
  "${traefik_api[@]}" >"$tmp/traefik-manager.yaml"
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

# 9. grpc.enabled=false suppresses the manager gRPC TCP route but keeps REST.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.grpc.enabled=false \
  --set ingress.rest.enabled=true \
  --set ingress.rest.host=manager-api.example.com \
  --set ingress.rest.service=demo-manager \
  --set ingress.tls.enabled=true \
  --set ingress.tls.secretName=demo-manager-public-tls \
  "${traefik_api[@]}" >"$tmp/traefik-rest-only.yaml"
if grep -q 'kind: IngressRouteTCP' "$tmp/traefik-rest-only.yaml"; then fail "grpc.enabled=false rendered a manager gRPC TCP route"; fi
if grep -q 'name: e-manager-grpc' "$tmp/traefik-rest-only.yaml"; then fail "grpc.enabled=false rendered a manager gRPC route"; fi
grep -q 'kind: IngressRoute' "$tmp/traefik-rest-only.yaml" || fail "REST IngressRoute missing with grpc disabled"

# 10. REST ServersTransport can still be disabled independently.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.grpc.enabled=false \
  --set ingress.rest.enabled=true \
  --set ingress.traefik.serversTransport.enabled=false \
  "${traefik_api[@]}" >"$tmp/traefik-no-transport.yaml"
if grep -q 'kind: ServersTransport' "$tmp/traefik-no-transport.yaml"; then fail "REST ServersTransport rendered while disabled"; fi
if grep -q 'serversTransport:' "$tmp/traefik-no-transport.yaml"; then fail "REST ServersTransport reference rendered while disabled"; fi

# 11. The scheduler is reached by direct node IP on 8002; the extras chart must
# not render a scheduler IngressRouteTCP anymore, even if the old values are
# still supplied.
helm template e "$extras" --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.controller=traefik \
  --set ingress.className=traefik \
  --set scheduler.enabled=true \
  --set scheduler.clusterID=c-m-example \
  --set scheduler.service=c1-control-scheduler \
  "${traefik_api[@]}" >"$tmp/no-scheduler-tcp.yaml"
if grep -q 'name: e-scheduler' "$tmp/no-scheduler-tcp.yaml"; then
  fail "extras chart still renders a scheduler IngressRouteTCP"
fi
if awk 'BEGIN{RS="---"} /kind: IngressRouteTCP/ && /component: scheduler/ {found=1} END{exit found?0:1}' "$tmp/no-scheduler-tcp.yaml"; then
  fail "extras chart still renders a scheduler TCP route"
fi

# The operator chart must not own Ingress, VPA, or PVC resources.
helm template t "$operator" >"$tmp/operator.yaml"
for env_name in EXTRAS_INGRESS_CONTROLLER EXTRAS_INGRESS_CLASS_NAME EXTRAS_INGRESS_GRPC_ENABLED EXTRAS_INGRESS_REST_ENABLED EXTRAS_INGRESS_TLS_SECRET EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED P2P_CLUSTER_ID; do
  grep -q "name: $env_name" "$tmp/operator.yaml" || fail "operator deployment missing $env_name"
done
if ! grep -A1 'name: EXTRAS_INGRESS_CONTROLLER' "$tmp/operator.yaml" | grep -q 'value: "auto"'; then
  fail "operator chart must default EXTRAS_INGRESS_CONTROLLER to auto"
fi
if grep -Eq '^kind: (Ingress|VerticalPodAutoscaler|PersistentVolumeClaim)$' "$tmp/operator.yaml"; then
  fail "operator chart rendered Ingress, VPA, or PVC"
fi

echo "extras ingress render checks passed"
