# d7y-cattle-operator

Kubernetes operator that runs [Dragonfly](https://d7y.io) P2P on Rancher-managed
RKE2 clusters.

**Status**: MR-2a (manager-only slice). The Cluster controller, role resolution,
and the KeyDB + manager HelmOps are implemented. Seed/control/data/nydus
releases, public manager endpoints, VPA, manager API registration, and the
admission webhook follow in later MRs.

## What it does today

- Parses and validates the operator configuration from flags/environment variables.
- Ensures a single mTLS CA and leaf certificate Secret (`dragonfly-p2p-mtls` by
  default) in the operator namespace. The Secret is created once and never
  overwritten, and is replicated into each Cluster namespace.
- Reconciles `provisioning.cattle.io/v1` Clusters selected by the
  `p2p-dragonfly-io: "true"` label.
- Deploys KeyDB and the Dragonfly manager through Fleet HelmOps, without
  PersistentVolumeClaims.
- Keeps the manager Service cluster-internal; public gRPC/REST endpoints are not
  created yet.
- Runs until it receives `SIGINT`/`SIGTERM`.

The `p2p.dragonfly.io/seed` annotation is recognized but ignored: the operator
emits a `SeedRoleNotImplemented` Cluster event and deploys nothing for it.

## Follow-ups

- Seed role: scheduler (control), seed client (data), and nydus-snapshotter HelmOps.
- Manager public gRPC/REST Ingresses and the extras chart (p2p-static, VPA).
- Manager API registration and scheduler cluster ID allocation.
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

A Cluster labeled `p2p-dragonfly-io: "true"` gets one KeyDB release. The manager
annotation adds the Dragonfly manager release:

| Roles | HelmOps |
|---|---|
| label only | `<cluster>-keydb` |
| `manager: "true"` | KeyDB, `<cluster>-manager` (internal Service only) |
| `seed: "true"` | KeyDB; seed is not implemented yet, a Cluster event is emitted |
| both | KeyDB, `<cluster>-manager`; seed is still ignored |

The operator requires Rancher 2.15+ with Fleet 0.16+ (the `HelmOp` CRD).
The KeyDB chart (`keydb` 0.1.0) is pinned in the operator and published
separately from this module.

Both releases target `dragonfly-system` and the manager release depends on KeyDB
through `spec.dependsOn`. The mTLS Secret is copied into the Cluster namespace
and delivered downstream through `spec.downstreamResources`.

## Cluster annotations

The operator only acts on Clusters labeled `p2p-dragonfly-io: "true"`.

| Annotation | Value | Effect |
|---|---|---|
| `p2p.dragonfly.io/manager` | `"true"` | Manager role: manager and its database, KeyDB as external Redis |
| `p2p.dragonfly.io/seed` | `"true"` | Reserved for a follow-up MR; currently ignored with an event |

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
