# p2p-static

Nginx static server for the Dragonfly P2P bootstrap flow. It serves
`/p2p-install.sh` and the static assets under `/bin/` (and optionally
`/nydus/<version>/`). The chart does not ship binaries and creates no PVC or
Secret. The bootstrap script installs the host-only nydus snapshotter and its
updater; there is no in-cluster nydus DaemonSet or HelmOp.

## Install

```bash
helm install p2p-static charts/p2p-static \
  --namespace dragonfly-system \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=static.example.com \
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
MANAGER_GRPC=https://manager-grpc.example.com:65003
STATIC_URL=http://rancher-static.example.com
SCHEDULER_HOST=d7y-scheduler.c-m-example.example.com
SCHEDULER_CLUSTER_ID=2
CA_CRT_B64=<standard base64 of ca.crt>
CLIENT_CRT_B64=<standard base64 of tls.crt>
CLIENT_KEY_B64=<standard base64 of tls.key>
```

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
  `containerd-nydus-grpc --config /etc/nydus/config.toml --nydusd /usr/local/bin/nydusd --nydusd-config /etc/nydus/nydusd-config.json`.
- `/etc/nydus/config.toml` uses `daemon_mode = "multiple"`,
  `fs_driver = "fusedev"`, and the snapshotter-level auto-detection options
  `enable_referrer_detect = true` and `enable_index_detect = true`; tarfs is
  not enabled. These two flags are nydus-snapshotter options (top-level
  `config.toml`), not nydusd JSON fields.
- `/etc/nydus/nydusd-config.json` uses the `registry` backend with the
  object-form proxy at `http://127.0.0.1:4001` and `fallback: true`.

If manager/scheduler preflight fails, or dfdaemon/nydus download or startup
fails, the script writes an overlayfs-only direct-registry configuration and
exits success so RKE2 can start. On containerd 2.x it removes the nydus
drop-in; on 1.x it writes an overlayfs template. It does not enable the
`127.0.0.1:4001` nydus proxy in that path. Existing certificate/config files
are left in place for a later retry.

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
`nydus-updater.timer`. The updater re-fetches the pinned assets, compares
them with the installed binaries, installs changed files atomically, and
restarts `nydus-snapshotter-bootstrap.service` only when something changed.
It never talks to Kubernetes.

## Containerd version detection

`p2p-install.sh` reads `containerd --version` or the RKE2 containerd binary and
defaults to the containerd 2.x v3 drop-in path when neither is available, which
is the normal pre-RKE2 case on a new VM. Set `CONTAINERD_MAJOR=1` only for older
nodes; the bootstrap payload currently has no field for this.

## Self-test

```bash
bash charts/p2p-static/files/test.sh
```

The test uses temporary roots and `SKIP_DOWNLOAD=1`; it asserts the env payload
decode, certificate files/modes, `dfget.yaml`, `dynconfig.yaml`, scheduler ID,
`/etc/nydus/config.toml` (multiple/fusedev, no tarfs), the registry-backend
`nydusd-config.json`, the global nydus drop-in (containerd 2.x) and global
template (containerd 1.x), host unit ordering/exec flags, the updater
script/units, the fail-open direct-registry path, and absence of certificate
values in logs. `scripts/test-p2p-static.sh` runs `bash -n`, `shellcheck` when
available, the self-test, and a `helm template` render check.

## Pilot gaps

- Base-template merge and drop-in import behaviour need a live pilot, including
  `containerd config dump` after a manual `rke2-agent`/`rke2-server` restart.
- The static `/bin` (or `/nydus/<version>`) volume must be populated externally
  with trusted `v0.16.0` assets; the script checks executability but not
  checksums/signatures.
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
