# p2p-static

Nginx static server for the Dragonfly P2P bootstrap flow. It serves
`/p2p-install.sh` and the static assets under `/bin/` (and optionally
`/nydus/<version>/`). The chart does not ship binaries and creates no PVC or
Secret. The bootstrap script installs the host-only nydus snapshotter, the
host `nydus-resolver` HTTPS forward proxy, and their updater; there is no
in-cluster nydus or resolver DaemonSet/HelmOp.

## Install

```bash
helm install p2p-static charts/p2p-static \
  --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=static.example.test \
  --set static.existingClaim=static-bin
```

`static.existingClaim` is optional. Without it the chart uses an `emptyDir`;
populate it from GitOps/an init job. `static.sizeLimit` optionally caps the
`emptyDir`.

The Ingress is optional and does not manage TLS or DNS.

## Bootstrap payload

`p2p-install.sh` expects `?cfg=<base64url>` where the decoded value is an
env-file, not JSON:

```text
MANAGER_GRPC=https://manager-grpc.example.test:65003
STATIC_URL=http://rancher-static.example.test
SCHEDULER_HOST=d7y-scheduler.c-m-example.example.test
SCHEDULER_CLUSTER_ID=2
CA_CRT_B64=<standard base64 of ca.crt>
CLIENT_CRT_B64=<standard base64 of tls.crt>
CLIENT_KEY_B64=<standard base64 of tls.key>
SMART_PROXY_URL=https://proxy.example.test   # optional; fail-open
RESOLVER_UPSTREAM=https://proxy.example.test
RESOLVER_LISTEN=127.0.0.1:3999
```

`RESOLVER_UPSTREAM` and `RESOLVER_LISTEN` are optional as a pair. When both
are present the script installs and requires the host resolver; when both are
absent it falls back to the direct registry. There is no registry map: the
resolver uses the CONNECT host as the first upstream path segment and the
registry proxy normalizes aliases and owns `/<registry>/...` routing and the
registry allowlist.

`SMART_PROXY_URL` is optional. When present it is validated as an
http(s) URL and kept for the in-cluster mirror bootstrap; new VMs never make
the proxy mandatory (the script stays fail-open). The mirror chart points
allowlisted registries at the external proxy domain, not at a per-node proxy. The in-cluster
`containerd-mirror` DaemonSet applies the proxy `hosts.toml` after RKE2.

The script decodes the outer URL-safe base64, parses `KEY=VALUE` lines with
`read`/`case` (no `eval`), and decodes the certificate fields with `base64 -d`.
There is no JSON, `jq`, or python dependency on the node. Certificate values
are never logged; `tls.key` is written with mode `0600`.

For local testing, set `P2P_ROOT=/tmp/root`, `SKIP_DOWNLOAD=1`, and
`SKIP_PREFLIGHT=1`; the script will write its files below that root and skip
network/systemd/module actions.

The resolver config template is
`files/nydus-resolver-config.yaml.tmpl`. The script writes the real
`/etc/nydus-resolver/config.yaml` (non-secret) from the payload, the fixed CA
paths, and the detected system CA bundle. The resolver binary creates
`/var/lib/nydus-resolver/ca.key` (0600) and `ca-bundle.pem` (local CA + system
roots) on first start.

## Containerd design

Nydus is the node-wide CRI snapshotter and runs on the host, managed by
`nydus-snapshotter-bootstrap.service`. No in-cluster nydus DaemonSet is
deployed, and workloads do not need `runtimeClass: nydus`.

- containerd 2.x / config v3: `p2p-install.sh` writes the drop-in
  `config-v3.toml.d/90-nydus.toml`. It sets
  `[plugins.'io.containerd.cri.v1.images'] snapshotter='nydus'`,
  `use_local_image_pull=true`, `disable_snapshot_annotations=false`,
  `discard_unpacked_layers=false`, plus `[proxy_plugins.nydus]` pointing at
  `/run/containerd-nydus/containerd-nydus-grpc.sock`. The RKE2
  `config-v3.toml.tmpl` is left untouched.
- containerd 1.7 / config v2 has no drop-in imports, so the script writes a
  global-nydus `config.toml.tmpl` with the same snapshotter and proxy settings.
- The host unit starts before `rke2-server.service`/`rke2-agent.service`
  (`Before=`) and uses
  `containerd-nydus-grpc --config /etc/nydus/config.toml --nydusd /usr/local/bin/nydusd --nydusd-config /etc/nydus/nydusd-config.json`.
- `/etc/nydus/config.toml` uses `daemon_mode = "multiple"` and
  `fs_driver = "fusedev"`. Under `[experimental]` it sets
  `enable_referrer_detect = true` and `enable_index_detect = true`; tarfs is
  not enabled. These are nydus-snapshotter `config.toml` options (in the
  `[experimental]` table), not nydusd JSON fields.
- `/etc/nydus/nydusd-config.json` uses the `registry` backend with the
  object-form proxy at `http://127.0.0.1:4001` and `fallback: true`. The
  `blobcache` device cache stays enabled, and `fs_prefetch` is on
  (`threads_count: 8`, `merging_size: 1 MiB`, `prefetch_all: true`).
  `rafs.prefetch`/`rafs.readahead` are intentionally not emitted: the pinned
  nydusd schema does not document those keys, and unknown fields can prevent
  nydusd from starting.

If manager/scheduler preflight fails, or dfdaemon/nydus download or startup
fails, the script writes an overlayfs-only direct-registry configuration and
exits success so RKE2 can start. On containerd 2.x it removes the nydus
drop-in; on 1.x it writes an overlayfs template. It does not enable the
`127.0.0.1:4001` nydus proxy in that path. Existing certificate/config files
are left in place for a later retry.

## Harbor admin Secret

Harbor is deployed by the operator, not by this chart. Before enabling
`p2p.dragonfly.io/harbor`, put a Secret in the local/operator namespace
(default name `harbor-admin`) with the `HARBOR_ADMIN_PASSWORD` key. The
operator copies it per cluster and Fleet delivers it to Harbor as
`existingSecretAdminPassword`; values must never be placed in chart values or
Git.

## Host nydus-resolver

`nydus-resolver` is a static amd64 host binary attached to the GitHub
Release, installed to `/usr/local/bin/nydus-resolver` by `p2p-install.sh`.
It is not built into the container image and is not a DaemonSet.

Release asset and URL:

- asset: `nydus-resolver-linux-amd64` and `nydus-resolver-linux-amd64.sha256`
- pinned: `https://github.com/<GH_REPO>/releases/download/<RESOLVER_VERSION>/nydus-resolver-linux-amd64`
- latest: `https://github.com/<GH_REPO>/releases/latest/download/nydus-resolver-linux-amd64`

Defaults in `p2p-install.sh`:

```bash
RESOLVER_VERSION=v0.1.33
RESOLVER_ASSET=nydus-resolver-linux-amd64
GH_REPO=gorizond/d7y-cattle-operator
```

Override them for a different release or repository. The script downloads
with `curl -fL`, verifies the sibling `.sha256` when the release publishes it,
installs the binary atomically with mode `0755`, and falls back to the direct
registry with a clear message if the release asset is unavailable. The daily
`nydus-updater` continues to refresh nydus assets from the static store; rerun
`p2p-install.sh` (with the desired `RESOLVER_VERSION`) to update the resolver.
Multi-arch assets are future work; non-amd64 nodes currently fall back to the
direct registry.

`nydus-resolver.service` runs before `nydus-snapshotter-bootstrap.service`
(`Before=`), and the snapshotter unit depends on it with
`After=nydus-resolver.service` and `Wants=nydus-resolver.service`. The
snapshotter receives:

```ini
Environment=HTTPS_PROXY=http://127.0.0.1:3999
Environment=NO_PROXY=127.0.0.1,localhost,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,.svc,.cluster.local
Environment=SSL_CERT_FILE=/var/lib/nydus-resolver/ca-bundle.pem
```

The resolver binds `127.0.0.1:3999`, terminates CONNECT TLS with a local leaf
certificate, and forwards `<upstream>/<connect-host>/<original-path>`. The
upstream registry proxy normalizes Docker aliases and enforces its registry
allowlist; the resolver has no registry names or map. `ca-bundle.pem` contains
the local CA first and the system roots after it, so using it as
`SSL_CERT_FILE` does not break other TLS in the snapshotter.

## Host dfdaemon bootstrap switch

`dfdaemon-bootstrap.service` is the cold-start P2P client that runs before
RKE2. The in-cluster `client` DaemonSet is the steady state on each node.

- `dfdaemon-bootstrap-boot.service` has `Wants=dfdaemon-bootstrap.service` and
  `Before=dfdaemon-bootstrap.service rke2-server.service rke2-agent.service`,
  so the host bootstrap starts on every VM boot even if the previous switch
  cycle disabled it.
- `dfdaemon-bootstrap-switch.timer` runs `dfdaemon-bootstrap-switch` every 30s
  (first check 45s after boot). The script detects the client pod with
  `crictl pods` in namespace `dragonfly-system` (label
  `app.kubernetes.io/name=client`, then pod name `*-data-client-*`), and checks
  that the pod has a running container. A container-name check is the fallback
  for older crictl versions; a TCP check of the client health port is used only
  after the host bootstrap is already disabled, because both processes may own
  the same local ports.
- When the client is running, the switch runs
  `systemctl disable --now dfdaemon-bootstrap.service` and writes
  `/var/lib/dragonfly/bootstrap-switched`. When the client is absent and the
  bootstrap is disabled/stopped, it runs
  `systemctl enable --now dfdaemon-bootstrap.service` and removes the marker.
- The switch never references or changes `nydus-snapshotter-bootstrap.service`
  (or any nydus unit). It is a host-only timer: the chart renders no switch
  DaemonSet and no PVC.

## Host nydus assets and updater

`p2p-install.sh` installs the pinned `v0.16.0` binaries to `/usr/local/bin`:
`containerd-nydus-grpc`, `nydusd`, `nydus-image`, and `nydus-overlayfs`. The
updater prefers `${STATIC_URL}/nydus/${NYDUS_VERSION}/<binary>` and falls back
to `${STATIC_URL}/bin/<binary>` for the legacy unversioned layout.

`nydus-updater.service` runs `nydus-updater` from the daily
`nydus-updater.timer`. The updater re-fetches the pinned nydus assets,
compares them with the installed binaries, installs changed files atomically,
and restarts `nydus-snapshotter-bootstrap.service` when anything changed. It
does not manage `nydus-resolver`; that binary comes from the GitHub release and
is updated by rerunning `p2p-install.sh`. It never talks to Kubernetes.

## Containerd version detection

`p2p-install.sh` reads `containerd --version` or the RKE2 containerd binary and
defaults to the containerd 2.x v3 drop-in path when neither is available, which
is the normal pre-RKE2 case on a new VM. Set `CONTAINERD_MAJOR=1` only for older
nodes; the bootstrap payload currently has no field for this.

## Self-test

```bash
bash charts/p2p-static/files/test.sh
```

The test uses temporary roots, `SKIP_DOWNLOAD=1`, and a fake `curl`/release
asset for the resolver download path; it asserts the env payload decode,
certificate files/modes, `dfget.yaml`, `dynconfig.yaml`, scheduler ID,
`/etc/nydus/config.toml` (multiple/fusedev, no tarfs), the registry-backend
`nydusd-config.json`, the global nydus drop-in (containerd 2.x) and global
template (containerd 1.x), resolver config/unit ordering/snapshotter proxy
environment, the updater script/units, the fail-open direct-registry path,
and absence of certificate values in logs. `scripts/test-p2p-static.sh` runs `bash -n`, `shellcheck` when
available, the self-test, and a `helm template` render check.

## Pilot gaps

- Base-template merge and drop-in import behaviour need a live pilot, including
  `containerd config dump` after a manual `rke2-agent`/`rke2-server` restart.
- The static `/bin` (or `/nydus/<version>`) volume must be populated externally
  with trusted `v0.16.0` assets plus the static amd64 `nydus-resolver`; the
  script checks executability but not checksums/signatures.
- Resolver systemd ordering, CA bundle regeneration on restart, and
  `SSL_CERT_FILE` behaviour must be verified on a live node. `nydusd`'s own
  registry client may not honor the snapshotter's proxy environment.
- The exact `containerd-nydus-grpc` v0.16.0 flags and the host-unit start order
  need pilot verification.
- nydus tarfs is intentionally disabled; enabling it later requires a kernel
  with tarfs support (>= 6.4) and a matching nydusd.
- The updater restarts the host snapshotter after a binary change; the pilot
  must confirm that the short socket outage does not disturb running pods, and
  that the static asset layout matches the deployed p2p-static volume.
- The bootstrap `dfget.yaml` intentionally has no `manager.addr`; it uses a
  local `dynconfig.yaml` with `scheduler.addr: <SCHEDULER_HOST>:443` so the
  node uses the public Traefik front. The exact dfdaemon dynconfig reload needs
  pilot verification.
- The static URL must be reachable from new nodes before CNI.
