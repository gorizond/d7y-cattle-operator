# Quickstart: d7y-cattle-operator

This guide covers the checks that do not require a live pilot. Live-pilot
sections are included as explicit verification steps; they are not claimed as
passed by this repository.

## Prerequisites

- Rancher management cluster with Fleet and the `provisioning.cattle.io/v1`
  `Cluster` API.
- At least one RKE2 cluster in `fleet-default` (optional: the `local` cluster in
  `fleet-local`).
- Helm repositories for the upstream Dragonfly chart, `nydus-snapshotter`, and
  the external KeyDB chart (`enapter/keydb` `0.48.0` from
  `https://enapter.github.io/charts/`).
- A stateless Nginx static server (`NGINX_STATIC_URL`) with
  `deploy/nginx/nginx.conf` and binaries under `/bin/`.
- `kubectl`, `helm`, `curl`, `openssl`, `jq`.

Do not commit secrets or tokens. All examples use placeholders.

## 0. Chart version override

```bash
helm template charts/d7y-cattle-operator \
  --set dragonfly.chart.version=<version> \
  --set dragonfly.chart.repo=<repo-url> | grep -A2 'chart: dragonfly'
```

Expected: every Dragonfly HelmOp uses the override; the default is the verified
`1.10.1`. A different version emits an `UnsupportedChartVersion` warning event
when reconciled.

## 1. Unit and static checks

```bash
go test ./...
go vet ./...
helm lint charts/*
helm template d7y-cattle-operator charts/d7y-cattle-operator | kubectl apply --dry-run=server -f -
helm template p2p-static charts/p2p-static > /dev/null
```

Expected: all green; rendered operator manifests contain no PVC. The external
KeyDB chart is not vendored here; the operator pins `enapter/keydb` `0.48.0`
with `persistentVolume.enabled: false`.

## 2. Webhook: CREATE-only and idempotency (envtest/kind)

```bash
# Existing configs must not change.
kubectl get <provider>config <existing> -o jsonpath='{.spec.userData}' > before.txt
kubectl annotate cluster <c> p2p.dragonfly.io/seed=true
kubectl label cluster <c> p2p-dragonfly-io=true
kubectl apply -f <same-config>.yaml   # UPDATE
kubectl get <provider>config <existing> -o jsonpath='{.spec.userData}' > after.txt
diff before.txt after.txt             # must be identical
```

Create a new `*Config` and verify exactly one injection marker:

```bash
kubectl apply -f new-machine-config.yaml
kubectl get <provider>config <new> -o jsonpath='{.spec.userData}' | grep -c 'd7y-cattle-operator:p2p-install'
```

Expected: `1`; re-applying does not add a second call.

## 3. Manager role (pilot cluster)

```bash
kubectl label cluster <c> p2p-dragonfly-io=true
kubectl annotate cluster <c> p2p.dragonfly.io/manager=true
kubectl -n <cluster-ns> get helmop <c>-keydb <c>-manager -w
kubectl -n dragonfly-system get pods
```

Checks:

- `dragonfly-p2p-mtls` exists in the operator namespace and `<cluster-ns>` and is
  delivered downstream to `dragonfly-system`.
- KeyDB and manager pods are ready; no PVC exists.
- gRPC without a client certificate fails:

  ```bash
  openssl s_client -connect <manager-grpc-domain>:65003 \
    -servername <manager-grpc-domain> </dev/null
  # expected: handshake failure / certificate required
  ```

- gRPC with a client certificate succeeds:

  ```bash
  openssl s_client -connect <manager-grpc-domain>:65003 \
    -servername <manager-grpc-domain> \
    -cert tls.crt -key tls.key -CAfile ca.crt </dev/null \
    | grep 'Verify return code: 0'
  ```

- REST is JWT-protected:

  ```bash
  curl -sk https://<manager-api-host>:8080/api/v1/scheduler-clusters   # 401
  TOKEN=$(curl -sk https://<manager-api-host>:8080/api/v1/users/signin \
    -d '{"username":"...","password":"..."}' | jq -r .token)
  curl -sk -H "Authorization: Bearer $TOKEN" \
    https://<manager-api-host>:8080/api/v1/scheduler-clusters | jq .
  ```

- The webhook serving certificate is managed by needacert:

  ```bash
  kubectl -n <operator-ns> get svc <operator> \
    -o jsonpath='{.metadata.annotations.need-a-cert\.cattle\.io/secret-name}'
  kubectl get mutatingwebhookconfiguration <name> \
    -o jsonpath='{.webhooks[0].clientConfig.caBundle}'
  ```

## 4. Seed role and a new node

```bash
kubectl annotate cluster <c> p2p.dragonfly.io/seed=true
kubectl annotate cluster <c> p2p.dragonfly.io/scheduler-replicas=2
kubectl annotate cluster <c> p2p.dragonfly.io/seedclient-replicas=3
kubectl -n <cluster-ns> get helmop <c>-control -o yaml \
  | grep -E 'replicas|schedulerClusterID|externalManager'
kubectl -n <cluster-ns> get helmop <c>-data -o yaml \
  | grep -E 'replicas|dynconfig|externalManager'
```

Scale the pool by one node and verify:

- The `scheduler-cluster-id` annotation is set once and matches the HelmOp
  values.
- `GET /api/v1/scheduler-clusters` contains the matching record.
- The new machine config has one `cfg` line and the node bootstraps.
- `dfdaemon` and `nydus-snapshotter` DaemonSets are ready.
- The seed-client node has no port collisions. On a node running both
  components, check:

  ```bash
  ss -lntup | grep -E ':(4000|4001|4002|4003|4004|4005|4006|4100|4101|4102|4103|4104|4105|4106)'
  ```

  Client ports are `4000–4006`; seedClient ports are `4100–4106`.
- `proxy`, `metrics`, `health`, and `stats` bind to `127.0.0.1`; check the
  rendered `data` HelmOp values.
- Local dynconfig is applied; data ConfigMaps do not contain `manager.addr`.
- Replica counts match the annotations (`2` and `3`).

## 5. Manager DB loss self-heal

```bash
kubectl -n dragonfly-system delete pod -l app=dragonfly-manager   # emptyDir DB
kubectl -n <cluster-ns> annotate cluster <c> d7y-test=reconcile
```

Expected: within one reconcile, the scheduler record with the same ID exists
again; no duplicate is created.

## 6. Cleanup

```bash
kubectl annotate cluster <c> p2p.dragonfly.io/seed-
kubectl annotate cluster <c> p2p.dragonfly.io/manager-
kubectl label cluster <c> p2p-dragonfly-io-
kubectl -n <cluster-ns> get helmop   # none for this cluster
```

Expected: HelmOps are gone; the `scheduler-cluster-id` annotation remains; the
mTLS Secret remains. The Cluster conditions show `RolesResolved=False` with
reason `LabelMissing`, and the seed conditions show `SeedDisabled` if they
existed before the label was removed.

## 7. Local cluster in `fleet-local`

```bash
kubectl -n fleet-local label cluster local p2p-dragonfly-io=true
kubectl -n fleet-local annotate cluster local p2p.dragonfly.io/manager=true
kubectl -n fleet-local get helmop
```

Expected: HelmOp and Secret copy in `fleet-local`; downstream resources in
`dragonfly-system`; the controller does not assume `fleet-default`.

## Negative controls

- A Cluster without `p2p-dragonfly-io: "true"` gets zero HelmOps and no managed
  conditions.
- An existing `*Config` after an operator upgrade has byte-identical
  `userData`.
- An UPDATE on a `*Config` returns no patch.
- No PVC exists for operator components (`kubectl get pvc -A`).
- Ports `8002` and `4000` reject clients without a certificate.
- Ports `4001–4006` and `4101–4106` are not reachable from outside the cluster;
  storage ports `4005/4006` and `4105/4106` are cluster-only.
- Two Clusters with the same scheduler ID group schedulers in the manager, but
  seed peers do not exchange data across Clusters; use a distinct ID per
  Kubernetes cluster.
