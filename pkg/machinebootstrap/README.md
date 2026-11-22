# Machine bootstrap

The operator injects the short p2p bootstrap command into the Rancher
node-driver machine bootstrap Secret:

```sh
# d7y-cattle-operator:p2p-install
set -a
P2P_DISTRO='...'
SCHEDULER_IPS='...'
MANAGER_GRPC='...'
... full BuildInstallEnv env lines ...
set +a
curl -sfL "<static>/p2p-install.sh" | bash || true
# d7y-cattle-operator:p2p-install-end
```

The env-file is embedded in the Secret, shell-quoted, and exported with
`set -a`/`set +a`; the piped `bash` reads the variables. The script itself is
served by the `p2p-static` chart at a plain `/p2p-install.sh` URL — there is no
payload in the URL or query. The Secret no longer carries the ~69.7 KiB
`p2p-install.sh` body. The `|| true` keeps the p2p step fail-open so a P2P
failure cannot prevent the surrounding Rancher install script from calling
`do_install "$@"`. The markers make the block idempotent and let the
pre-install block be inserted directly before it.

The `p2p-static` chart serves `/p2p-install.sh` (a byte-identical copy of
`pkg/machinebootstrap/assets/p2p-install.sh`) and the static `/bin`, `/nydus`,
`/client`, and `/snapshotter` assets. The endpoint sets `access_log off`.

The env includes `P2P_DISTRO`, `SCHEDULER_IPS`, `MANAGER_GRPC`, `STATIC_URL`,
`SMART_PROXY_URL`, `NYDUS_MIRROR_URL`, `SCHEDULER_HOST`,
`SCHEDULER_CLUSTER_ID`, `CLIENT_VERSION`, `NYDUS_VERSION`,
`NYDUS_SNAPSHOTTER_VERSION`, `CA_CRT_B64`, `CLIENT_CRT_B64`, and
`CLIENT_KEY_B64`. The client certificate **and key** stay in the Secret;
CA-only is insufficient for mTLS. The limit that matters is the effective
`user_data` the node driver sends: gzip+base64 of the Secret value. It stays
below the 32 KiB limit used by some providers; the raw Secret value can be
larger (about 38 KiB on a live RKE2 node). The separate Kubernetes Secret
object limit (1 MiB) is enforced by the operator's serialized-size guard.

The env carries the client identity's `ca-bundle.crt` (falling back to
`ca.crt` for legacy Secrets). The bootstrap Secret is annotated with
`p2p.dragonfly.io/client-cert-revision` and
`p2p.dragonfly.io/bootstrap-inputs` (client revision + scheduler IPs/ID +
distro); when either changes, the injected block is replaced in place (bounded
by the p2p start/end markers). A provisioning-Cluster watch re-enqueues only
that cluster's bootstrap Secrets when its scheduler annotations change.
Existing nodes only pick up a changed bootstrap block after their Rancher
bootstrap/cloud-init runs again, which in practice requires a reboot. Version
convergence is separate: host-sync updates `/etc/nydus/versions.env` live, and a
later block re-run reads it.

Existing v0.3.23 Secrets with the old inline heredoc block are replaced by the
short block on the next reconcile. Existing nodes only pick up the new block
after their Rancher bootstrap/cloud-init runs again (normally a reboot), as
with any other bootstrap change.

The env always carries `P2P_DISTRO` and the Cluster's
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

## Pre-install annotation

A provisioning Cluster can carry a per-cluster POSIX shell body in the
`d7y-operator-preinstall` annotation. It is rendered as a second marked block
(`# d7y-cattle-operator:preinstall` … `# d7y-cattle-operator:preinstall-end`)
immediately **before** the p2p block, so it runs before `p2p-install.sh` and
Dragonfly. The feature uses the same gate as the p2p block: label
`d7y-operator-cattle: "true"` and annotation `p2p.dragonfly.io/seed: "true"`.
An empty or whitespace-only annotation keeps the previous p2p-only behavior.

The body is copied literally into a quoted heredoc (`D7Y_PREINSTALL_EOF`, with
`_1`, `_2`, … suffixes when the body contains the delimiter) and executed by
`bash` in a subshell:

```sh
bash /tmp/d7y-preinstall.sh >/tmp/d7y-preinstall.log 2>&1 || d7y_preinstall_rc=$?
```

The subshell and the namespaced `d7y_preinstall_rc` keep the outer Rancher
`dash` bootstrap script untouched. The block is fail-open: a failing command
does not stop the bootstrap or Dragonfly. stdout/stderr are echoed with the
`[d7y-preinstall]` prefix and a final `[d7y-preinstall] exit=<code>` line; the
final log line is emitted even without a trailing newline.

Lifecycle and ordering: every reconcile removes the old pre-install block,
replaces the p2p block, then inserts the new pre-install block before the p2p
marker. The body is part of `p2p.dragonfly.io/bootstrap-inputs`, so a text edit
re-renders the Secret once, identical text is a no-op, and clearing the
annotation removes the block while the p2p block stays. Block markers created
by v0.3.21 are recognized without manual migration. Existing nodes pick up the
annotation only when their bootstrap/cloud-init runs again (normally a reboot);
the operator never restarts machines.

**Security:** the mTLS client key is in the machine-bootstrap Secret (and
therefore in node-driver `user_data`, which the provider can see), but it is **not**
in the HTTP URL or access logs. The `p2p-static` chart sets `access_log off` for
`/p2p-install.sh`; do not expose the Secret or its env in public artifacts.

Limits and safety: the annotation is limited to 256 KiB. The final Secret is
checked against the Kubernetes 1 MiB object limit by serializing a DeepCopy with
`encoding/json` (`data` is base64 in the API, so the raw value length is not a
safe guard). When the pre-install block would overflow, it is dropped, the p2p
block is kept, the value is never truncated, and a `PreinstallTooLarge` warning
event is emitted. NUL or a line equal to either pre-install marker is rejected
with `PreinstallInvalid`. Events never contain the annotation body.

**Never put secrets in this annotation.** Its value is visible in the Cluster
object and in every machine bootstrap Secret, and it grants root execution on
new nodes. Pass auth keys/tokens through a separate mechanism.

## P2P bootstrap asset delivery

The first delivery is deliberately direct. `p2p-install.sh` detects the node
architecture like RKE2/k3s: `uname -m` `x86_64`/`amd64` maps to the Rust
`x86_64` target and nydus `amd64`; `aarch64`/`arm64` maps to `aarch64` and
nydus `arm64`; `TARGET_ARCH` overrides detection. `armv7l`/`armhf`/`armv6l`
fail clearly because no client/nydus armv7 assets exist. It then installs both
`dfdaemon` and `dfget` from the static store
(`/client/<version>/<arch>/`, then legacy `/bin/`) or the pinned
`dragonflyoss/client` `v1.5.14` GNU tarball for the detected arch, verifying
its SHA-256. The amd64 GNU tarball is
`dragonfly-client-v1.5.14-x86_64-unknown-linux-gnu.tar.gz`, SHA-256
`ba080e62…b1f7`; its `dfdaemon` is the 42.1 MB GNU build (SHA-256
`e0d03d4759…bf85`). The arm64 GNU tarball is
`dragonfly-client-v1.5.14-aarch64-unknown-linux-gnu.tar.gz`, SHA-256
`370ff792…10af`. Both GNU and musl binaries report `1.5.14 (8887c145f)`, so the
artifact hash is the discriminator. Using
dfget for dfdaemon itself would be the chicken-and-egg case and is never done.

After that, if `SCHEDULER_IPS` is non-empty, `/usr/local/bin/dfget` exists,
`P2P_ASSETS` is `auto` (default) or `1`, and the normal preflight passed, the
installer starts `dfdaemon-bootstrap.service` before downloading nydus. It
waits up to `P2P_ASSETS_READY_TIMEOUT` seconds (default 60) for
`/var/run/dragonfly/dfdaemon.sock`; a timeout or start failure stops the
service and switches the remaining downloads to direct.
`dfget --scheduling-policy always --endpoint /var/run/dragonfly/dfdaemon.sock
--timeout 10m [--digest sha256:<sum>] <url> -O <tmp>` fetches the pinned nydus
tarball (`nydus-static-v2.4.5-linux-<arch>.tgz`) and the upstream
`containerd/nydus-snapshotter` `v0.16.0` tarball (extracts
`bin/containerd-nydus-grpc`).
The static-store and P2P URLs carry version+arch:
`/nydus/<version>/<arch>/nydus-static.tgz` and
`/snapshotter/<version>/<arch>/<asset>`. The nydus order is P2P, static-store
tarball, GitHub release; the snapshotter order is the release checksum
(sidecar first, pinned fallback), P2P with digest, static store, GitHub
release. Every nydus tarball and
snapshotter is SHA-256 verified; a mismatch is discarded and never installed.
A final direct checksum mismatch is fatal. Upstream v0.16.0 publishes amd64
and arm64 tarballs with `bin/containerd-nydus-grpc`; both the tarball and the
extracted binary are pinned per arch. The amd64 binary matches the 236-node
reference `9bd24308…`. Upstream v0.16.0 reads mirrors only from its optional
mirrors-config directory and does **not** implicitly consume containerd's
`certs.d`; that fork-only behavior is gone. Set `NYDUS_MIRROR_URL` to populate
`/etc/nydus/mirrors`, or rely on the origin registry/kubeconfig keychain.

The client, nydus, and snapshotter versions come from the bootstrap payload
(`CLIENT_VERSION`, `NYDUS_VERSION`, `NYDUS_SNAPSHOTTER_VERSION`; chart defaults
`v1.5.14`, `v2.4.5`, `v0.16.0`). The operator chart values are the single
source; the script keeps per-arch checksum pins only as a last-resort fallback
for those default versions and fails if a non-default version arrives without
its checksum. `P2P_ASSETS=0` disables the early dfdaemon/P2P path entirely;
the variable is a plain env entry, so it can be added to the bootstrap payload
for rollback.
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
normally disabled by the switch. It does use the static store first (versioned
then compat), then GitHub, and verifies release checksums and the snapshotter
binary pin. P2P updater support is deferred.

## Host-sync live updates

`p2p-host-sync` writes the operator-configured versions/repositories/checksum
fallbacks to `/etc/nydus/versions.env` and `/etc/nydus/updater.env`, installs
the current `/usr/local/bin/nydus-updater` and `nydus-fs-select`, and triggers
`nydus-updater.service` via hostPID/`nsenter` when the revision changes.
`nydus-updater` fetches release checksums first, updates
`dfdaemon`/`dfget`, nydus binaries, and upstream
`containerd-nydus-grpc`, then restarts the snapshotter only when a binary or
the fs driver changed and `dfdaemon-bootstrap` only when client binaries
changed and the service is active. `p2p-install.sh` sources
`/etc/nydus/versions.env` on a later block re-run, so a re-run uses the
live-converged versions. A new node still needs the bootstrap block (normal
cloud-init/reboot); existing nodes converge versions without reboot.

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
