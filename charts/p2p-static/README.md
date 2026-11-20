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

### Manager-host route

The chart can serve the bootstrap script and its static assets on the manager
host, using the manager's TLS Secret:

```bash
helm install p2p-static charts/p2p-static \
  --namespace dragonfly-system \
  --set manager.enabled=true \
  --set manager.host=manager.example.com \
  --set manager.tlsSecret=manager-tls
```

`manager.entryPoint` defaults to `websecure`; the IngressRoute uses
`ingress.className` (default `traefik`). The route matches
`Host("<manager-host>")` plus `PathPrefix("/p2p-install.sh")`,
`PathPrefix("/bin")`, or `PathPrefix("/nydus")`, and forwards to the
`p2p-static` Service. The manager REST API keeps its own `/api` paths; these
prefixes do not overlap.

The machine-config webhook uses `nginx.staticURL` for the bootstrap line. Set it
to the same manager host (for example `https://manager.example.com`) and the
CloudBlast `postBootScript` line becomes:

```bash
curl -sfL https://<manager-host>/p2p-install.sh?cfg=<base64url> | bash
```

The payload already contains the manager, static, and scheduler values plus the
base64 client certificate and key. The same route serves `/bin` and `/nydus`,
so the script can use `STATIC_URL=https://<manager-host>` for its static
binaries. No secret values are rendered by the chart.

The chart value `nydus.clusterId` is rendered into `p2p-install.sh`:

```bash
helm install p2p-static charts/p2p-static \
  --set nydus.clusterId=example-node  # nydusd id/domain_id; default empty
```

`nydus.clusterId` is optional: when empty the installer uses the payload
scheduler cluster ID and then the node hostname.

The filesystem driver is not a chart value. `p2p-install.sh` installs both
nydusd configs and `/usr/local/bin/nydus-fs-select`; the selector runs before
every snapshotter start and chooses fscache or fusedev from the running kernel
and the fscache prerequisites. Stock kernels therefore get fusedev without a
chart override, and a later kernel upgrade can enable fscache without
rerunning the installer.

The Ingress is optional and does not manage TLS or DNS.

## Bootstrap payload

`p2p-install.sh` expects `?cfg=<base64url>` where the decoded value is an
env-file, not JSON:

```text
MANAGER_GRPC=https://manager-grpc.example.test:443
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

## RKE2 and k3s

`p2p-install.sh` detects the node distro from `/var/lib/rancher/k3s`:
RKE2 uses `/var/lib/rancher/rke2/...` and orders before
`rke2-server.service rke2-agent.service`; k3s uses
`/var/lib/rancher/k3s/...`, its `crictl`, and `k3s.service`. The containerd
socket is `/run/k3s/containerd/containerd.sock` on both distros. A missing
marker keeps the existing RKE2 behavior.

## Static binary fallback

The static store remains the primary source for `nydusd`, `nydus-image`, and
`nydus-overlayfs`. If both `${STATIC_URL}/nydus/<version>/<binary>` and
`${STATIC_URL}/bin/<binary>` return 404, the installer downloads the official
`dragonflyoss/nydus` `nydus-static-<version>-linux-amd64.tgz` release and
extracts the same three binaries. The fallback is enabled by default
(`NYDUS_ALLOW_GITHUB=1`) and logs whether the static store or GitHub was used.

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
  (`Before=`), runs `ExecStartPre=/usr/local/bin/nydus-fs-select`, and uses
  `containerd-nydus-grpc --config /etc/nydus/config.toml --nydusd /usr/local/bin/nydusd`.
  The nydusd config path comes from `/etc/nydus/config.toml`, so the selector
  can change it between starts without editing the unit.
- `/usr/local/bin/nydus-fs-select` writes `/etc/nydus/config.toml`
  idempotently. When the running kernel has `CONFIG_EROFS_FS_ONDEMAND=y` and
  `CONFIG_CACHEFILES_ONDEMAND=y`, the modules load, `/dev/cachefiles` exists,
  and cachefilesd does not own the device, it selects the fscache shape:
  `daemon_mode = "shared"`, `nydusd_config =
  "/etc/nydus/nydusd-config.fscache.json"`, `fs_driver = "fscache"`,
  `threads_number = 8`, `[remote] skip_ssl_verify = false`, and
  `[remote.auth] enable_kubeconfig_keychain = true`. Otherwise it selects
  fusedev: `daemon_mode = "multiple"`, `fs_driver = "fusedev"`,
  `[experimental] enable_referrer_detect = true`/`enable_index_detect = true`,
  and the legacy `/etc/nydus/nydusd-config.json`. Tarfs is never enabled.
  The chosen mode and reason are logged; no secrets are read or logged.
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

### fscache kernel requirements (custom or signed kernel)

Stock Ubuntu 24.04 GA/HWE kernels ship both required options as `n`
(`CONFIG_EROFS_FS_ONDEMAND=n`, `CONFIG_CACHEFILES_ONDEMAND=n`); 6.12 is not a
noble HWE kernel. The selector probes the running kernel at every snapshotter
start and requires:

- The literal `CONFIG_EROFS_FS_ONDEMAND=y` and
  `CONFIG_CACHEFILES_ONDEMAND=y` in the running kernel config
  (`/boot/config-$(uname -r)`, or `/proc/config.gz`). `=n`, `=m`, and unset
  select fusedev. A stock kernel therefore works out of the box on fusedev.
- The `erofs`, `fscache`, and `cachefiles` modules loaded with `modprobe`, and
  `/dev/cachefiles` present. A present `/dev/cachefiles` only proves
  `CONFIG_CACHEFILES` is loaded; it does not prove on-demand support, so the
  CONFIG check is independent and runs first. `p2p-install.sh` also writes
  `/etc/modules-load.d/nydus.conf` (`erofs`, `cachefiles`) for the next boot.
- Exclusive ownership of `/dev/cachefiles`. `cachefilesd` must be absent or
  stopped, disabled, masked, and inactive: the selector stops/disables/masks
  the unit when present and verifies it is inactive. If it is still active,
  masking fails, or a `cachefilesd` process is running, the selector chooses
  fusedev instead of racing it for the device.

Any missing prerequisite selects fusedev with the legacy nydusd config and a
logged reason, rather than starting a snapshotter that cannot mount images.
`nydus-updater` reruns the selector before deciding whether to restart the
unit, and the unit's `ExecStartPre` reruns it on every start, so a kernel or
module change is picked up without rerunning `p2p-install.sh`.

Images must be RAFS v6 for EROFS/fscache. The nydusify
conversion must pass `--fs-version 6` (the proxy PR wires `fs_version=6`);
this chart only validates and reports the prerequisite and contains no
conversion code.

### Nydus mirror config

The mirror layout applies only when the selector chooses `fusedev`. The
installer always writes the mirror assets for a configured `NYDUS_MIRROR_URL`
and records `/etc/nydus/mirrors.enabled`; if fscache is selected, the fscache
`config.toml` omits `[remote.mirrors_config]`, keeps the local dfdaemon proxy
in the nydusd bootstrap, and the selector logs a warning.

When `NYDUS_MIRROR_URL` is set, the installer writes:

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
the containerd-compatible mirror table that nydus-snapshotter reads. When the
selector chooses fusedev, it also adds this to `/etc/nydus/config.toml`:

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

Confirmed on a live RKE2 cluster. Workloads use direct Harbor references of the form
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
pinned in the static store at `NYDUS_VERSION` (`v2.4.5`). The updater prefers
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
asset for the snapshotter download path. It asserts the env payload decode,
certificate files/modes, `dfget.yaml`, `dynconfig.yaml`, scheduler ID, the
exact selector-installed fscache and fusedev `/etc/nydus/config.toml` shapes,
the selector matrix (`=n`, `=m`, missing config, both `=y` plus device and no
cachefilesd, active cachefilesd, missing device, missing fscache config), the
exact `nydusd-config.fscache.json` render (including dynamic
`id`/`domain_id`), both installed nydusd configs, `ExecStartPre` and the
`nydus-updater` selector call, the fusedev nydus mirror layout and proxy-free
backend, the fscache mirror warning, the global nydus drop-in (containerd 2.x)
and global template (containerd 1.x), release URL/checksum, the fail-open
direct-registry path, and absence of certificate values in logs or hardcoded
hosts in the selector. `shellcheck` runs when installed.
`scripts/test-p2p-static.sh` runs `bash -n`, `shellcheck` when available, the
self-test, and a `helm template` render check.

## Pilot gaps

- The fscache path is unit- and render-tested only. Stock Ubuntu 24.04
  GA/HWE kernels report both CONFIG options as `n`, so the selector must choose
  fusedev there. A live demo VM with a custom or signed kernel must confirm
  both are `=y`, `/dev/cachefiles` is created, cachefilesd is
  absent/stopped/disabled/masked, `ExecStartPre` rewrites `/etc/nydus/config.toml`
  to the fscache shape, and the pinned snapshotter supports
  `threads_number`/`enable_kubeconfig_keychain` before rollout.
- Base-template merge and drop-in import behaviour need a live pilot, including
  `containerd config dump` after a manual `rke2-agent`/`rke2-server` restart.
- The static `/bin` (or `/nydus/<version>`) volume must be populated externally
  with trusted `v2.4.5` daemon assets; the script checks executability but not
  signatures. The patched snapshotter release is checksum-verified.
- The patched `containerd-nydus-grpc` certs.d behavior and the host-unit start
  order need pilot verification.
- The `v0.16.0-p2p.2` release must preserve the mirror URL path in
  `splitMirrorURL`; verify that nydusd fetches
  `<mirror>/docker.io/v2/...` and that a non-matching registry still falls back
  to its origin host.
- nydus tarfs is intentionally disabled; enabling it later requires a kernel
  with tarfs support (>= 6.4) and a matching nydusd.
- The updater reruns `nydus-fs-select` and restarts the host snapshotter only
  when the binaries or the driver selection changed; the pilot must confirm
  that the short socket outage does not disturb running pods, and that the
  static asset layout matches the deployed p2p-static volume.
- The bootstrap `dfget.yaml` intentionally has no `manager.addr`; it uses a
  local `dynconfig.yaml` with `scheduler.addr: <SCHEDULER_HOST>:443` so the
  node uses the public Traefik front. The exact dfdaemon dynconfig reload needs
  pilot verification.
- The static URL must be reachable from new nodes before CNI.
