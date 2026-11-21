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

`static.existingClaim` is optional. Without it the chart uses an `emptyDir`
that survives for the pod lifetime; an existing claim survives pod restarts.
`static.sizeLimit` optionally caps the `emptyDir`.

The static volume is self-populating: a `populate-static` init container
downloads the pinned nydus v2.4.5 static tarball, verifies its SHA-256, and
writes `nydusd`, `nydus-image`, `nydus-overlayfs`, and `nydus-fs-select` to both
`/nydus/<version>/` and `/bin/`. It also mirrors the verified tarball at
`/nydus/<version>/nydus-static.tgz` for the host installer's static-store
fallback. From the pinned dragonfly-client v1.5.14 tarball it fills
`/bin/dfdaemon` and `/bin/dfget` only when each file is absent; an existing
`dfdaemon` (the live poligon volume) is never overwritten. Downloads retry with
backoff and fail the init container instead of starting nginx with incomplete
assets. All artifact URLs, versions, checksums, and the init image are
overridable under `static.populate`; `static.populate.enabled=false` restores
the old externally-populated behavior.

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

The chart populates the static volume from pinned upstream releases; it does
not vendor binaries. Defaults are nydus `v2.4.5`
(`nydus-static-v2.4.5-linux-amd64.tgz`) and dragonfly-client `v1.5.14`
(`dragonfly-client-v1.5.14-x86_64-unknown-linux-musl.tar.gz`). Both tarballs
are SHA-256 verified before extraction. `/nydus/<version>/` is preferred by the
host updater and the installer's tarball fallback; `/bin/` is the legacy
fallback and carries `dfget` for the P2P asset path. An existing
`/bin/dfdaemon` or `/bin/dfget` is preserved; the chart only fills missing
files. The patched nydus-snapshotter release is not served by this chart; the
embedded installer fetches it through P2P, then the static store, then its
GitHub release, and verifies its `.sha256` asset. The embedded operator script
checks executability of host binaries, not signatures, and `nydus-fs-select`
is shipped from the operator asset and checked against it by
`scripts/test-p2p-static.sh`. The chart's populate test also asserts that
`/bin/dfget` is installed and `/nydus/v2.4.5/nydus-static.tgz` is mirrored.

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
