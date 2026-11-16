# d7y-cattle-operator

Kubernetes operator that runs [Dragonfly](https://d7y.io) P2P on Rancher-managed
RKE2 clusters.

**Status**: MR-12 (manager gRPC via public Traefik TCP TLS passthrough; DNS-
resolved public scheduler front; no node/ClusterIP SANs; fail-open bootstrap).
The manager gRPC edge is a TCP passthrough on `manager-grpc:443` with exact
`HostSNI`; scheduler clients and bootstrap nodes reach it externally, and the
manager terminates mTLS end-to-end. The Cluster controller, role resolution,
KeyDB, manager, seed role, manager registration, `dragonfly-extras`, the
fail-open `p2p-static` bootstrap, and the admission webhook are implemented.
Live envtest/pilot verification remains deferred.

## What it does today

- Parses and validates the operator configuration from flags/environment variables.
- Ensures a single mTLS CA and leaf certificate Secret (`dragonfly-p2p-mtls` by
  default) in the operator namespace. The leaf carries the manager DNS names
  and the DNS-resolved public scheduler IP(s); node IPs and internal
  ClusterIPs are never added. The Secret is updated only when the SAN set
  changes, and a changed SAN set regenerates the CA and leaf because the old
  CA key is not persisted. The Secret is replicated into each Cluster namespace
  and delivered downstream through Fleet `downstreamResources`; Traefik
  forwards manager gRPC TLS without terminating it.
- Reconciles `provisioning.cattle.io/v1` Clusters selected by the
  `p2p-dragonfly-io: "true"` label.
- Deploys KeyDB and the Dragonfly manager through Fleet HelmOps, without
  PersistentVolumeClaims. Dragonfly releases use the gorizond fork chart
  `dragonfly 1.10.1-patch.2`, which renders `server.grpc.tls` under `grpc`,
  adds TLS flags to the manager/scheduler `grpc_health_probe` commands, and
  keeps the scheduler headless Service upstream-shaped.
- Deploys the seed role (scheduler/control, seedClient+client/data) when
  `p2p.dragonfly.io/seed: "true"` and a scheduler cluster ID is present. Nydus
  is host-only: `p2p-install.sh` installs and supervises it on the node; the
  operator creates no nydus DaemonSet or HelmOp.
- Manages the manager Ingresses, the public scheduler TCP front, and optional
  VPAs through the separate `<cluster>-extras` HelmOp and the
  `dragonfly-extras` chart. The operator has no Ingress, IngressRouteTCP, or
  VPA RBAC and creates neither kind directly.
- Uses a JWT-authenticated manager REST client (`pkg/managerapi`) and a
  thread-safe scheduler ID allocator (`pkg/registry`) for seed registration:
  auto mode writes the server-assigned ID to both `scheduler-cluster-id` and
  the durable `scheduler-cluster-id-auto` marker; user-owned IDs are never
  overwritten and mismatches are reported via the `d7y.io/IdMismatch` condition.
  Missing records are recreated and relinked on every reconcile.
- Publishes role/registration state as Cluster conditions
  (`d7y.io/RolesResolved`, `d7y.io/KeyDBReady`, `d7y.io/ControlReady`,
  `d7y.io/DataReady`, `d7y.io/Registered`) and updates them only when they
  change.
- Runs until it receives `SIGINT`/`SIGTERM`.

## Follow-ups

- Live envtest/pilot verification of the CREATE-only webhook, node bootstrap,
  and the status/condition transitions.
- The extras chart's VPA targetRef names must be verified against live workload
  names in the pilot.
- The KeyDB chart is external: `enapter/keydb` `0.48.0` from
  `https://enapter.github.io/charts/`. It is not vendored in this repo. The
  HelmOp requests two active multi-master nodes with persistence disabled. See
  [Release](#release).

## Install

Install the published chart from the GitHub Pages Helm repository:

```bash
helm repo add gorizond https://gorizond.github.io/d7y-cattle-operator
helm repo update
helm install d7y-cattle-operator gorizond/d7y-cattle-operator \
  --namespace dragonfly-system \
  --create-namespace \
  --set manager.grpcDomain=manager-grpc.example.com \
  --set manager.apiURL=https://manager-api.example.com \
  --set nginx.staticURL=https://static.example.com \
  --set managerApiCredentialsSecret=dragonfly-manager-api \
  --set scheduler.domain=example.com
```

`scheduler.domain` has no default and is required. There is no public or
internal scheduler IP value: the public scheduler IP(s) are resolved at
reconcile time from `d7y-scheduler.<cluster-id>.<domain>`, and both bootstrap
nodes and DaemonSets use that public host. DNS changes are SAN changes, so they
rotate the CA and leaf and roll the Dragonfly workloads.

For local development use `charts/d7y-cattle-operator` instead of the repository
chart. The release tag (`v<chart version>`) and the default image tag
(`v<chart version>`) are kept in sync by the release workflow.

The operator reads credentials only from Secret names. No secret values are passed
through flags or chart values. `nginx.staticURL` and
`managerApiCredentialsSecret` are required by the configuration contract but are
not used until the p2p-static and registration MRs.

### Configuration values

| Value | Environment variable | Description |
|---|---|---|
| `manager.grpcDomain` | `MANAGER_GRPC_DOMAIN` | Public manager gRPC hostname, without scheme |
| `manager.grpcPort` | `MANAGER_GRPC_PORT` | Public manager gRPC port used by schedulers; default `443` |
| `manager.apiURL` | `MANAGER_API_URL` | Public manager REST API URL (`https://`) |
| `scheduler.domain` | `P2P_DOMAIN` | Base domain for `d7y-scheduler.<fleet-cluster-id>.<domain>`; required |
| `scheduler.clusterID` | `P2P_CLUSTER_ID` | Optional fallback when provisioning Cluster `status.clusterName` is empty |
| `nginx.staticURL` | `NGINX_STATIC_URL` | Base URL of the static asset server (`http` or `https`) |
| `managerApiCredentialsSecret` | `MANAGER_API_CREDENTIALS_SECRET` | Secret with `username` and `password` keys for the manager API |
| `dragonfly.chart.repo` | `DRAGONFLY_CHART_REPO` | Helm repository URL of the Dragonfly chart; defaults to the gorizond fork |
| `extras.chart.repo` | `EXTRAS_CHART_REPO` | Helm repository URL of the dragonfly-extras chart |
| `extras.chart.name` | `EXTRAS_CHART_NAME` | dragonfly-extras chart name |
| `extras.chart.version` | `EXTRAS_CHART_VERSION` | dragonfly-extras chart version |
| `extras.ingress.controller` | `EXTRAS_INGRESS_CONTROLLER` | `nginx` (default) or `traefik` |
| `extras.ingress.className` | `EXTRAS_INGRESS_CLASS_NAME` | Ingress class; empty uses the cluster default |
| `extras.ingress.grpcEnabled` | `EXTRAS_INGRESS_GRPC_ENABLED` | Render the manager gRPC front when the manager role is active; default `true` |
| `extras.ingress.restEnabled` | `EXTRAS_INGRESS_REST_ENABLED` | Render the REST Ingress when the manager role is active; default `true` |
| `extras.ingress.tlsSecret` | `EXTRAS_INGRESS_TLS_SECRET` | Ingress TLS Secret; empty disables the TLS block |
| `extras.ingress.traefikServersTransportEnabled` | `EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED` | Render the REST Traefik `ServersTransport`; default `true` |

Defaults: `mtlsSecretName=dragonfly-p2p-mtls`, `dragonfly.chart.name=dragonfly`,
`dragonfly.chart.repo=https://gorizond.github.io/dragonflyoss-helm-charts`,
`dragonfly.chart.version=1.10.1-patch.2`, `extras.chart.name=dragonfly-extras`,
`extras.chart.version=0.1.8`, `extras.ingress.controller=nginx`,
`extras.ingress.grpcEnabled=true`, `extras.ingress.restEnabled=true`,
`manager.grpcPort=443`, `webhook.port=9443`,
`leaderElect=false`. The public scheduler IP(s) are resolved from DNS at
reconcile time; there is no scheduler IP value. `scheduler.domain` has no
default and is required. `scheduler.clusterID` is only a fallback for an
unpopulated `status.clusterName`.

## Roles and HelmOps

A Cluster labeled `p2p-dragonfly-io: "true"` gets one KeyDB release. The role
annotations add the Dragonfly releases:

| Roles | HelmOps |
|---|---|
| label only | `<cluster>-keydb`, `<cluster>-extras` (KeyDB VPA only) |
| `manager: "true"` | KeyDB, `<cluster>-manager`, `<cluster>-extras` (Ingress + VPAs) |
| `seed: "true"` + scheduler ID | KeyDB, `<cluster>-control`, `<cluster>-data`, `<cluster>-extras` (VPAs) |
| manager + seed | manager, control, data, extras |
| `seed: "true"` without scheduler ID | KeyDB + extras (KeyDB VPA); `SchedulerClusterIDMissing` event, no seed releases |

### Pilot data resource sizing

The fork dragonfly chart defaults to `8 CPU / 16Gi` requests for `seedClient`,
which cannot fit three replicas on the pilot nodes. `pkg/helmop/data.go` pins:

| Workload | Requests | Limits |
|---|---|---|
| `seedClient` | `1 CPU / 2Gi` | `4 CPU / 8Gi` |
| `client` | `250m / 512Mi` | `2 CPU / 4Gi` |

The four poligon nodes each have 16 CPU and 32,100 MiB allocatable. Before the
seed role they carried 8.7-10.3 CPU and 18.2-21.0 GiB of requests per node,
leaving 5.7-7.3 CPU and 10.4-13.1 GiB free. Three `seedClient` replicas plus
one `client` pod per node and two control schedulers add 4.25 CPU / 8.5 GiB
in total (1.5 CPU / 3 GiB on the busiest node), so all requests fit with at
least 4.2 CPU / 7.5 GiB still free per node. In the normal one-seed-per-node
spread, `seedClient` plus `client` plus a control scheduler also fit their
limits on a node (14 CPU / 28 GiB of 16 CPU / 31.3 GiB). The `seedClient`
hostNetwork ports 4100-4106 force the three replicas onto distinct nodes at
runtime.

The operator requires Rancher 2.15+ with Fleet 0.16+ (the `HelmOp` CRD).
The KeyDB chart (`enapter/keydb` `0.48.0`) is pinned in the operator; KeyDB is
external and is not published from this module. Nydus binaries are installed by
`p2p-install.sh`, not by an in-cluster chart. The KeyDB HelmOp runs two active masters (`nodes: 2`,
`multiMaster: "yes"`, `activeReplicas: "yes"`) with `persistentVolume.enabled:
false`, so it uses emptyDir and creates no PVC. The chart exposes the service as
`<cluster>-keydb:6379`.

### Status conditions

The operator writes conditions to `Cluster.status.conditions` and updates them
only when the value, reason, or message changes:

| Condition | True when |
|---|---|
| `d7y.io/RolesResolved` | The Cluster has the `p2p-dragonfly-io: "true"` label; the role annotations are parsed. |
| `d7y.io/KeyDBReady` | The `<cluster>-keydb` HelmOp was applied. |
| `d7y.io/ControlReady` | The seed scheduler HelmOp was applied (`seed: "true"`). |
| `d7y.io/DataReady` | The seedClient/client HelmOp was applied (`seed: "true"`). |
| `d7y.io/Registered` | Scheduler and seed-peer records were ensured and linked in the manager. |
| `d7y.io/RegistrationFailed` | Registration failed permanently; `d7y.io/Retrying` marks transient failures. |
| `d7y.io/IdMismatch` | The user-provided `scheduler-cluster-id` differs from the manager server ID; the annotation is preserved. |

A label-only Cluster reports `d7y.io/ControlReady=False` and
`d7y.io/DataReady=False` with reason `SeedDisabled`; a seed Cluster whose mTLS
Secret is missing reports `MTLSSecretUnavailable`. Conditions never contain
Secret values or certificate material.

### Dragonfly chart version

`dragonfly.chart.repo` defaults to
`https://gorizond.github.io/dragonflyoss-helm-charts` and
`dragonfly.chart.version` to the verified `1.10.1-patch.2` (appVersion
`2.5.4`). The fork carries these upstream-compatible patches:

- `server.grpc.tls` is rendered under `server.grpc`, so the manager actually
  serves gRPC over mTLS.
- The manager and scheduler startup/readiness/liveness probes pass
  `-tls -tls-ca-cert=... -tls-client-cert=... -tls-client-key=...` when the
  matching TLS configuration is set.
- The scheduler Service uses the real container gRPC port and honours
  `scheduler.config.server.tls`; `server.grpc.tls` is accepted as an alias and
  `server.rest.tls` is rejected as manager-only.
- The scheduler keeps its upstream-shaped headless peer Service. Only the
  public TCP front is rendered by `dragonfly-extras`; the fork chart adds no
  scheduler client Service.

Both values are passed to every Dragonfly HelmOp (`manager`, `control`,
`data`). A different version is allowed for experiments, but the operator
emits an `UnsupportedChartVersion` warning event on each reconcile because TLS
fields and topology control/data behavior may change between chart versions.
Pin the chart version in the operator release values; never hardcode it in Go
code. The fork is an external dependency of this repository: the chart is
pulled by Fleet at reconcile time and is not vendored here.

### Public scheduler front and P2P certificate

Bootstrap nodes need the scheduler before RKE2 (and therefore before Fleet
DaemonSets) exists. There is a single public front for both:

- A Traefik `IngressRouteTCP` named `<release>-scheduler` on the `websecure`
  entry point with `tls.passthrough: true`. Traefik never terminates TLS and no
  Let's Encrypt certificate or TLSOption is attached. The default
  ``HostSNI(`*`)`` matcher also covers clients that dial the public LB IP and
  send no SNI; set `scheduler.hostSNI` when the entry point is shared with
  another passthrough route. The backend is the fork chart's headless
  `<cluster>-control-scheduler` Service on port `8002`.
- Both bootstrap clients and DaemonSets use
  `d7y-scheduler.<cluster-id>.<domain>:443`. DaemonSets get that address from
  their local `dynconfig.yaml`; bootstrap clients get the public `advertiseIP`
  and `advertisePort=443` through the manager dynconfig and their local
  dynconfig. There is no internal scheduler ClusterIP Service.

The per-cluster DNS alias is
`d7y-scheduler.<fleet-cluster-id>.<scheduler.domain>`, where the cluster ID
comes from provisioning Cluster `status.clusterName` (with
`scheduler.clusterID`/`P2P_CLUSTER_ID` as an explicit fallback). The operator
resolves the alias at reconcile time with `net.LookupHost`; the resolved
address(es) become P2P leaf IP SANs and the first IPv4 address becomes the
scheduler `advertiseIP` and the extras external-dns target. The extras chart
adds `external-dns.alpha.kubernetes.io/hostname` and
`external-dns.alpha.kubernetes.io/target` to the `IngressRouteTCP` so an
external-dns Traefik source can publish it; a deployment zone wildcard can
also resolve it.
The alias is deliberately **not** a certificate identity: Dragonfly client
1.5.14 verifies the resolved IP, so the P2P leaf contains the resolved public
IP(s) plus the manager DNS names. It contains no ClusterIP and no node IPs.

If DNS resolution fails or returns no addresses, the operator emits a
`SchedulerDNSResolutionFailed` event and retries; it does not rewrite the
existing certificate to an empty IP set. Existing public IP SANs are
preserved when a new answer arrives, so a DNS/IP rotation keeps the old and new
addresses valid during propagation. `scheduler.config.server.advertiseIP` and
`advertisePort=443` use the public front; DaemonSets use the same public host
from their local dynconfig.

### Fail-open node bootstrap

The `p2p-static` bootstrap script (`p2p-install.sh`) is fail-open so the first
node of a new cluster can join RKE2 even when the scheduler is not reachable
yet:

- It preflights the manager gRPC endpoint and the public scheduler host
  (DNS/TCP, short timeout).
- If scheduler/DNS/manager preflight fails, or dfdaemon/nydus download/startup
  fails, it removes the nydus containerd drop-in (containerd 2.x) or writes an
  overlayfs-only direct-registry template (1.x) and exits success. It does not
  enable the `127.0.0.1:4001` nydus proxy or the bootstrap services in that
  path.
- Existing certificate/config files are preserved, but no P2P services are
  enabled. A later successful run (or an existing seed cluster) installs the
  full public-P2P configuration.
- Nydus is installed on the host from pinned `v0.16.0` assets
  (`containerd-nydus-grpc`, `nydusd`, `nydus-image`, `nydus-overlayfs`) and is
  the global CRI snapshotter. `enable_oci_tar` is not set (it is not a nydus
  field). A daily host timer runs `nydus-updater` to refresh the assets and
  restart the host unit only when something changed.
- Host `dfdaemon-bootstrap` is the cold-start P2P client. A boot helper starts
  it before RKE2 even if the switch disabled it at the previous shutdown; a
  30s host timer detects the node's in-cluster `client` pod via `crictl` and
  disables the host bootstrap while the pod is running. If the pod disappears,
  the timer re-enables the host bootstrap. The switch never touches nydus.

Certificate rotation is controlled by a pod template annotation:
`p2p.dragonfly.io/cert-revision` is the SHA-256 of `tls.crt`. It is added to
the manager Deployment, the control scheduler StatefulSet, and the data
seedClient/client workloads. A stable SAN set keeps the annotation stable, so
normal reconciles do not roll pods; a real certificate change rolls each
workload exactly once. `EnsureSecret` also removes the
`kubectl.kubernetes.io/last-applied-configuration` annotation because it can
contain a copy of `tls.key`.

### Port security

Dragonfly peers use `hostNetwork`, so Kubernetes NetworkPolicy does not cover
their node ports. Enforce the following with node firewall rules and cloud
security groups:

| Ports | Purpose | Exposure |
|---|---|---|
| `8002` | Scheduler gRPC backend | mTLS required; reachable only from the Traefik pod network, never exposed publicly |
| `4000` | dfdaemon upload | Plaintext by default; allow only cluster peers/scheduler. Set `p2p-data-tls` to require mTLS. |
| `4001/4002/4003/4004` | client proxy/metrics/health/stats | bind to `127.0.0.1`; do not expose |
| `4005/4006` | client storage TCP/QUIC | cluster network only |
| `4100` | seedClient upload | Plaintext by default; allow only cluster peers/scheduler. Set `p2p-data-tls` to require mTLS. |
| `4101/4102/4103/4104` | seedClient proxy/metrics/health/stats | bind to `127.0.0.1`; do not expose |
| `4105/4106` | seedClient storage TCP/QUIC | cluster network only |
| `65003` | manager gRPC backend | mTLS end-to-end; Traefik only forwards TCP; never plaintext |
| `443` | manager gRPC edge / scheduler TCP front | manager gRPC: TCP TLS passthrough, client mTLS terminates at manager; scheduler: TCP TLS passthrough |
| `8080` | manager REST | JWT required; public Ingress is allowed |

Do not use `proxy.basicAuth` for peer authentication: chart 1.10.1-patch.2
places it in the wrong configuration block. The operator keeps `proxy`, `metrics`, `health`,
and `stats` on `127.0.0.1` for both `client` and `seedClient`. Because both
DaemonSets may run on the same node, seedClient ports are shifted to
`4100–4106` so they do not collide with client `4000–4006`.

All releases target `dragonfly-system` and depend on each other through
`spec.dependsOn`: control depends on KeyDB, data depends on control, KeyDB, and
extras (which owns the public scheduler TCP front), manager depends on KeyDB,
and extras depends on manager when Ingress is enabled. The mTLS Secret is copied into the
Cluster namespace and delivered downstream through Fleet
`spec.downstreamResources`.

### Extras chart

The `dragonfly-extras` chart owns the manager gRPC/REST Ingresses and the
recommendation-only VPAs. The operator only creates the `<cluster>-extras`
HelmOp:

- `ingress.enabled` is true only for the manager role. The per-Ingress toggles
  are `ingress.grpc.enabled` and `ingress.rest.enabled`; both default to `true`.
- `ingress.controller=nginx` (default) terminates client mTLS at the edge,
  serves the P2P certificate for the gRPC host, and reaches the manager with
  `backend-protocol: GRPCS`, `proxy-ssl-secret` (the P2P Secret, which carries
  `ca.crt` for backend verification and `tls.crt`/`tls.key` as the client
  certificate), `proxy-ssl-verify: "on"` and `proxy-ssl-name` set to
  `ingress.grpc.host`. Client certificates are verified at the edge from
  `dragonfly-p2p-mtls`; REST keeps `backend-protocol: HTTPS` and the public
  certificate.
- `ingress.controller=traefik` renders a `traefik.io/v1alpha1` `IngressRoute`
  named `<release>-manager-api` for the REST backend (Traefik v3 ignores
  `traefik.ingress.kubernetes.io/service.serversscheme`). The route uses
  `scheme: https` and, when enabled, references the `ServersTransport` named
  `<release>-manager-rest` in the release namespace; that resource uses
  `insecureSkipVerify: true`. The REST Ingress is not rendered in Traefik mode,
  and no nginx annotations are added.
- In Traefik mode the manager gRPC endpoint is an `IngressRouteTCP` named
  `<release>-manager-grpc` (CRD `traefik.io/v1alpha1`, entry point `websecure`)
  with exact `HostSNI` matching `ingress.grpc.host`, backend
  `ingress.grpc.service:ingress.grpc.port` (65003), and
  `tls.passthrough: true`. Traefik does not terminate TLS or proxy HTTP/2;
  the manager verifies the client certificate end-to-end. There is no TLS
  Secret, `TLSOption`, `ServersTransport`, or CA-only Secret on this path, so
  no HTTP router competes with the exact SNI.
- `ingress.controller=nginx` keeps its existing GRPCS + client-mTLS Ingress
  path for compatibility. Traefik mode never renders an HTTP manager-gRPC
  IngressRoute or ServersTransport.
- REST keeps its own `<release>-manager-rest` `ServersTransport` with
  `insecureSkipVerify: true`; the REST IngressRoute uses `scheme: https` and
  the public certificate.
- When the seed role is active, the chart also renders `<release>-scheduler`
  (`traefik.io/v1alpha1` `IngressRouteTCP`, `websecure`) with
  `tls.passthrough: true`, `HostSNI` from `scheduler.hostSNI`
  (default `*`), and the fork chart's headless scheduler Service as backend.
  It adds external-dns hostname/target annotations built from
  `scheduler.clusterID` and `scheduler.domain`. There is no certificate, TLSOption, or
  ServersTransport on this path, and it is independent of the manager
  `ingress.enabled` toggle.
- `ingress.tls.enabled` adds the public REST TLS block using
  `ingress.tls.secretName`. Manager gRPC uses TCP passthrough and does not
  attach a certificate; `ingress.grpc.tlsSecret` is nginx-only.
- Operator chart settings for the canary:

  ```yaml
  extras:
    ingress:
      controller: traefik
      className: traefik
      grpcEnabled: false
      restEnabled: true
      tlsSecret: dragonfly-p2p-mtls
  ```
- The manager gRPC server serves TLS with client mTLS on `:65003` using the
  mounted `/etc/dragonfly/certs` material; the edge controller must therefore
  use an HTTPS/GRPCS backend with the P2P client certificate. The manager REST
  server keeps its internal `/etc/dragonfly/certs` certificate because REST
  stays HTTPS between Traefik/nginx and the manager. No cert-manager or Let's
  Encrypt resources are part of this repo; the public REST TLS Secret is
  supplied directly on the cluster.
- `vpa.enabled` is set from API discovery for `autoscaling.k8s.io/v1`; the
  chart additionally checks `Capabilities.APIVersions.Has`. Target names are
  `<cluster>-keydb`, `<cluster>-manager`, `<cluster>-control-scheduler`,
  and `<cluster>-data-seed-client`; each target is enabled only for its active
  role. Nydus is host-only and has no VPA target.
- The chart creates no PVC. The operator creates and replicates the mTLS
  Secret; the chart no longer renders a CA-only Secret. The operator RBAC
  remains limited to Clusters, HelmOps, Secrets, and Events.

## Cluster annotations

The operator only acts on Clusters labeled `p2p-dragonfly-io: "true"`.

| Annotation | Value | Effect |
|---|---|---|
| `p2p.dragonfly.io/manager` | `"true"` | Manager role: manager and its database, KeyDB as external Redis |
| `p2p.dragonfly.io/seed` | `"true"` | Seed role: scheduler, seed client/client (nydus runs on the host) |
| `p2p.dragonfly.io/scheduler-cluster-id` | integer | Scheduler cluster ID; required by the seed role until MR-3 auto-allocates it |
| `p2p.dragonfly.io/scheduler-replicas` | integer | Scheduler replicas (default `1`) |
| `p2p.dragonfly.io/seedclient-replicas` | integer | Seed client replicas (default `1`) |
| `p2p.dragonfly.io/monitoring` | label selector, `true`, or `enabled` | Enable metrics and ServiceMonitors for manager/scheduler/seedClient/client when the selector matches the Cluster labels; empty, invalid, or non-matching disables. Requires the Prometheus Operator ServiceMonitor CRD. |
| `p2p.dragonfly.io/p2p-data-tls` | `"true"` or `"enabled"` | Keep mTLS on the P2P data path (`seedClient`/`client` upload and scheduler `seedPeer`). Default/off uses plaintext P2P data; scheduler server, manager gRPC/API, and monitoring TLS are unaffected. |
| `p2p.dragonfly.io/node-config-key` | field name | Machine-config `spec` field patched by the webhook (default `userData`, e.g. `postBootScript`) |

When monitoring is enabled, a Dragonfly component with `enable=false` is
explicitly given `metrics.enable=false` and `metrics.serviceMonitor.enable=false`
so the fork chart does not render an empty metrics Service for it. When
monitoring is disabled, the operator leaves the chart metrics defaults
untouched.

P2P data TLS is disabled by default. The operator omits the upload server/client
certificate paths from both data workloads and the scheduler `seedPeer.tls`
block. Set `p2p.dragonfly.io/p2p-data-tls: "enabled"` (or `"true"`) to restore
mTLS on the data path. Scheduler `server.tls`, manager gRPC/API TLS, and
monitoring TLS are always retained. The bootstrap `p2p-install.sh` config keeps
scheduler control TLS and leaves upload plaintext, matching the cluster default.

## Manager API client and scheduler ID allocator

`pkg/managerapi` is a stdlib-only client for the Dragonfly v2.5.4 manager REST
API:

- JWT `POST /api/v1/users/signin` with `name`/`password` (the Secret key remains
  `username`); response `token` + `expire`; Bearer auth; one re-signin on 401.
- List/create SchedulerCluster and SeedPeerCluster, GET by ID, and idempotent
  `Ensure*` helpers.
- Pagination follows the `Link` header `rel=next`; list responses may be `null`
  (treated as empty) and `page`/`per_page`/exact `name` filters are supported.
- SeedPeerCluster → SchedulerCluster linking uses the explicit
  `PUT /api/v1/seed-peer-clusters/{seed_id}/scheduler-clusters/{scheduler_id}`
  endpoint; the create bodies never send `id`, `seed_peer_cluster_id`, or
  `scheduler_cluster_id`.
- 404 → `ErrNotFound`, 409 → `ErrConflict` (re-read), 401/403 → auth failure,
  422 → `ErrUnprocessable` (no retry); 5xx/timeouts surface as errors for the
  controller's future backoff.
- `block_list` is sent as an empty object (`{}`) because the contract expects an
  object and the nested shape is not confirmed.

`pkg/registry` allocates scheduler cluster IDs with a mutex and cache: ID 1 is
reserved, automatic allocation starts at 2, manager IDs are skipped, an
existing annotation is returned unchanged and never overwritten, and repeated
or concurrent calls for the same cluster return the cached ID.

The seed reconcile uses both packages: it ensures the SchedulerCluster and
SeedPeerCluster by exact name, links them with the explicit PUT endpoint, and
writes the server-assigned ID to `scheduler-cluster-id` plus the durable
`scheduler-cluster-id-auto` marker in auto mode. If the marker equals
`scheduler-cluster-id`, the ID is auto-owned and may be updated after manager DB
loss; if the marker is absent or different, the ID is user-owned and is never
overwritten (`d7y.io/IdMismatch` on server mismatch). This decision survives operator
restarts. Registration failures set `d7y.io/RegistrationFailed` or `d7y.io/Retrying` and block
control/data without blocking manager/KeyDB.

## Bootstrap static chart

`charts/p2p-static` serves `/p2p-install.sh` and the static asset volume through
Nginx. The bootstrap payload is a base64url **env-file** (no JSON, no `jq`, no
python on the node). The script installs the host-only nydus snapshotter
(`v0.16.0`, including `nydus-overlayfs`), writes the global nydus containerd
drop-in `config-v3.toml.d/90-nydus.toml`, starts it before RKE2, and installs a
daily host updater. There is no in-cluster nydus DaemonSet or HelmOp. See
`charts/p2p-static/README.md` for values, payload keys, drop-ins, and pilot
risks.

## Admission webhook

The operator serves `/mutate` on port 9443. The `MutatingWebhookConfiguration`
matches `rke-machine-config.cattle.io/v1` wildcard resources with
`operations: ["CREATE"]`, `failurePolicy: Ignore`, `sideEffects: None`, and
`admissionReviewVersions: ["v1"]`. Updates are never mutated.

A new machine config is patched only when its target Cluster (resolved by the
`<cluster>-<pool>` name prefix, or by the `p2p.dragonfly.io/cluster`
(`namespace/name` or name), `p2p.dragonfly.io/cluster-name`, and
`p2p.dragonfly.io/cluster-namespace` annotations) has the
`p2p-dragonfly-io: "true"` label and `p2p.dragonfly.io/seed: "true"` annotation,
or the config itself carries them. The payload is the base64url env-file from
`contracts/bootstrap-payload.md`; the node-config field is patched in plain YAML,
plain shell, or base64-encoded cloud-config form with the marker
`# d7y-cattle-operator:p2p-install`. Reapplying does not add a second call.

The patched spec field defaults to `userData`. Set the target Cluster annotation
`p2p.dragonfly.io/node-config-key` to choose a different single field name
(`postBootScript`, `userdata`, ...). The value must match
`^[A-Za-z][A-Za-z0-9]*$` and is interpreted as a field name under `spec`, not a
JSON path. Invalid values emit a `NodeConfigKeyInvalid` warning event and the
webhook does not patch. Cloud-config content keeps the existing `runcmd`
insertion; plain shell content, such as CloudblastConfig `postBootScript`, is
preserved and the bootstrap line is appended as a new shell line.

```yaml
apiVersion: provisioning.cattle.io/v1
kind: Cluster
metadata:
  name: c1
  namespace: fleet-default
  labels:
    p2p-dragonfly-io: "true"
  annotations:
    p2p.dragonfly.io/seed: "true"
    p2p.dragonfly.io/node-config-key: postBootScript
---
apiVersion: rke-machine-config.cattle.io/v1
kind: CloudblastConfig
metadata:
  name: c1-pool1
  namespace: fleet-default
spec:
  postBootScript: |
    #!/bin/bash
    echo "before bootstrap"
```

Serving TLS comes from Wrangler needacert: the webhook Service carries
`need-a-cert.cattle.io/secret-name`; needacert creates/rotates the TLS Secret
and injects the CA bundle into the MutatingWebhookConfiguration. The mounted
Secret is optional, and the server falls back to an in-memory self-signed
certificate only until needacert is available; `failurePolicy: Ignore` keeps
node creation unblocked.

## Known pilot risk: extras target names

The Ingress backends and VPA targetRefs assume the Dragonfly chart workload
names above. Verify them against the live release in the pilot, especially the
`data` StatefulSet (`<cluster>-data-seed-client`) and the `control` StatefulSet
(`<cluster>-control-scheduler`). The extras chart falls back to derived names
when explicit names are empty, but the operator always sets them.

The nginx gRPC Ingress reads `auth-tls-secret` and `proxy-ssl-secret` from
`dragonfly-system`; the operator must be installed there (the default
`namespace`) so the P2P Secret is present in the Ingress namespace.

## Known pilot risk: fork chart dependency

The operator defaults to the external fork
`https://gorizond.github.io/dragonflyoss-helm-charts` and pins
`dragonfly 1.10.1-patch.2`. The chart is not vendored into this repository; if
the fork pages or repository disappear, every Dragonfly HelmOp fails to
install. The fork is an interim dependency until the two patches are merged
upstream. When upstream publishes an equivalent release, re-run the fork chart
TLS checks against it and switch `dragonfly.chart.repo/version` back to the
upstream repository in a separate change.

Manager gRPC uses the Traefik `IngressRouteTCP` and `tls.passthrough` fields,
which are long-standing CRD fields. The REST `ServersTransport` continues to
use the verified `serverstransports.traefik.io` schema; a Traefik downgrade
that predates those fields would affect REST only.

## Known pilot risk: host-only nydus

Nydus runs only on the host, installed by `p2p-install.sh`; the operator no
longer creates a nydus chart, DaemonSet, HelmOp, RuntimeClass, or VPA. The
script installs pinned `v0.16.0` binaries (including `nydus-overlayfs`), writes
`/etc/nydus/config.toml` (multiple/fusedev, `[experimental]`
`enable_referrer_detect`/`enable_index_detect`, no tarfs), the registry-backend
`nydusd-config.json`, and the global containerd drop-in. It starts the host unit
before RKE2 and runs a daily updater for the binaries. Do not add
`enable_oci_tar` (it is not a nydus field). Pilot must verify the drop-in merge
with `containerd config dump`, the v0.16.0 unit flags, and the host restart
procedure. Tarfs is intentionally disabled; enabling it requires kernel >= 6.4
and a matching nydusd. Ordinary-OCI Zran is not claimed.

## MR roadmap

| MR | Scope | Status |
|---|---|---|
| MR-1 | Scaffold, PKI, mTLS Secret, chart/CI skeleton | merged |
| MR-2 | KeyDB, manager, control, data, extras HelmOps | merged |
| MR-3 | Scheduler ID allocation, manager REST client, registration | merged |
| MR-4a | `p2p-static` chart and bootstrap payload | merged |
| MR-4b | CREATE-only admission webhook and userData injection | merged |
| MR-5 | Status conditions, chart version guard, release workflow, docs | merged |
| MR-6 | Traefik extras REST Ingress, per-Ingress toggles, manager-only canary | merged |
| MR-7 | Operator-managed gRPC ingress (nginx and Traefik) | merged |
| MR-8 | Edge TLS termination, h2c backend, and CA-only Secret for Traefik clientAuth | merged |
| MR-9 | Render the CA-only Secret in the extras chart instead of Fleet downstreamResources | merged |
| MR-10 | Patched Dragonfly chart and end-to-end gRPC TLS (HTTPS backend + ServersTransport) | this change |

Live envtest/pilot items remain deferred for the MR-10 follow-up list.

## Development

Go 1.26 is required because the operator builds against the Rancher 2.15 and
Fleet 0.16 API machinery used by the target management cluster.

Docker is the source of truth for builds and tests:

```bash
make docker-build   # container image
make docker-test    # go test ./... inside Docker
make lint           # go vet ./... inside Docker
make helm-lint      # helm lint every chart under charts/
make helm-template  # render the operator, extras, and p2p-static charts
```

## Security notes

- The operator runs as UID/GID 65532 with a read-only root filesystem.
- mTLS private keys exist only in the `kubernetes.io/tls` Secret and are never logged.
- The operator uses only Wrangler/lasso Kubernetes clients (no client-go clientset
  or dynamic client) and requests no Ingress or VPA permissions.

## Release

Pushing a `v*` tag runs `.github/workflows/release.yml`:

- builds and pushes the multi-arch operator image tagged with the Git tag
  (`v<chart version>`);
- lints every chart under `charts/` and publishes it with `chart-releaser` to
  the `gh-pages` branch.

The repository charts are `d7y-cattle-operator`, `dragonfly-extras`, and
`p2p-static`. The KeyDB chart is external (`enapter/keydb` `0.48.0` from
`https://enapter.github.io/charts/`) and is not vendored or published here. The
operator pins that external chart and submits the three-master, no-PVC values
described above.

## License

Apache-2.0. See [LICENSE](LICENSE).
