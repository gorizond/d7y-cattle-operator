# p2p-static

Nginx static server for the Dragonfly P2P bootstrap flow. It serves the static
asset volume under `/bin/` and `/nydus/<version>/` only. The host bootstrap
script is embedded in the operator (`pkg/machinebootstrap/assets/p2p-install.sh`)
and is not served by this chart.

## Install

```bash
helm install p2p-static charts/p2p-static \
  --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=static.example.test \
  --set static.existingClaim=static-bin
```

`static.existingClaim` is optional. Without it the chart uses an `emptyDir`;
populate it from GitOps or an init job. `static.sizeLimit` optionally caps the
`emptyDir`.

## Manager-host route

The chart can serve the static assets on the manager host, using the manager's
TLS Secret:

```bash
helm install p2p-static charts/p2p-static \
  --namespace dragonfly-system \
  --set manager.enabled=true \
  --set manager.host=manager.example.com \
  --set manager.tlsSecret=manager-tls
```

`manager.entryPoint` defaults to `websecure`; the IngressRoute uses
`ingress.className` (default `traefik`). The route matches
`Host("<manager-host>")` plus `PathPrefix("/bin")` or `PathPrefix("/nydus")`,
and forwards to the `p2p-static` Service. The manager REST API keeps its own
`/api` paths; these prefixes do not overlap.

## Asset volume

The chart never ships binaries. Populate the static volume with the trusted
`v2.4.5` nydus daemon assets (`nydusd`, `nydus-image`, `nydus-overlayfs`) and
the patched nydus-snapshotter release. `/nydus/<version>/` is preferred by the
host updater; `/bin/` is the legacy fallback. The embedded operator script
checks executability but not signatures. The patched snapshotter release is
checksum-verified by the embedded updater.

The Ingress is optional and does not manage TLS or DNS.

## Self-test

```bash
bash pkg/machinebootstrap/assets/test.sh
bash scripts/test-p2p-static.sh
```

The self-test uses temporary roots, `SKIP_DOWNLOAD=1`, and a fake
`curl`/release asset for the snapshotter download path. It tests the embedded
script, including env injection, certificate files/modes, dfget/dynconfig,
scheduler ID, fscache/fusedev selection, nydus update, fail-open fallback, and
the selector matrix. `scripts/test-p2p-static.sh` also renders this chart and
checks that `/bin` and `/nydus` are served while `/p2p-install.sh` is not.
