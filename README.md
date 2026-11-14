# d7y-cattle-operator

Kubernetes operator that runs [Dragonfly](https://d7y.io) P2P on Rancher-managed
RKE2 clusters.

**Status**: MR-7 (operator-managed gRPC ingress for nginx and Traefik and an
optional public TLS Secret for the manager gRPC server; MR-6 traefik-compatible
extras REST Ingress and per-Ingress toggles). The Cluster controller, role
resolution, KeyDB, manager, seed role, manager registration,
`dragonfly-extras`, the `p2p-static` chart, and the admission webhook are
implemented. Live envtest/pilot verification remains deferred.

## What it does today

- Parses and validates the operator configuration from flags/environment variables.
- Ensures a single mTLS CA and leaf certificate Secret (`dragonfly-p2p-mtls` by
  default) in the operator namespace. The Secret is created once and never
  overwritten, and is replicated into each Cluster namespace.
- Reconciles `provisioning.cattle.io/v1` Clusters selected by the
  `p2p-dragonfly-io: "true"` label.
- Deploys KeyDB and the Dragonfly manager through Fleet HelmOps, without
  PersistentVolumeClaims.
- Deploys the seed role (scheduler/control, seedClient+client/data, and
  nydus-snapshotter) when `p2p.dragonfly.io/seed: "true"` and a scheduler
  cluster ID is present.
- Manages the manager Ingresses and optional VPAs through the separate
  `<cluster>-extras` HelmOp and the `dragonfly-extras` chart. The operator has
  no Ingress or VPA RBAC and creates neither kind directly.
- Uses a JWT-authenticated manager REST client (`pkg/managerapi`) and a
  thread-safe scheduler ID allocator (`pkg/registry`) for seed registration:
  auto mode writes the server-assigned ID to both `scheduler-cluster-id` and
  the durable `scheduler-cluster-id-auto` marker; user-owned IDs are never
  overwritten and mismatches are reported via the `d7y.io/IdMismatch` condition.
  Missing records are recreated and relinked on every reconcile.
- Publishes role/registration state as Cluster conditions
  (`d7y.io/RolesResolved`, `d7y.io/KeyDBReady`, `d7y.io/ControlReady`,
  `d7y.io/DataReady`, `d7y.io/NydusReady`, `d7y.io/Registered`) and updates
  them only when they change.
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
  --set managerApiCredentialsSecret=dragonfly-manager-api
```

For local development use `charts/d7y-cattle-operator` instead of the repository
chart. The release tag (`v<chart version>`) and the default image tag
(`v<chart version>`) are kept in sync by the release workflow.

The operator reads credentials only from Secret names. No secret values are passed
through flags or chart values. `nginx.staticURL` and
`managerApiCredentialsSecret` are required by the configuration contract but are
not used until the p2p-static and registration MRs.

### Required values

| Value | Environment variable | Description |
|---|---|---|
| `manager.grpcDomain` | `MANAGER_GRPC_DOMAIN` | Public manager gRPC hostname, without scheme |
| `manager.apiURL` | `MANAGER_API_URL` | Public manager REST API URL (`https://`) |
| `nginx.staticURL` | `NGINX_STATIC_URL` | Base URL of the static asset server (`http` or `https`) |
| `managerApiCredentialsSecret` | `MANAGER_API_CREDENTIALS_SECRET` | Secret with `username` and `password` keys for the manager API |
| `manager.publicTLSSecret` | `MANAGER_PUBLIC_TLS_SECRET` | Optional Secret with `tls.crt`/`tls.key` for the manager gRPC server; empty keeps the internal mTLS certificate |
| `dragonfly.chart.repo` | `DRAGONFLY_CHART_REPO` | Helm repository URL of the Dragonfly chart |
| `extras.chart.repo` | `EXTRAS_CHART_REPO` | Helm repository URL of the dragonfly-extras chart |
| `extras.chart.name` | `EXTRAS_CHART_NAME` | dragonfly-extras chart name |
| `extras.chart.version` | `EXTRAS_CHART_VERSION` | dragonfly-extras chart version |
| `extras.ingress.controller` | `EXTRAS_INGRESS_CONTROLLER` | `nginx` (default) or `traefik` |
| `extras.ingress.className` | `EXTRAS_INGRESS_CLASS_NAME` | Ingress class; empty uses the cluster default |
| `extras.ingress.grpcEnabled` | `EXTRAS_INGRESS_GRPC_ENABLED` | Render the gRPC Ingress when the manager role is active; default `true` |
| `extras.ingress.restEnabled` | `EXTRAS_INGRESS_REST_ENABLED` | Render the REST Ingress when the manager role is active; default `true` |
| `extras.ingress.tlsSecret` | `EXTRAS_INGRESS_TLS_SECRET` | Ingress TLS Secret; empty disables the TLS block |
| `extras.ingress.traefikServersTransportEnabled` | `EXTRAS_TRAEFIK_SERVERS_TRANSPORT_ENABLED` | Render the Traefik `ServersTransport`; default `true` |

Defaults: `mtlsSecretName=dragonfly-p2p-mtls`, `dragonfly.chart.name=dragonfly`,
`dragonfly.chart.version=1.10.1`, `extras.chart.name=dragonfly-extras`,
`extras.chart.version=0.1.2`, `extras.ingress.controller=nginx`,
`extras.ingress.grpcEnabled=true`, `extras.ingress.restEnabled=true`,
`manager.publicTLSSecret=""`,
`webhook.port=9443`, `leaderElect=false`.

## Roles and HelmOps

A Cluster labeled `p2p-dragonfly-io: "true"` gets one KeyDB release. The role
annotations add the Dragonfly releases:

| Roles | HelmOps |
|---|---|
| label only | `<cluster>-keydb`, `<cluster>-extras` (KeyDB VPA only) |
| `manager: "true"` | KeyDB, `<cluster>-manager`, `<cluster>-extras` (Ingress + VPAs) |
| `seed: "true"` + scheduler ID | KeyDB, `<cluster>-control`, `<cluster>-data`, `<cluster>-nydus-snapshotter`, `<cluster>-extras` (VPAs) |
| manager + seed | all six releases |
| `seed: "true"` without scheduler ID | KeyDB + extras (KeyDB VPA); `SchedulerClusterIDMissing` event, no seed releases |

The operator requires Rancher 2.15+ with Fleet 0.16+ (the `HelmOp` CRD).
The KeyDB chart (`enapter/keydb` `0.48.0`) and nydus-snapshotter chart
(`0.0.10`) are pinned in the operator; KeyDB is external and is not published
from this module. The KeyDB HelmOp runs two active masters (`nodes: 2`,
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
| `d7y.io/NydusReady` | The nydus-snapshotter HelmOp was applied (`seed: "true"`). |
| `d7y.io/Registered` | Scheduler and seed-peer records were ensured and linked in the manager. |
| `d7y.io/RegistrationFailed` | Registration failed permanently; `d7y.io/Retrying` marks transient failures. |
| `d7y.io/IdMismatch` | The user-provided `scheduler-cluster-id` differs from the manager server ID; the annotation is preserved. |

A label-only Cluster reports `d7y.io/ControlReady=False`, `d7y.io/DataReady=False`, and
`d7y.io/NydusReady=False` with reason `SeedDisabled`; a seed Cluster whose mTLS Secret
is missing reports `MTLSSecretUnavailable`. Conditions never contain Secret
values or certificate material.

### Dragonfly chart version

`dragonfly.chart.version` defaults to the verified `1.10.1` and is passed to
every Dragonfly HelmOp (`manager`, `control`, `data`). A different value is
allowed for experiments, but the operator emits an `UnsupportedChartVersion`
warning event on each reconcile because TLS fields and topology control/data
behavior may change between chart versions. Pin the chart version in the
operator release values; never hardcode it in Go code.

### Port security

Dragonfly peers use `hostNetwork`, so Kubernetes NetworkPolicy does not cover
their node ports. Enforce the following with node firewall rules and cloud
security groups:

| Ports | Purpose | Exposure |
|---|---|---|
| `8002` | Scheduler gRPC | mTLS required; allow only cluster nodes/manager |
| `4000` | dfdaemon upload | mTLS required; allow only cluster peers/scheduler |
| `4001/4002/4003/4004` | client proxy/metrics/health/stats | bind to `127.0.0.1`; do not expose |
| `4005/4006` | client storage TCP/QUIC | cluster network only |
| `4100` | seedClient upload | mTLS required; allow only cluster peers/scheduler |
| `4101/4102/4103/4104` | seedClient proxy/metrics/health/stats | bind to `127.0.0.1`; do not expose |
| `4105/4106` | seedClient storage TCP/QUIC | cluster network only |
| `65003` | manager gRPC | mTLS required; public Ingress is allowed |
| `8080` | manager REST | JWT required; public Ingress is allowed |

Do not use `proxy.basicAuth` for peer authentication: chart 1.10.1 places it in
the wrong configuration block. The operator keeps `proxy`, `metrics`, `health`,
and `stats` on `127.0.0.1` for both `client` and `seedClient`. Because both
DaemonSets may run on the same node, seedClient ports are shifted to
`4100–4106` so they do not collide with client `4000–4006`.

All releases target `dragonfly-system` and depend on each other through
`spec.dependsOn`: control and nydus depend on KeyDB, data depends on control and
KeyDB, manager depends on KeyDB, and extras depends on manager when Ingress is
enabled. The mTLS Secret is copied into the Cluster namespace and delivered
downstream through `spec.downstreamResources`.

### Extras chart

The `dragonfly-extras` chart owns the manager gRPC/REST Ingresses and the
recommendation-only VPAs. The operator only creates the `<cluster>-extras`
HelmOp:

- `ingress.enabled` is true only for the manager role. The per-Ingress toggles
  are `ingress.grpc.enabled` and `ingress.rest.enabled`; both default to `true`.
- `ingress.controller=nginx` (default) keeps the existing behavior: gRPC uses
  `GRPCS` with client mTLS verification from `dragonfly-p2p-mtls` and no SSL
  redirect; REST uses `backend-protocol: HTTPS`.
- `ingress.controller=traefik` renders a `traefik.io/v1alpha1` `IngressRoute`
  named `<release>-manager-api` for the REST backend (Traefik v3 ignores
  `traefik.ingress.kubernetes.io/service.serversscheme`). The route uses
  `scheme: https` and, when enabled, references the `ServersTransport` named
  `<release>-manager-rest` in the release namespace; that resource uses
  `insecureSkipVerify: true`. The REST Ingress is not rendered in Traefik mode,
  and no nginx annotations are added.
- In Traefik mode the gRPC endpoint is an `IngressRouteTCP` named
  `<release>-manager-grpc` (CRD `traefik.io/v1alpha1`, entry point `websecure`)
  with `HostSNI` matching `ingress.grpc.host` and TLS passthrough. gRPC mTLS
  must reach the manager, so the manager presents the public certificate (see
  below) and the internal CA remains the client CA. The nginx gRPC Ingress is
  not rendered in Traefik mode; the REST IngressRoute is unchanged.
- `ingress.tls.enabled` adds a TLS block using `ingress.tls.secretName`. The
  manager-only canary uses `dragonfly-p2p-mtls` for
  `manager-api.koldun.negash.ru`.
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
- `manager.publicTLSSecret` (operator value, `MANAGER_PUBLIC_TLS_SECRET`) points
  at a Secret with a public `tls.crt`/`tls.key`, for example
  `poligon-manager-public-tls` created directly on the cluster. When set, the
  manager gRPC server serves that certificate while `manager.config.server.grpc.tls.caCert`
  stays the internal `/etc/dragonfly/certs/ca.crt` client CA, and the Secret is
  mounted read-only at `/etc/dragonfly/public-tls`. REST keeps the internal
  certificate because Traefik terminates public TLS for REST. The Secret is not
  managed by this repo (no cert-manager or Let's Encrypt chart/CRs); after
  renewal, update the Secret and restart the manager to load the new
  certificate.
- `vpa.enabled` is set from API discovery for `autoscaling.k8s.io/v1`; the
  chart additionally checks `Capabilities.APIVersions.Has`. Target names are
  `<cluster>-keydb`, `<cluster>-manager`, `<cluster>-control-scheduler`,
  `<cluster>-data-seed-client`, and `<cluster>-nydus-snapshotter`; each target
  is enabled only for its active role.
- The chart creates no PVC and no Secrets. The operator RBAC remains limited to
  Clusters, HelmOps, Secrets, and Events.

## Cluster annotations

The operator only acts on Clusters labeled `p2p-dragonfly-io: "true"`.

| Annotation | Value | Effect |
|---|---|---|
| `p2p.dragonfly.io/manager` | `"true"` | Manager role: manager and its database, KeyDB as external Redis |
| `p2p.dragonfly.io/seed` | `"true"` | Seed role: scheduler, seed client/client, nydus-snapshotter |
| `p2p.dragonfly.io/scheduler-cluster-id` | integer | Scheduler cluster ID; required by the seed role until MR-3 auto-allocates it |
| `p2p.dragonfly.io/scheduler-replicas` | integer | Scheduler replicas (default `1`) |
| `p2p.dragonfly.io/seedclient-replicas` | integer | Seed client replicas (default `1`) |

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
control/data/nydus without blocking manager/KeyDB.

## Bootstrap static chart

`charts/p2p-static` serves `/p2p-install.sh` and `/bin/` through Nginx. The
bootstrap payload is a base64url **env-file** (no JSON, no `jq`, no python on the
node). Containerd keeps **overlayfs as the global snapshotter** and selects Nydus
per runtime with `runtimeClass: nydus`; do **not** set
`machineGlobalConfig.snapshotter: nydus`. See `charts/p2p-static/README.md` for
values, payload keys, containerd templates, and pilot risks.

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
`contracts/bootstrap-payload.md`; userData is patched in plain YAML or
base64-encoded form with the marker
`# d7y-cattle-operator:p2p-install`. Reapplying does not add a second call.

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

The gRPC Ingress reads `auth-tls-secret` from `dragonfly-system`; the operator
must be installed there (the default `namespace`) so the mTLS Secret is present
in the Ingress namespace.

## Known pilot risk: nydus-snapshotter chart 0.0.10

The nydus HelmOp keeps the intended contract values, but chart 0.0.10 does not
expose all of them:

- `hostSocket` is ignored; the chart hardcodes its containerd integration paths,
  so the RKE2 socket `/run/k3s/containerd/containerd.sock` is not configured.
- `initContainer.command`/`initContainer.privileged` are ignored; the chart runs
  its own `toml-cli` init container.
- `runtimeClass: nydus` is ignored; the DaemonSet template does not set a
  runtime class.

`priorityClassName` and `containerRuntime.containerd.configFile` are supported.
Resolve the socket/runtime-class gap in the pilot (chart patch or a fork) before
enabling the seed role on a production cluster.

## MR roadmap

| MR | Scope | Status |
|---|---|---|
| MR-1 | Scaffold, PKI, mTLS Secret, chart/CI skeleton | merged |
| MR-2 | KeyDB, manager, control, data, nydus, extras HelmOps | merged |
| MR-3 | Scheduler ID allocation, manager REST client, registration | merged |
| MR-4a | `p2p-static` chart and bootstrap payload | merged |
| MR-4b | CREATE-only admission webhook and userData injection | merged |
| MR-5 | Status conditions, chart version guard, release workflow, docs | merged |
| MR-6 | Traefik extras REST Ingress, per-Ingress toggles, manager-only canary | merged |
| MR-7 | Operator-managed gRPC ingress (nginx and Traefik) and manager public TLS Secret | this change |

Live envtest/pilot items remain deferred for the MR-7 follow-up list.

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
