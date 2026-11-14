package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

// Nydus returns the nydus-snapshotter HelmOp for RKE2 nodes. It is disabled by
// default (NYDUS_ENABLED=false) until a containerd-2.x-compatible path exists;
// chart 0.0.10 targets containerd 1.x. Do not add enable_oci_tar or
// snapshotter.config.daemon: chart 0.0.10 has no such values.
//
// Known pilot risk: chart 0.0.10 does not expose hostSocket, initContainer
// command/privileged, or runtimeClass values, so it ignores those settings and
// hardcodes its own containerd socket/init logic. The RKE2 socket path and the
// RuntimeClass therefore require a chart patch or a pilot-specific override
// before this role is exercised on a real cluster.
func Nydus(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"fullnameOverride":  NydusName(c.Name),
		"hostNetwork":       true,
		"hostPid":           true,
		"hostSocket":        "/run/k3s/containerd/containerd.sock",
		"priorityClassName": "system-node-critical",
		"containerRuntime": map[string]any{
			"containerd": map[string]any{
				"configFile": "/var/lib/rancher/rke2/agent/etc/containerd/config.toml.tmpl",
			},
		},
		"initContainer": map[string]any{
			"privileged": true,
			"command": []any{
				"nsenter", "--mount=/proc/1/ns/mnt", "--",
				"systemctl", "disable", "--now", "dfdaemon-bootstrap", "nydus-snapshotter-bootstrap",
			},
		},
		"runtimeClass": "nydus",
	}
	chart := Chart{Repo: NydusChartRepo, Name: NydusChartName, Version: NydusChartVersion}
	return newHelmOp(NydusName(c.Name), c, chart, "nydus", values, []string{KeyDBName(c.Name)})
}
