# p2p-static

Nginx static server for the Dragonfly P2P bootstrap flow. It serves the static
asset volume under `/bin/`, `/nydus/<version>/`, `/client/<version>/<arch>/`,
and `/snapshotter/<version>/<arch>/`, plus the bootstrap script endpoints
`/p2p-install.sh` and `/container-init.sh`.

## Script endpoints

The chart renders `files/p2p-install.sh` and `files/container-init.sh` into a
ConfigMap and serves them directly at `/p2p-install.sh` and
`/container-init.sh` (no query parameters, no `sub_filter`). Each file is a
byte-identical copy of its `pkg/machinebootstrap/assets/` counterpart; the
self-test fails if the pairs drift. Both locations set `access_log off` and
`Cache-Control: no-store`.

The node-side env is embedded in the machine-bootstrap Secret by the operator
and exported to the piped `bash` with `set -a`/`set +a`: `P2P_DISTRO`,
`SCHEDULER_IPS`, `MANAGER_GRPC`, `STATIC_URL`, `SMART_PROXY_URL`,
`NYDUS_MIRROR_URL`, `SCHEDULER_CLUSTER_ID`, the
client/nydus/snapshotter versions, and the CA, client certificate, and client
key (base64). The effective `user_data` the driver sends (gzip+base64 of
the Secret value) stays below the 32 KiB limit used by the test provider; the
raw Secret value can be larger.

**Security:** the mTLS client key is in the Secret (and provider-visible
`user_data`), but **not** in the HTTP URL/access logs. Do not expose the Secret
or its env in public artifacts or tickets, and treat the machine-bootstrap
Secret as sensitive. The endpoint is reachable wherever the static URL is
published; restrict it like the bootstrap configuration.

## Container init endpoint

`/container-init.sh` prepares a container (no systemd, containerd,
privileged mode, or FUSE) and takes no arguments. Its env contract uses the
same names as `/p2p-install.sh`: required `STATIC_URL`, `MANAGER_GRPC`,
`SCHEDULER_CLUSTER_ID`, `CA_CRT_B64`, `CLIENT_CRT_B64`, `CLIENT_KEY_B64`;
optional `SCHEDULER_IPS`, `SMART_PROXY_URL`, `NYDUS_MIRROR_URL`, client/nydus
versions, checksum pins, registry credentials, `P2P_BIN_DIR`, `P2P_ROOT`,
`TARGET_ARCH`, and timeouts. It does not configure network/VPN/Tailscale or
iptables and does not install `nydus-mount`.

Ordering: run `container-init.sh` to exit 0 (`dfdaemon` healthy, `nydusd` and
`/usr/local/bin/{dfget,nydusd}` plus `/etc/nydus/config.json` installed), then
run the external `nydus-mount`/`nydusd` command for the model image, then start
`vllm`/`sglang` against the mount point.

`MANAGER_GRPC` must be the manager gRPC **SNI host** (the host used by the
manager gRPC ingress, e.g. `ingress.grpc.host`), not the manager REST API host.
`/etc/nydus/config.json` is a template: before starting `nydusd`, copy it and
inject `device.backend.config.host` and `repo` for the model image; the base
file alone is not runnable. Re-running `container-init.sh` with changed
certificates, cluster ID, or registry auth restarts only the script's own
`dfdaemon`; an unchanged environment is a no-op. Run the script from `/tmp` (or
another path), not `/usr/local/bin`, to keep the "three P2P binaries" rule.

## Install

```bash
helm install p2p-static charts/p2p-static \
  --namespace dragonfly-system \
  --values scripts/testdata/p2p-static-operator-values.yaml \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=static.example.test
```

`static.populate.enabled` defaults to `false` for standalone lint/render; set it
to `true` and provide the version/repository/checksum values (the fixture above
is an operator-shaped example). The integrated operator always supplies them.

The integrated operator deployment uses an `emptyDir` and never sets
`static.existingClaim`; the volume survives for the pod lifetime and is
re-populated after pod recreation. `static.existingClaim` remains available
for a standalone opt-in PVC, and `static.sizeLimit` optionally caps the
`emptyDir`.

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
The chart carries no version, repository, or checksum defaults: the operator
chart (`charts/d7y-cattle-operator/values.yaml`) owns them and renders them into
`static.populate.*`. `static.populate.enabled=false` restores the old
externally-populated behavior. If populate is enabled, the template fails when
the client versions, nydus version/arches, or snapshotter version/arches are
missing.

There is no PVC. The chart uses `emptyDir`, so everything is re-downloaded
after pod recreation. The operator values include amd64 and arm64 for client,
nydus, and the upstream nydus-snapshotter; that is roughly 550–560 MiB after
population (the `/bin` compatibility copies add ~150 MiB) plus transient
download/extract space during init. On a slow first start this means fetching
roughly 205 MiB before nginx becomes ready. Narrow the version/arch lists or
set `static.sizeLimit` (for example `1Gi`) if that cost is unacceptable.

## Operator-managed values

In the integrated deployment the operator creates the `p2p-static` HelmOp and
renders these values from config (`CLIENT_VERSION`, `NYDUS_VERSION`,
`NYDUS_SNAPSHOTTER_VERSION`, release repositories, manager host/TLS Secret, and
ingress class). The release is adopted with `takeOwnership=true`; no manual
chart edits are needed. The operator chart is the single source of those
versions/repositories/checksums; this chart only carries empty required-value
placeholders.

Checksums are dynamic: the populate init container first fetches the release
sidecar (`.sha256sum`/`.sha256`). Values `sha256`/`tarballSHA256` are fallback
pins only, used when a release publishes no checksum asset (Dragonfly client
v1.5.14). Dragonfly client releases publish no checksum/signature sidecar, so
the client path uses the configured checksum directly instead of probing
predictable 404 URLs. A mismatch fails the init container.

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
`Host("<manager-host>")` plus `PathPrefix` for `/p2p-install.sh`,
`/container-init.sh`, `/bin`, `/nydus`, `/client`, or `/snapshotter`, and
forwards to the `p2p-static` Service. The manager REST API keeps its own
`/api` paths; these prefixes do not overlap.

## Asset volume

The chart populates the static volume from the upstream releases selected by
the operator chart; it does not vendor binaries. The operator defaults are
nydus `v2.4.5` (`nydus-static-v2.4.5-linux-<arch>.tgz`) and dragonfly-client
`v1.5.14` (`dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu.tar.gz`, SHA-256
`ba080e62f1626ab2fc2a5a0629a5cd912cc15099508820403108abbe2f75b1f7`). This is
the GNU build used by the host bootstrap: its `dfdaemon` is 42.1 MB with SHA-256
`e0d03d4759310e49cc1eba05341d2d4acf678f4926f2c37d9a4127ea1b94bf85`; both the
GNU and musl binaries report the same `1.5.14 (8887c145f)` build string, so the
artifact hash, not the version banner, is the discriminator. Both tarballs are
SHA-256 verified before extraction. The host updater prefers
`/client/<version>/<arch>/`, `/nydus/<version>/<arch>/`, and
`/snapshotter/<version>/<arch>/`; nydus/snapshotter fall back to the legacy
`/bin` and `/nydus/<version>/` paths, while the client falls back to the
GitHub tarball. `/bin` also carries `dfget` for the host installer. An existing
`/bin/dfdaemon` or `/bin/dfget` is preserved; the chart only fills missing
files from the GNU client tarball. The upstream nydus-snapshotter release tarballs are mirrored under
`/snapshotter/<version>/<arch>/`; the embedded installer fetches the tarball
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
checks that `/bin`, `/nydus`, `/client`, `/snapshotter`, `/p2p-install.sh`, and
`/container-init.sh` are served, the manager route forwards all six prefixes,
the script endpoints have `access_log off`, and the chart scripts match the
embedded assets.
