# p2p-static

Nginx static server for the Dragonfly P2P bootstrap flow. It serves
`/p2p-install.sh` and the binaries under `/bin/`. The chart does not ship
binaries and creates no PVC or Secret.

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
SCHEDULER_CLUSTER_ID=2
CA_CRT_B64=<standard base64 of ca.crt>
CLIENT_CRT_B64=<standard base64 of tls.crt>
CLIENT_KEY_B64=<standard base64 of tls.key>
```

The script decodes the outer URL-safe base64, parses `KEY=VALUE` lines with
`read`/`case` (no `eval`), and decodes the certificate fields with `base64 -d`.
There is no JSON, `jq`, or python dependency on the node. Certificate values
are never logged; `tls.key` is written with mode `0600`.

For local testing, set `P2P_ROOT=/tmp/root` and `SKIP_DOWNLOAD=1`; the script
will write its files below that root and skip network/systemd/module actions.

## Containerd design

The containerd template keeps **overlayfs as the global snapshotter** and adds
Nydus per runtime:

- containerd 1.7 / config v2: `[plugins."io.containerd.grpc.v1.cri".containerd]`
  has `snapshotter = "overlayfs"`, with a `runtimes.nydus` entry that has
  `snapshotter = "nydus"` and `SystemdCgroup = true`.
- containerd 2.0 / config v3: `[plugins.'io.containerd.cri.v1.images']` has
  `snapshotter = 'overlayfs'`, plus `runtime_platforms.nydus.snapshotter='nydus'`
  and a `runtimes.nydus` entry with `snapshotter='nydus'`.
- Both templates add `[proxy_plugins.nydus]` pointing at
  `/run/containerd-nydus/containerd-nydus-grpc.sock`.

Workloads opt in with `runtimeClass: nydus`; system pods stay on overlayfs.

**Do not set `machineGlobalConfig.snapshotter: nydus`.** RKE2 emits
`disable_snapshot_annotations = true` for that setting, which is wrong for
Nydus, and a missing Nydus socket would affect the whole node CRI instead of
only `runtimeClass: nydus` pods. There is intentionally no fallback guard.

The template starts with `{{ template "base" . }}`. If the base template already
defines the containerd table, the merged TOML must be verified on the pilot; the
2.0 `runtime_platforms` layout is source-derived and needs runtime validation.

## Containerd version detection

`p2p-install.sh` reads `containerd --version` or the RKE2 containerd binary and
defaults to the v1 template if neither is available. Set `CONTAINERD_MAJOR=2`
to force v3 on a node where RKE2/containerd is not installed yet; the bootstrap
payload currently has no field for this.

## Self-test

```bash
bash charts/p2p-static/files/test.sh
```

The test uses temporary roots and `SKIP_DOWNLOAD=1`; it asserts the env payload
decode, certificate files/modes, `dfget.yaml`, scheduler ID, nydus config,
per-runtime containerd tables, service ordering, and absence of certificate
values in logs. `shellcheck` is expected to pass for both scripts.

## Pilot gaps

- Base-template merge and the 2.0 `runtime_platforms` table need a live pilot.
- The static `/bin` volume must be populated externally with trusted binaries;
  the script checks executability but not checksums/signatures.
- `manager.addr` is written as `host:65003`; some dfdaemon versions may expect a
  scheme. The `manager.tls`/`scheduler.tls` schema and the exact
  `dfdaemon` / `containerd-nydus-grpc` flags need pilot verification.
- The script generates `nydusd-config.json`, not `/etc/nydus/config.toml`.
- The static URL must be reachable from new nodes before CNI.
