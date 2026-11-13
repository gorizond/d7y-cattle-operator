# d7y-cattle-operator

Kubernetes operator that runs [Dragonfly](https://d7y.io) P2P on Rancher-managed
RKE2 clusters.

**Status**: MR-3b (seed registration/self-heal). The Cluster controller, role
resolution, KeyDB, manager, seed role, `dragonfly-extras` chart, and manager
registration with the scheduler ID allocator are implemented. p2p-static/Nginx
and the admission webhook follow in later MRs.

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
  overwritten and mismatches are reported via the `IdMismatch` condition.
  Missing records are recreated and relinked on every reconcile.
- Runs until it receives `SIGINT`/`SIGTERM`.

## Follow-ups

- p2p-static/Nginx extras and the bootstrap payload/webhook.
- The extras chart's VPA targetRef names must be verified against live workload
  names in the pilot.

## Install

The chart is not published yet; install it from a checkout:

```bash
helm install d7y-cattle-operator charts/d7y-cattle-operator \
  --namespace dragonfly-system \
  --create-namespace \
  --set manager.grpcDomain=manager-grpc.example.com \
  --set manager.apiURL=https://manager-api.example.com \
  --set nginx.staticURL=https://static.example.com \
  --set managerApiCredentialsSecret=dragonfly-manager-api
```

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
| `dragonfly.chart.repo` | `DRAGONFLY_CHART_REPO` | Helm repository URL of the Dragonfly chart |
| `extras.chart.repo` | `EXTRAS_CHART_REPO` | Helm repository URL of the dragonfly-extras chart |
| `extras.chart.name` | `EXTRAS_CHART_NAME` | dragonfly-extras chart name |
| `extras.chart.version` | `EXTRAS_CHART_VERSION` | dragonfly-extras chart version |

Defaults: `mtlsSecretName=dragonfly-p2p-mtls`, `dragonfly.chart.name=dragonfly`,
`dragonfly.chart.version=1.10.1`, `extras.chart.name=dragonfly-extras`,
`extras.chart.version=0.1.0`, `webhook.port=9443`, `leaderElect=false`.

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
The KeyDB chart (`keydb` 0.1.0) and nydus-snapshotter chart (`0.0.10`) are
pinned in the operator; KeyDB is published separately from this module.

All releases target `dragonfly-system` and depend on each other through
`spec.dependsOn`: control and nydus depend on KeyDB, data depends on control and
KeyDB, manager depends on KeyDB, and extras depends on manager when Ingress is
enabled. The mTLS Secret is copied into the Cluster namespace and delivered
downstream through `spec.downstreamResources`.

### Extras chart

The `dragonfly-extras` chart owns the manager gRPC/REST Ingresses and the
recommendation-only VPAs. The operator only creates the `<cluster>-extras`
HelmOp:

- `ingress.enabled` is true only for the manager role. gRPC uses port 65003 with
  `GRPCS`, client mTLS verification from `dragonfly-p2p-mtls`, and no SSL
  redirect. REST uses port 8080 over HTTPS.
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
overwritten (`IdMismatch` on server mismatch). This decision survives operator
restarts. Registration failures set `RegistrationFailed` or `Retrying` and block
control/data/nydus without blocking manager/KeyDB.

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

## Development

Go 1.26 is required because the operator builds against the Rancher 2.15 and
Fleet 0.16 API machinery used by the target management cluster.

Docker is the source of truth for builds and tests:

```bash
make docker-build   # container image
make docker-test    # go test ./... inside Docker
make lint           # go vet ./... inside Docker
make helm-lint      # helm lint charts/d7y-cattle-operator
make helm-template  # render the chart
```

## Security notes

- The operator runs as UID/GID 65532 with a read-only root filesystem.
- mTLS private keys exist only in the `kubernetes.io/tls` Secret and are never logged.
- The operator uses only Wrangler/lasso Kubernetes clients (no client-go clientset
  or dynamic client) and requests no Ingress or VPA permissions.

## License

Apache-2.0. See [LICENSE](LICENSE).
