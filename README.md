# d7y-cattle-operator

Kubernetes operator that runs [Dragonfly](https://d7y.io) P2P on Rancher-managed
RKE2 clusters.

**Status**: MR-2b (seed role). The Cluster controller, role resolution, KeyDB,
manager, and seed role releases are implemented. Manager public endpoints,
extras (VPA/p2p-static), manager API registration, and the admission webhook
follow in later MRs.

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
- Keeps manager and scheduler Services cluster-internal; public endpoints are
  not created yet.
- Runs until it receives `SIGINT`/`SIGTERM`.

## Follow-ups

- MR-3: manager API registration and automatic scheduler cluster ID allocation.
- MR-2c: public manager gRPC/REST Ingresses and the extras chart (p2p-static, VPA).
- Bootstrap payload injection through a mutating webhook that only handles
  `CREATE` requests.

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
not used until the extras and registration MRs.

### Required values

| Value | Environment variable | Description |
|---|---|---|
| `manager.grpcDomain` | `MANAGER_GRPC_DOMAIN` | Public manager gRPC hostname, without scheme |
| `manager.apiURL` | `MANAGER_API_URL` | Public manager REST API URL (`https://`) |
| `nginx.staticURL` | `NGINX_STATIC_URL` | Base URL of the static asset server (`http` or `https`) |
| `managerApiCredentialsSecret` | `MANAGER_API_CREDENTIALS_SECRET` | Secret with `username` and `password` keys for the manager API |
| `dragonfly.chart.repo` | `DRAGONFLY_CHART_REPO` | Helm repository URL of the Dragonfly chart |

Defaults: `mtlsSecretName=dragonfly-p2p-mtls`, `dragonfly.chart.name=dragonfly`,
`dragonfly.chart.version=1.10.1`, `webhook.port=9443`, `leaderElect=false`.

## Roles and HelmOps

A Cluster labeled `p2p-dragonfly-io: "true"` gets one KeyDB release. The role
annotations add the Dragonfly releases:

| Roles | HelmOps |
|---|---|
| label only | `<cluster>-keydb` |
| `manager: "true"` | KeyDB, `<cluster>-manager` (internal Service only) |
| `seed: "true"` + scheduler ID | KeyDB, `<cluster>-control`, `<cluster>-data`, `<cluster>-nydus-snapshotter` |
| manager + seed | all five releases |
| `seed: "true"` without scheduler ID | KeyDB only; `SchedulerClusterIDMissing` event, no seed releases |

The operator requires Rancher 2.15+ with Fleet 0.16+ (the `HelmOp` CRD).
The KeyDB chart (`keydb` 0.1.0) and nydus-snapshotter chart (`0.0.10`) are
pinned in the operator; KeyDB is published separately from this module.

All releases target `dragonfly-system` and depend on each other through
`spec.dependsOn`: control and nydus depend on KeyDB, data depends on control and
KeyDB, and manager depends on KeyDB. The mTLS Secret is copied into the Cluster
namespace and delivered downstream through `spec.downstreamResources`.

## Cluster annotations

The operator only acts on Clusters labeled `p2p-dragonfly-io: "true"`.

| Annotation | Value | Effect |
|---|---|---|
| `p2p.dragonfly.io/manager` | `"true"` | Manager role: manager and its database, KeyDB as external Redis |
| `p2p.dragonfly.io/seed` | `"true"` | Seed role: scheduler, seed client/client, nydus-snapshotter |
| `p2p.dragonfly.io/scheduler-cluster-id` | integer | Scheduler cluster ID; required by the seed role until MR-3 auto-allocates it |
| `p2p.dragonfly.io/scheduler-replicas` | integer | Scheduler replicas (default `1`) |
| `p2p.dragonfly.io/seedclient-replicas` | integer | Seed client replicas (default `1`) |

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
