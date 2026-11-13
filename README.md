# d7y-cattle-operator

Kubernetes operator that runs [Dragonfly](https://d7y.io) P2P on Rancher-managed
RKE2 clusters.

**Status**: MR-2. The Cluster controller, role reconciliation, KeyDB/manager/
control/data/nydus HelmOps, and optional VPA support are implemented. Manager
API registration and the admission webhook follow in later MRs.

## What it does today

- Parses and validates the operator configuration from flags/environment variables.
- Ensures a single mTLS CA and leaf certificate Secret (`dragonfly-p2p-mtls` by
  default) in the operator namespace. The Secret is created once and never
  overwritten, and is replicated into each Cluster namespace.
- Reconciles `provisioning.cattle.io/v1` Clusters selected by the
  `p2p-dragonfly-io: "true"` label.
- Deploys KeyDB, manager, scheduler, seed client, and nydus-snapshotter through
  Fleet HelmOps, without PersistentVolumeClaims.
- Publishes the manager on a gRPC endpoint with client mTLS and a REST API over
  HTTPS.
- Runs until it receives `SIGINT`/`SIGTERM`.

## Planned behavior

- Manager API registration and scheduler cluster ID allocation.
- Inject a bootstrap payload into new machine configs through a mutating webhook
  that only handles `CREATE` requests.

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
through flags or chart values.

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

A Cluster labeled `p2p-dragonfly-io: "true"` gets one KeyDB release. Role
annotations add the Dragonfly releases:

| Roles | HelmOps |
|---|---|
| label only | `<cluster>-keydb` |
| `manager: "true"` | KeyDB, `<cluster>-manager`, gRPC/REST Ingresses |
| `seed: "true"` | KeyDB, `<cluster>-control`, `<cluster>-data`, `<cluster>-nydus-snapshotter` |
| both | all five releases and both Ingresses |

The operator requires Rancher 2.15+ with Fleet 0.16+ (the `HelmOp` CRD).
KeyDB (`keydb` 0.1.0) and nydus-snapshotter (`0.0.10`) chart versions are pinned
in the operator; the KeyDB chart is published separately from this module.

All releases target `dragonfly-system` and depend on each other through
`spec.dependsOn` (`manager`/`control`/`nydus` on KeyDB, `data` on control and
KeyDB). The mTLS Secret is copied into the Cluster namespace and delivered
downstream through `spec.downstreamResources`.

VerticalPodAutoscalers are applied in recommendation-only mode (`updateMode:
Off`) when the `autoscaling.k8s.io/v1` CRD is installed; otherwise they are
skipped and a Cluster event is emitted.

### Manager API credentials

Registration (MR-3) reads the manager API credentials from a Secret. Create it
before enabling the manager role:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: dragonfly-manager-api
  namespace: dragonfly-system
type: Opaque
stringData:
  username: admin
  password: change-me
```

The operator never logs the credential values.

## Cluster annotations

The operator only acts on Clusters labeled `p2p-dragonfly-io: "true"`.

| Annotation | Value | Effect |
|---|---|---|
| `p2p.dragonfly.io/manager` | `"true"` | Manager role: manager, database, and public gRPC/REST endpoints |
| `p2p.dragonfly.io/seed` | `"true"` | Seed role: scheduler, seed client, dfdaemon, and nydus-snapshotter |
| `p2p.dragonfly.io/scheduler-cluster-id` | integer | Scheduler cluster ID; set once and never overwritten |
| `p2p.dragonfly.io/scheduler-replicas` | integer | Scheduler replicas (default `1`) |
| `p2p.dragonfly.io/seedclient-replicas` | integer | Seed client replicas (default `1`) |

Role handling is implemented in later MRs; the annotations are documented here
because they define the operator contract.

## Development

Go 1.26 is required because the operator builds against the Rancher 2.15 and
Fleet 0.16 API machinery used by the target management cluster.

Docker is the source of truth for builds and tests:

```bash
make docker-build   # container image
make docker-test    # go test ./... inside Docker
make lint           # go vet ./... inside Docker
make helm-lint      # helm lint charts/d7y-cattle-operator
make helm-template  # render the chart (with and without the webhook)
```

## Security notes

- The operator runs as UID/GID 65532 with a read-only root filesystem.
- mTLS private keys exist only in the `kubernetes.io/tls` Secret and are never logged.
- The bootstrap payload delivered to nodes contains a client key by design; this
  risk is documented in the design specification.

## License

Apache-2.0. See [LICENSE](LICENSE).
