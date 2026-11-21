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

The block always carries `P2P_DISTRO` and the Cluster's
`p2p.dragonfly.io/scheduler-ips` annotation (computed from other seed
clusters' `p2p.dragonfly.io/scheduler-public-ip` annotations). With an empty IP list the block is
still installed (binaries/configs/units), but `dfdaemon-bootstrap` is not
started and no direct-registry fallback is taken. With IPs, `dynconfig.yaml`
uses `scheduler.addrs` with `:443` for each address and the host bootstrap is
started.

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
