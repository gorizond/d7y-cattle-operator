# p2p-scheduler-cert

Renders the per-seed `dragonfly-p2p-scheduler` TLS Secret
(`kubernetes.io/tls`) in the target namespace. The chart creates no other
resources.

The operator creates one `<cluster>-p2p-scheduler-cert` HelmOp per seed and
supplies the leaf certificate, key, and CA bundle from the operator-namespace
`<cluster>-p2p-scheduler` Secret. The HelmOp sets `takeOwnership`, so it adopts
the Secret delivered by the legacy raw Bundle during migration. The operator
marks the legacy Bundle `keepResources` and waits for its forced redeploy
before deleting it, so the Secret is orphaned rather than removed. The Secret
name and keys are fixed so the control release can keep mounting them.

## Values

```yaml
tls:
  crt: |
    -----BEGIN CERTIFICATE-----
    ...
  key: |
    -----BEGIN PRIVATE KEY-----
    ...
  ca: |
    -----BEGIN CERTIFICATE-----
    ...
```

All three values are required; `helm template` fails when omitted.

## Render check

```bash
helm template c1-p2p-scheduler-cert charts/p2p-scheduler-cert \
  --namespace dragonfly-system \
  --values scripts/testdata/p2p-scheduler-cert-values.yaml
```
