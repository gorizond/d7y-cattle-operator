# p2p-host-sync

Privileged per-seed DaemonSet that keeps host bootstrap inputs current without
waiting for a node reboot.

It writes from its ConfigMap to the host:

- `/etc/dragonfly/{dynconfig.yaml,scheduler-ips,certs/*}` (existing);
- `/etc/nydus/versions.env` and `/etc/nydus/updater.env` with the operator
  versions, repositories, and fallback checksums;
- `/usr/local/bin/nydus-updater` and `/usr/local/bin/nydus-fs-select`.

The DaemonSet runs with `hostPID: true` and, once per revision, triggers
`nydus-updater.service` through
`nsenter -t 1 -m -u -i -n -p -- systemctl start nydus-updater.service`.
`STATIC_URL` is rendered from operator config and the updater tries the
static store first: versioned client binaries, versioned/compat nydus and
snapshotter tarballs; it falls back to GitHub when `STATIC_URL` is empty or an
artifact is absent. It fetches release checksum sidecars before installing,
updates the client (`dfdaemon`, `dfget`), nydus binaries, and upstream
`containerd-nydus-grpc`, and restarts the snapshotter only when a binary or the
fs driver changed. It restarts `dfdaemon-bootstrap` only when client binaries
changed and that service is active. Host-sync never restarts the snapshotter or
dfdaemon itself.

Values (normally rendered by the operator):

```yaml
staticURL: https://static.example.com
versions: {client: v1.5.14, nydus: v2.4.5, snapshotter: v0.16.0}
repos:
  client: dragonflyoss/client
  nydus: dragonflyoss/nydus
  snapshotter: containerd/nydus-snapshotter
checksums:  # fallback pins; release sidecars are preferred
  client: {amd64: "...", arm64: "..."}
  nydus: {amd64: "...", arm64: "..."}
  snapshotter: {amd64: "...", arm64: "..."}
  snapshotterBinary: {amd64: "...", arm64: "..."}
```

Live convergence: versions.env/updater.env, updater/selector binaries, and the
client/nydus/snapshotter binaries. A new node still needs the initial bootstrap
block (normal cloud-init/reboot); a later block re-run sources
`/etc/nydus/versions.env`.
