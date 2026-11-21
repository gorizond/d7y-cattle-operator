# Machine bootstrap

The operator embeds `assets/p2p-install.sh` and injects it, with the bootstrap
env-file assignments, into the Rancher node-driver machine bootstrap Secret.
The block is written to `/tmp/d7y-p2p-install.sh` with a POSIX heredoc and
executed as `bash /tmp/d7y-p2p-install.sh || true`, so a P2P failure cannot
prevent the surrounding Rancher install script from calling `do_install "$@"`.
The surrounding cloud-init shell is `dash`; the heredoc body is literal and is
not parsed by dash.

The same embedded script is the single source of truth for the host setup; the
`p2p-static` chart serves only the static `/bin` and `/nydus` assets.

The block carries the client identity's `ca-bundle.crt` (falling back to
`ca.crt` for legacy Secrets). The bootstrap Secret is annotated with
`p2p.dragonfly.io/client-cert-revision` and
`p2p.dragonfly.io/bootstrap-inputs` (client revision + scheduler IPs/ID +
distro); when either changes, the injected block is replaced in place (bounded
by the p2p start/end markers). A provisioning-Cluster watch re-enqueues only
that cluster's bootstrap Secrets when its scheduler annotations change.
Existing nodes only pick up the new block after their Rancher
bootstrap/cloud-init runs again, which in practice requires a reboot.

The block always carries `P2P_DISTRO` and the Cluster's
`p2p.dragonfly.io/scheduler-ips` annotation (seed clusters publish their own
public CAPI machine IPs; non-seed clusters aggregate other seeds). With an empty IP list the block is
still installed (binaries/configs/units), but `dfdaemon-bootstrap` is not
started and no direct-registry fallback is taken. With IPs, `dynconfig.yaml`
uses `scheduler.addrs` with `:8002` for each address and the host bootstrap is
started.

The injector does not wait for `p2p.dragonfly.io/scheduler-cluster-id`. When
the annotation is empty, the block is injected with the placeholder
`SCHEDULER_CLUSTER_ID=0` and the controller emits a
`SchedulerClusterIDPlaceholder` warning. This is the first-seed-node path: its
scheduler IPs are still empty, so the host dfdaemon does not start; later
reconciles keep the marker and are idempotent. Workers normally have the
annotation by the time their bootstrap Secret is rendered.

## P2P bootstrap asset delivery

The first delivery is deliberately direct. `p2p-install.sh` installs both
`dfdaemon` and `dfget` from the static store (`/bin/dfdaemon`, `/bin/dfget`) or
the pinned `dragonflyoss/client` `v1.5.14` tarball, verifying its SHA-256. Using
dfget for dfdaemon itself would be the chicken-and-egg case and is never done.

After that, if `SCHEDULER_IPS` is non-empty, `/usr/local/bin/dfget` exists,
`P2P_ASSETS` is `auto` (default) or `1`, and the normal preflight passed, the
installer starts `dfdaemon-bootstrap.service` before downloading nydus. It
waits up to `P2P_ASSETS_READY_TIMEOUT` seconds (default 60) for
`/var/run/dragonfly/dfdaemon.sock`; a timeout or start failure stops the
service and switches the remaining downloads to direct.
`dfget --scheduling-policy always --endpoint /var/run/dragonfly/dfdaemon.sock
--timeout 10m [--digest sha256:<sum>] <url> -O <tmp>` fetches the pinned nydus
tarball (`nydus-static-v2.4.5-linux-amd64.tgz`) and the patched snapshotter.
The nydus order is P2P, static-store tarball
(`/nydus/<version>/nydus-static.tgz`), GitHub release. The snapshotter order is
checksum fetch, P2P with digest, static store, GitHub release. Every nydus
tarball and snapshotter is SHA-256 verified; a mismatch is discarded and never
installed. A final direct checksum mismatch is fatal.

`P2P_ASSETS=0` disables the early dfdaemon/P2P path entirely; the variable is a
plain env entry, so it can be added to the bootstrap payload for rollback.
Concurrent boots are safe: each node downloads to its own temp directory and
installs atomically, while the scheduler/dfdaemon deduplicate P2P blobs; there
is no shared host lock. The host dfdaemon bootstrap switch still disables the
host daemon once the in-cluster client is running. The first seed normally has
no scheduler IPs and no peers and therefore stays direct.

The P2P data path is plaintext (dfdaemon upload), so SHA-256 is the integrity
guard. The installer's manager gRPC preflight remains a known avoidable
dependency for asset fetching: assets only need the scheduler, but the preflight
is intentionally unchanged. `nydus-snapshotter-bootstrap.service` gains
`After=dfdaemon-bootstrap.service` without `Wants`, so an early P2P dfdaemon is
ordered first without pulling it in on non-P2P paths. `nydus-updater` does not
use P2P yet: it runs after the cluster exists, where the host dfdaemon socket is
normally disabled by the switch. P2P updater support is deferred.

## Kernel prerequisites

- `CONFIG_EROFS_FS_ONDEMAND=y`
- `CONFIG_CACHEFILES_ONDEMAND=y`
- `modules-load.d/nydus.conf` loads the required modules
- the cachefiles filesystem must support `fs_version=6`
- stock kernels commonly report `CONFIG_EROFS_FS_ONDEMAND=n` and
  `CONFIG_CACHEFILES_ONDEMAND=n`; the embedded selector then uses fusedev
- a custom or signed kernel is required to exercise the fscache path

Do not hardcode a cluster name in nydusd ids; the script derives them from
the scheduler cluster ID or node hostname.
