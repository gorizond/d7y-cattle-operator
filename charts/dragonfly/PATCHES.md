# Local patches to the upstream Dragonfly chart

Vendored from `dragonflyoss/helm-charts` `dragonfly` **1.10.1** (appVersion 2.5.4).
The chart is republished as **1.10.1-patch.1** because Fleet HelmOps cannot
post-render and the operator cannot override the hardcoded probe commands.

Upstream source: `https://dragonflyoss.github.io/helm-charts` (`helm pull dragonfly --version 1.10.1`).

## Patch 1 — manager gRPC TLS indentation

`templates/manager/manager-configmap.yaml`

Upstream rendered `tls:` at the `server:` level instead of inside `server.grpc`,
so the manager ignored `manager.config.server.grpc.tls` and served plaintext/h2c
gRPC. The `tls:` block now nests under `grpc:` and keeps the existing
`caCert`/`cert`/`key` values. No other manager config keys change.

## Patch 2 — manager gRPC probes with mTLS

`templates/manager/manager-deployment.yaml` plus the shared helper
`dragonfly.grpcProbeTLSArgs` in `templates/_helpers.tpl`.

When `manager.config.server.grpc.tls` has `caCert`, `cert` and `key`, the
startup/readiness/liveness `grpc_health_probe` commands gain:

```
-tls -tls-ca-cert=<caCert> -tls-client-cert=<cert> -tls-client-key=<key>
```

When the TLS map is absent or incomplete, the probes keep the upstream
plaintext flags only.

## Patch 3 — scheduler gRPC probes with mTLS

`templates/scheduler/scheduler-statefulset.yaml` plus the same helper.

When `scheduler.config.server.tls` has `caCert`, `cert` and `key`, the
startup/readiness/liveness `grpc_health_probe` commands gain the same four TLS
flags. Otherwise the probes stay plaintext, matching upstream.

No other upstream files are changed apart from `Chart.yaml` (`version:
1.10.1-patch.1`). Dependencies (`charts/mysql`, `charts/postgres`, `charts/valkey`)
are vendored unchanged from the upstream package.
