# p2p-static

Nginx static server for the Dragonfly P2P bootstrap flow. It serves
`/p2p-install.sh` and the static assets under `/bin/` (and optionally
`/nydus/<version>/`). The chart does not ship binaries and creates no PVC or
Secret. The bootstrap script installs the host-only patched nydus snapshotter
and its static-asset updater; there is no in-cluster nydus DaemonSet or HelmOp.

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

The chart values `nydus.fsDriver` and `nydus.clusterId` are rendered into
`p2p-install.sh`:

```bash
helm install p2p-static charts/p2p-static \
  --set nydus.fsDriver=fusedev        # legacy fallback; default is fscache
  --set nydus.clusterId=example-node  # nydusd id/domain_id; default empty
```

`nydus.fsDriver` selects the host snapshotter filesystem driver (`fscache`
by default, `fusedev` as the documented fallback). `nydus.clusterId` is
optional: when empty the installer uses the payload scheduler cluster ID and
then the node hostname.

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
NYDUS_MIRROR_URL=https://proxy.example.test/docker.io   # optional; no fallback proxy
```

`SMART_PROXY_URL` is optional. When present it is validated as an
http(s) URL and kept for the in-cluster mirror bootstrap; new VMs never make
the proxy mandatory (the script stays fail-open). The in-cluster
`containerd-mirror` DaemonSet writes only `certs.d/_default/hosts.toml` ->
dfdaemon after RKE2; dfdaemon 1.5.14 forwards `?ns=` to the proxy.

`NYDUS_MIRROR_URL` is optional and must be the HTTPS base URL of the registry
proxy mirror path, including the registry segment (for example
`https://proxy.example.test/docker.io`; include `proxy.basePath` when the proxy
uses one). Empty keeps the legacy dfdaemon proxy block in `/etc/nydus/nydusd-config.json`.
When set, the installer writes the nydusd mirror layout described below and
generates a backend without `proxy.url`/`use_http`.

The script decodes the outer URL-safe base64, parses `KEY=VALUE` lines with
`read`/`case` (no `eval`), and decodes the certificate fields with `base64 -d`.
There is no JSON, `jq`, or python dependency on the node. Certificate values
are never logged; `tls.key` is written with mode `0600`.

For local testing, set `P2P_ROOT=/tmp/root`, `SKIP_DOWNLOAD=1`, and
`SKIP_PREFLIGHT=1`; the script will write its files below that root and skip
network/systemd/module actions.

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
  `containerd-nydus-grpc --config /etc/nydus/config.toml --nydusd /usr/local/bin/nydusd --nydusd-config <config>`.
  `<config>` is `/etc/nydus/nydusd-config.fscache.json` for `fscache` and
  `/etc/nydus/nydusd-config.json` for `fusedev`.
- `/etc/nydus/config.toml` uses the fscache shape by default:
  `daemon_mode = "shared"`, `nydusd_config =
  "/etc/nydus/nydusd-config.fscache.json"`, `fs_driver = "fscache"`,
  `threads_number = 8`, `[remote] skip_ssl_verify = false`, and
  `[remote.auth] enable_kubeconfig_keychain = true`. With
  `nydus.fsDriver=fusedev` it instead uses `daemon_mode = "multiple"`,
  `fs_driver = "fusedev"`, `[experimental] enable_referrer_detect =
  true`/`enable_index_detect = true`, and the legacy
  `/etc/nydus/nydusd-config.json`. Tarfs is never enabled.
- `/etc/nydus/nydusd-config.fscache.json` is the nydusd bootstrap config for
  EROFS over fscache. It is generated from the canonical
  `files/nydusd-config.fscache.json` shape: `registry` backend over the local
  Dragonfly proxy (`http://127.0.0.1:4001`, ping
  `http://127.0.0.1:4003/healthy`, `fallback: true`), `fscache` cache under
  `/var/lib/containerd-nydus/cache`, and `id`/`domain_id` from the chart
  value, the payload `NYDUS_CLUSTER_ID`, the scheduler cluster ID, or the
  node hostname. No cluster name is hardcoded.
- `/etc/nydus/nydusd-config.json` uses the `registry` backend. Without
  `NYDUS_MIRROR_URL` it keeps the object-form dfdaemon proxy at
  `http://127.0.0.1:4001` with `fallback: true` and `use_http: false`. With
  `NYDUS_MIRROR_URL` set, that proxy block is omitted and nydusd fetches blobs
  directly from the mirror host selected by the snapshotter. The `blobcache`
  device cache stays enabled, and `fs_prefetch` is on (`threads_count: 8`,
  `merging_size: 1 MiB`, `prefetch_all: true`).
  `rafs.prefetch`/`rafs.readahead` are intentionally not emitted: the pinned
  nydusd schema does not document those keys, and unknown fields can prevent
  nydusd from starting.

### fscache kernel requirements (Ubuntu 24.04 HWE 6.8+)

The default `fscache` driver requires:

- `CONFIG_EROFS_FS_ONDEMAND=y` and `CONFIG_CACHEFILES_ONDEMAND=y` in the
  running kernel config (`/boot/config-$(uname -r)`, or `/proc/config.gz`).
  `p2p-install.sh` verifies both and falls back to `fusedev` with a clear
  error when either is missing (Ubuntu 24.04 HWE 6.8/6.12 kernels).
- The `erofs`, `fscache`, and `cachefiles` modules. The installer loads them
  with `modprobe`, writes `/etc/modules-load.d/nydus.conf` with `erofs` and
  `cachefiles` for the next boot, and verifies `/dev/cachefiles` appears.
- Exclusive ownership of `/dev/cachefiles`. The installer never starts
  `cachefilesd`; if the unit exists it runs `systemctl stop`,
  `systemctl disable`, and `systemctl mask cachefilesd.service`, because
  cachefilesd monopolizes the device that nydusd fscache must own.

Any missing prerequisite logs an error and falls back to `fusedev` with the
legacy nydusd config, rather than starting a snapshotter that cannot mount
images. Set `nydus.fsDriver=fusedev` to skip the fscache probe entirely.
`SKIP_KERNEL_CHECK=1` bypasses only the kernel config-file check and is meant
for non-Ubuntu kernels where the on-demand options are known to be built in.

Images must be converted to RAFS v6 for EROFS/fscache. The registry-proxy
nydusify conversion sets `rafs_version: 6` (proxy PR); this chart only
validates and reports the prerequisite and contains no conversion code.

### Nydus mirror config

The mirror layout applies to the `fusedev` driver only. In the default
`fscache` mode the installer keeps the local dfdaemon proxy in the nydusd
bootstrap and logs a warning if `NYDUS_MIRROR_URL` is set; the fscache
`config.toml` does not include `[remote.mirrors_config]`.

When `NYDUS_MIRROR_URL` is set with `fusedev`, the installer writes:

```text
/etc/nydus/mirrors/docker.io/hosts.toml
/etc/nydus/mirrors/index.docker.io/hosts.toml
/etc/nydus/mirrors/_default/hosts.toml
```

Each file contains only the public mirror URL (the URL includes the registry
path, for example `/docker.io`) and no credentials:

```toml
server = "https://proxy.example.test/docker.io"

[host."https://proxy.example.test/docker.io"]
```

The `server` line documents the default server; the `[host."<url>"]` entry is
the containerd-compatible mirror table that nydus-snapshotter reads. It also
adds this to `/etc/nydus/config.toml`:

```toml
[remote.mirrors_config]
dir = "/etc/nydus/mirrors"
```

The snapshotter selects the first matching mirror and rewrites nydusd's backend
host before each mount. The fork release preserves the mirror URL path, so
`https://<proxy>/docker.io` becomes the blob source
`https://<proxy>/docker.io/v2/...`; this is why the installer no longer routes
nydus blobs through dfdaemon in this mode. Dragonfly and the containerd mirror
are not changed.

If manager/scheduler preflight fails, or dfdaemon/nydus download or startup
fails, the script writes an overlayfs-only direct-registry configuration and
exits success so RKE2 can start. On containerd 2.x it removes the nydus
drop-in; on 1.x it writes an overlayfs template. It does not enable the
`127.0.0.1:4001` nydus proxy in that path. Existing certificate/config files
are left in place for a later retry.

### Nydus on Harbor: confirmed settings

Confirmed on poligon (live). Workloads use direct Harbor references of the form
`harbor.../mirror/<registry>/<repo>:<tag>`; the node-wide nydus snapshotter
resolves them, so no `runtimeClass` is needed.

Two switches are required:

- containerd 2.x drop-in: `use_local_image_pull = false`. RKE2's default `true`
  bypasses the standard CRI unpack path; the nydus snapshotter needs CRI labels
  (`cri.image-ref`, `cri.manifest-digest`) and layer annotations to detect nydus
  meta/index, and detection silently fails without them.
- nydusd dfdaemon proxy branch: `"use_http": true`. dfdaemon MITMs CONNECT with a
  self-signed cert (`CN=rcgen`), nydusd fails TLS verification and falls back to
  direct Harbor; plain HTTP to `127.0.0.1:4001` avoids the MITM.

Measured effect (cold python start, then warm):

| path                | cold   | warm   |
|---------------------|--------|--------|
| direct (no dfdaemon)| 11.2 s | 11.4 s |
| via dfdaemon        | 5.0 s  | 2.9 s  |

Caveat: cross-node P2P for RAFS and zran is not yet confirmed.

## Harbor admin Secret

Harbor is deployed by the operator, not by this chart. Before enabling
`p2p.dragonfly.io/harbor`, put a Secret in the local/operator namespace
(default name `harbor-admin`) with the `HARBOR_ADMIN_PASSWORD` key. The
operator copies it per cluster and Fleet delivers it to Harbor as
`existingSecretAdminPassword`; values must never be placed in chart values or
Git.

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

`p2p-install.sh` installs the patched `v0.16.0-p2p.2` snapshotter from
`gorizond/nydus-snapshotter` to `/usr/local/bin/containerd-nydus-grpc`. The
release asset is `nydus-snapshotter-linux-amd64` plus its `.sha256`; the script
downloads it with `curl -fL`, verifies the checksum, installs it atomically
with mode `0755`, and falls back to the direct registry when the asset is
unavailable. The patch is amd64-only.

The patched snapshotter reads containerd's `certs.d/<host>/hosts.toml` (RKE2
path `/var/lib/rancher/rke2/agent/etc/containerd/certs.d`, then
`/etc/containerd/certs.d`). With the current seed architecture containerd has
only `_default` -> dfdaemon; dfdaemon 1.5.14 infers the upstream registry from
containerd's `?ns=` and the proxy receives the direct-ref path. Without a
certs.d override it keeps the previous `docker.io` ->
`registry-1.docker.io` behavior. No MITM proxy or local CA is installed. Index
detection (`enable_index_detect`) is unchanged; the mirror is selected by the
snapshotter before nydusd starts, not by a local proxy.

### Registry auth for the nydus path

`p2p-install.sh` writes only public mirror URLs and never writes registry
credentials. The supported modes are:

- **Network-only (default)**: the registry proxy is reachable from the node
  network (NetworkPolicy/TLS) and requires no client auth. This is the
  recommended mode for the proxy; no `/root/.docker/config.json` is needed.
- **Node keychain**: if the proxy or Harbor requires auth, provision a
  pull-only robot and place it in `/root/.docker/config.json` (mode `0600`).
  The patched snapshotter/nydusd reads that keychain; do not add credentials
  to `/etc/nydus` or to the bootstrap payload.
- **Pod image pull**: pods whose images are rewritten to the proxy use a
  namespace `imagePullSecret`/ServiceAccount with the pull-only credentials.
  The snapshotter path is host-level and does not read pod secrets.

Never print or commit robot secrets; the installer writes and the rendered
chart contains only the public mirror URL.

The other daemon binaries (`nydusd`, `nydus-image`, `nydus-overlayfs`) stay
pinned in the static store at `NYDUS_VERSION` (`v0.16.0`). The updater prefers
`${STATIC_URL}/nydus/${NYDUS_VERSION}/<binary>` and falls back to
`${STATIC_URL}/bin/<binary>` for the legacy unversioned layout.

`nydus-updater.service` runs `nydus-updater` from the daily
`nydus-updater.timer`. The updater re-fetches those static assets, compares
them with the installed binaries, installs changed files atomically, and
restarts `nydus-snapshotter-bootstrap.service` when anything changed. It does
not manage the patched `containerd-nydus-grpc`; that binary is updated by
rerunning `p2p-install.sh`. It never talks to Kubernetes.

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
asset for the snapshotter download path; it asserts the env payload decode,
certificate files/modes, `dfget.yaml`, `dynconfig.yaml`, scheduler ID, the
default fscache `/etc/nydus/config.toml` shape and the exact
`nydusd-config.fscache.json` render (including dynamic `id`/`domain_id`),
the `fusedev` fallback config and unit path, `modprobe` calls for
`erofs`/`fscache`/`cachefiles` plus the module-failure fallback, the
registry-backend `nydusd-config.json`, the fusedev nydus mirror layout and
proxy-free backend, the global nydus drop-in (containerd
2.x) and global template (containerd 1.x), the host snapshotter unit and
release URL/checksum, the updater script/units, the fail-open direct-registry
path, and absence of certificate values in logs. `shellcheck` runs when
installed. `scripts/test-p2p-static.sh` runs `bash -n`, `shellcheck` when
available, the self-test, and a `helm template` render check.

## Pilot gaps

- The fscache path is unit- and render-tested only. A live poligon VM must
  confirm `CONFIG_EROFS_FS_ONDEMAND=y`/`CONFIG_CACHEFILES_ONDEMAND=y` on the
  running kernel, `/dev/cachefiles` creation, cachefilesd disable/mask, and
  the pinned snapshotter's support for `threads_number`/
  `enable_kubeconfig_keychain` before rollout.
- Base-template merge and drop-in import behaviour need a live pilot, including
  `containerd config dump` after a manual `rke2-agent`/`rke2-server` restart.
- The static `/bin` (or `/nydus/<version>`) volume must be populated externally
  with trusted `v0.16.0` daemon assets; the script checks executability but not
  signatures. The patched snapshotter release is checksum-verified.
- The patched `containerd-nydus-grpc` certs.d behavior and the host-unit start
  order need pilot verification.
- The `v0.16.0-p2p.2` release must preserve the mirror URL path in
  `splitMirrorURL`; verify that nydusd fetches
  `<mirror>/docker.io/v2/...` and that a non-matching registry still falls back
  to its origin host.
- nydus tarfs is intentionally disabled; enabling it later requires a kernel
  with tarfs support (>= 6.4) and a matching nydusd.
- The updater restarts the host snapshotter after a static binary change; the
  pilot must confirm that the short socket outage does not disturb running
  pods, and that the static asset layout matches the deployed p2p-static
  volume.
- The bootstrap `dfget.yaml` intentionally has no `manager.addr`; it uses a
  local `dynconfig.yaml` with `scheduler.addr: <SCHEDULER_HOST>:443` so the
  node uses the public Traefik front. The exact dfdaemon dynconfig reload needs
  pilot verification.
- The static URL must be reachable from new nodes before CNI.
