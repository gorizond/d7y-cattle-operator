# p2p-host-sync

Chart 0.1.5. Privileged per-seed DaemonSet that keeps host bootstrap inputs
current without waiting for a node reboot. It mounts `/etc/dragonfly`,
`/etc/nydus`, and `/usr/local/bin` from the host and runs with `hostPID`.

It writes from its ConfigMap to the host:

- `/etc/dragonfly/{dynconfig.yaml,scheduler-ips,certs/*}` (existing);
- `/etc/nydus/versions.env` and `/etc/nydus/updater.env` with the operator
  versions, repositories, and fallback checksums, including
  `NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64/ARM64` (verified v0.16.0 defaults:
  `9bd24308ef51…2ff0` / `399f7a5b80…3859`);
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

Values are required from the operator; this chart intentionally carries no
versions, repositories, or checksums of its own. `staticURL` may be empty only
when the static store is disabled, in which case the updater uses GitHub.

```yaml
staticURL: https://static.example.com
versions: {client: <operator>, nydus: <operator>, snapshotter: <operator>}
repos:
  client: <operator>
  nydus: <operator>
  snapshotter: <operator>
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
