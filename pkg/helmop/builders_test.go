package helmop

import (
	"strings"
	"testing"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

func TestKeyDBHelmOp(t *testing.T) {
	op := KeyDB(testCluster())

	if op.Name != "c1-keydb" || op.Namespace != "fleet-default" {
		t.Errorf("name/namespace = %s/%s", op.Name, op.Namespace)
	}
	if op.Labels["p2p-dragonfly-io"] != "keydb" {
		t.Errorf("component label = %q", op.Labels["p2p-dragonfly-io"])
	}
	if len(op.Spec.DependsOn) != 0 {
		t.Errorf("dependsOn = %+v, want none", op.Spec.DependsOn)
	}
	if len(op.Spec.DownstreamResources) != 0 {
		t.Errorf("downstreamResources = %+v, want none", op.Spec.DownstreamResources)
	}
	if op.Spec.Helm.Repo != KeyDBChartRepo || op.Spec.Helm.Chart != KeyDBChartName || op.Spec.Helm.Version != KeyDBChartVersion {
		t.Errorf("chart = %s/%s %s", op.Spec.Helm.Repo, op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}
	if KeyDBChartRepo != "https://enapter.github.io/charts/" || KeyDBChartVersion != "0.48.0" {
		t.Errorf("keydb chart = %s %s, want enapter 0.48.0", KeyDBChartRepo, KeyDBChartVersion)
	}

	values := op.Spec.Helm.Values.Data
	for key, want := range map[string]any{
		"fullnameOverride": "c1-keydb",
		"nodes":            2,
		"multiMaster":      "yes",
		"activeReplicas":   "yes",
		"threads":          2,
		"protectedMode":    "no",
		"appendonly":       "no",
		"imageRepository":  "eqalpha/keydb",
		"imageTag":         "x86_64_v6.3.2",
	} {
		if got := values[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	if got := valueAt(t, values, "persistentVolume", "enabled"); got != false {
		t.Errorf("persistentVolume.enabled = %v, want false", got)
	}
	for _, legacy := range []string{"architecture", "replica", "master"} {
		if containsKey(values, legacy) {
			t.Errorf("legacy key %q is present", legacy)
		}
	}
}

func TestNydusHelmOp(t *testing.T) {
	c := testCluster()
	op := Nydus(c)

	if op.Name != "c1-nydus-snapshotter" {
		t.Errorf("name = %q", op.Name)
	}
	if len(op.Spec.DependsOn) != 1 || op.Spec.DependsOn[0].Name != "c1-keydb" {
		t.Errorf("dependsOn = %+v", op.Spec.DependsOn)
	}
	if op.Spec.Helm.Repo != c.NydusChart.Repo || op.Spec.Helm.Chart != c.NydusChart.Name || op.Spec.Helm.Version != c.NydusChart.Version {
		t.Errorf("chart = %s/%s %s", op.Spec.Helm.Repo, op.Spec.Helm.Chart, op.Spec.Helm.Version)
	}
	if NydusChartRepo != "https://gorizond.github.io/d7y-cattle-operator" || NydusChartName != "nydus-snapshotter" || NydusChartVersion != "0.1.0" {
		t.Errorf("nydus chart defaults = %s/%s %s", NydusChartRepo, NydusChartName, NydusChartVersion)
	}
	if NydusHostSocket != "/run/k3s/containerd/containerd.sock" {
		t.Errorf("NydusHostSocket = %q, want the RKE2 containerd socket", NydusHostSocket)
	}

	values := op.Spec.Helm.Values.Data
	for path, want := range map[string]any{
		"snapshotterMode":       NydusSnapshotterModePerRuntime,
		"hostSocket":            NydusHostSocket,
		"priorityClassName":     "system-node-critical",
		"containerd.manage":     true,
		"containerd.configDir":  NydusContainerdConfigDir,
		"containerd.stateDir":   NydusContainerdStateDir,
		"containerd.dropInDir":  NydusContainerdDropInDir,
		"containerd.dropInFile": NydusContainerdDropInFile,
		"runtimeClass.create":   true,
		"runtimeClass.name":     NydusRuntimeClassName,
	} {
		keys := strings.Split(path, ".")
		if got := valueAt(t, values, keys...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if containsKey(values, "enable_oci_tar") {
		t.Error("values contain the non-existent enable_oci_tar key")
	}
}

func TestNydusGlobalMode(t *testing.T) {
	c := testCluster()
	c.NydusSnapshotterMode = NydusSnapshotterModeGlobal

	values := Nydus(c).Spec.Helm.Values.Data
	if got := values["snapshotterMode"]; got != NydusSnapshotterModeGlobal {
		t.Errorf("snapshotterMode = %v, want global", got)
	}
}

func TestNydusModeDefaultsToPerRuntime(t *testing.T) {
	c := testCluster()
	c.NydusSnapshotterMode = ""

	values := Nydus(c).Spec.Helm.Values.Data
	if got := values["snapshotterMode"]; got != NydusSnapshotterModePerRuntime {
		t.Errorf("snapshotterMode = %v, want %v", got, NydusSnapshotterModePerRuntime)
	}
}

func TestBuildersUseConfiguredChartVersion(t *testing.T) {
	c := testCluster()
	c.Chart.Version = "9.9.9"
	c.ExtrasChart.Version = "8.8.8"
	c.NydusChart.Version = "7.7.7"

	for name, op := range map[string]*fleetv1alpha1.HelmOp{
		"manager": Manager(c),
		"control": Control(c),
		"data":    Data(c),
	} {
		if got := op.Spec.Helm.Version; got != "9.9.9" {
			t.Errorf("%s chart version = %q, want 9.9.9", name, got)
		}
	}
	if got := Extras(c, ExtrasOptions{}).Spec.Helm.Version; got != "8.8.8" {
		t.Errorf("extras chart version = %q, want 8.8.8", got)
	}
	if got := Nydus(c).Spec.Helm.Version; got != "7.7.7" {
		t.Errorf("nydus chart version = %q, want 7.7.7", got)
	}
}

func TestVerifiedDragonflyChartVersion(t *testing.T) {
	if VerifiedDragonflyChartVersion != "1.10.1-patch.2" {
		t.Errorf("VerifiedDragonflyChartVersion = %q, want 1.10.1-patch.2", VerifiedDragonflyChartVersion)
	}
}

func TestHelmOpsHaveNoPersistence(t *testing.T) {
	c := testCluster()
	ops := map[string]*fleetv1alpha1.HelmOp{
		"keydb":   KeyDB(c),
		"manager": Manager(c),
		"control": Control(c),
		"data":    Data(c),
		"nydus":   Nydus(c),
		"extras":  Extras(c, ExtrasOptions{IngressEnabled: true, VPAEnabled: true, Manager: true, Seed: true}),
	}
	for name, op := range ops {
		if containsKey(op.Spec.Helm.Values.Data, "persistentVolumeClaim") {
			t.Errorf("%s HelmOp values contain persistentVolumeClaim", name)
		}
		if enabled := truePersistence(op.Spec.Helm.Values.Data); len(enabled) > 0 {
			t.Errorf("%s HelmOp enables persistence at %v", name, enabled)
		}
	}
}

func containsKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if _, ok := typed[key]; ok {
			return true
		}
		for _, child := range typed {
			if containsKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsKey(child, key) {
				return true
			}
		}
	}
	return false
}

func truePersistence(value any) []string {
	var paths []string
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "persistence" || key == "persistentVolume" {
				if settings, ok := child.(map[string]any); ok {
					if enabled, ok := settings["enabled"].(bool); ok && enabled {
						paths = append(paths, key)
					}
					if enabled, ok := settings["enable"].(bool); ok && enabled {
						paths = append(paths, key)
					}
				}
			}
			paths = append(paths, truePersistence(child)...)
		}
	case []any:
		for _, child := range typed {
			paths = append(paths, truePersistence(child)...)
		}
	}
	return paths
}
