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

The static volume is self-populating: a `populate-static` init container reads
`static.populate.client.versions`, `static.populate.nydus.arches`, and
`static.populate.snapshotter.arches`, verifies every SHA-256, and writes:

- `/client/<version>/<arch>/{dfdaemon,dfget}`
- `/nydus/<version>/<arch>/{nydusd,nydus-image,nydus-overlayfs,nydus-static.tgz}`
- `/snapshotter/<version>/<arch>/<snapshotter-asset>`

Legacy compatibility paths (`/bin/dfdaemon`, `/bin/dfget`,
`/bin/nydusd`, `/nydus/<version>/...`, `/bin/<snapshotter-asset>`) are filled
from the **node architecture only** when absent. An existing `/bin/dfdaemon`
(the live poligon volume) is never overwritten. Downloads retry with backoff
and fail the init container instead of starting nginx with incomplete assets.
All artifact URLs, versions, checksums, arches, and the init image are
overridable under `static.populate`; `static.populate.enabled=false` restores
the old externally-populated behavior.

There is no PVC. The chart uses `emptyDir`, so everything is re-downloaded
after pod recreation. Defaults include amd64 and arm64 for client, nydus, and
the upstream nydus-snapshotter `v0.16.0`; that is roughly 550–560 MiB after
population (the `/bin` compatibility copies add ~150 MiB) plus transient
download/extract space during init. On a slow first start this means fetching
roughly 205 MiB before nginx becomes ready. Narrow the version/arch lists or
set `static.sizeLimit` (for example `1Gi`) if that cost is unacceptable.

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
(`dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu.tar.gz`, SHA-256
`ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7`). This is
the GNU build used by the host bootstrap: its `dfdaemon` is 42.1 MB with SHA-256
`e0d03d4759310e49cc1eba05341d2d4acf678f4926f2c37d9a4127ea1b94bf85`; both the
GNU and musl binaries report the same `1.5.14 (8887c145f)` build string, so the
artifact hash, not the version banner, is the discriminator. Both tarballs are
SHA-256 verified before extraction. `/nydus/<version>/` is preferred by the
host updater and the installer's tarball fallback; `/bin/` is the legacy
fallback and carries `dfget` for the P2P asset path. An existing
`/bin/dfdaemon` or `/bin/dfget` is preserved; the chart only fills missing
files from the GNU client tarball. The upstream nydus-snapshotter `v0.16.0` release tarballs are mirrored under
`/snapshotter/v0.16.0/<arch>/`; the embedded installer fetches the tarball
through P2P, then the static store, then the upstream GitHub release, and
verifies both the tarball and the extracted `bin/containerd-nydus-grpc`. The embedded operator script
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
