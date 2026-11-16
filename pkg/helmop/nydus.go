package helmop

import fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"

const (
	// NydusHostSocket is the RKE2 node containerd socket used by the snapshotter
	// content API and the restart readiness gate.
	NydusHostSocket = "/run/k3s/containerd/containerd.sock"
	// NydusContainerdConfigDir is the RKE2 containerd config directory.
	NydusContainerdConfigDir = "/var/lib/rancher/rke2/agent/etc/containerd"
	// NydusContainerdStateDir is the RKE2 containerd state/content directory.
	NydusContainerdStateDir = "/var/lib/rancher/rke2/agent/containerd"
	// NydusContainerdDropInDir is imported by config-v3.toml.tmpl.
	NydusContainerdDropInDir = "/var/lib/rancher/rke2/agent/etc/containerd/config-v3.toml.d"
	// NydusContainerdDropInFile is the nydus drop-in file name.
	NydusContainerdDropInFile = "90-nydus.toml"
	// NydusRuntimeClassName is the perRuntime RuntimeClass handler.
	NydusRuntimeClassName = "nydus"
)

// Nydus returns the nydus-snapshotter HelmOp for RKE2 nodes. The custom chart
// targets containerd 2.2 and supports both snapshotter modes:
// perRuntime (overlayfs default, runtimeClass: nydus opt-in) and global.
// Creation stays gated by NYDUS_ENABLED.
func Nydus(c Cluster) *fleetv1alpha1.HelmOp {
	mode := c.NydusSnapshotterMode
	if mode == "" {
		mode = NydusSnapshotterModePerRuntime
	}
	values := map[string]any{
		"snapshotterMode":   mode,
		"hostSocket":        NydusHostSocket,
		"priorityClassName": "system-node-critical",
		"containerd": map[string]any{
			"manage":     true,
			"configDir":  NydusContainerdConfigDir,
			"stateDir":   NydusContainerdStateDir,
			"dropInDir":  NydusContainerdDropInDir,
			"dropInFile": NydusContainerdDropInFile,
		},
		"runtimeClass": map[string]any{
			"create": true,
			"name":   NydusRuntimeClassName,
		},
	}
	return newHelmOp(NydusName(c.Name), c, c.NydusChart, "nydus", values, []string{KeyDBName(c.Name)})
}
