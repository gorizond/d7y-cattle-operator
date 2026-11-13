package helmop

import (
	"encoding/json"
	"testing"
)

func TestHelmOpSerializesPromotedFields(t *testing.T) {
	data, err := json.Marshal(Manager(testCluster()))
	if err != nil {
		t.Fatalf("marshal HelmOp: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal HelmOp: %v", err)
	}

	spec, ok := raw["spec"].(map[string]any)
	if !ok {
		t.Fatalf("spec = %T", raw["spec"])
	}
	if spec["namespace"] != TargetNamespace {
		t.Errorf("spec.namespace = %v", spec["namespace"])
	}
	if _, ok := spec["helm"].(map[string]any); !ok {
		t.Errorf("spec.helm = %T, want an object", spec["helm"])
	}
	if _, ok := spec["downstreamResources"].([]any); !ok {
		t.Errorf("spec.downstreamResources = %T, want an array", spec["downstreamResources"])
	}
	if _, ok := spec["dependsOn"].([]any); !ok {
		t.Errorf("spec.dependsOn = %T, want an array", spec["dependsOn"])
	}
	targets, ok := spec["targets"].([]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("spec.targets = %T %v", spec["targets"], spec["targets"])
	}
	target := targets[0].(map[string]any)
	if target["clusterName"] != "c1" {
		t.Errorf("target clusterName = %v", target["clusterName"])
	}

	helm := spec["helm"].(map[string]any)
	if helm["repo"] != "https://charts.example.com" || helm["chart"] != "dragonfly" || helm["version"] != "1.10.1" {
		t.Errorf("spec.helm = %v", helm)
	}
	if helm["releaseName"] != "c1-manager" {
		t.Errorf("releaseName = %v", helm["releaseName"])
	}
	values, ok := helm["values"].(map[string]any)
	if !ok {
		t.Fatalf("spec.helm.values = %T", helm["values"])
	}
	manager, ok := values["manager"].(map[string]any)
	if !ok || manager["enable"] != true {
		t.Errorf("values.manager = %v", values["manager"])
	}
}
