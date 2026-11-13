package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The operator must not own Ingress or VPA resources; they are delivered by
// the dragonfly-extras HelmOp.
func TestOperatorRBACHasNoIngressOrVPA(t *testing.T) {
	data, err := os.ReadFile("../../charts/d7y-cattle-operator/templates/rbac.yaml")
	if err != nil {
		t.Fatalf("read RBAC template: %v", err)
	}
	for _, forbidden := range []string{
		"networking.k8s.io",
		"autoscaling.k8s.io",
		"ingresses",
		"verticalpodautoscalers",
	} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("operator RBAC must not mention %q", forbidden)
		}
	}
}

func TestOperatorChartHasNoIngressOrVPA(t *testing.T) {
	files, err := filepath.Glob("../../charts/d7y-cattle-operator/templates/*.yaml")
	if err != nil {
		t.Fatalf("list chart templates: %v", err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, kind := range []string{"kind: Ingress", "kind: VerticalPodAutoscaler"} {
			if strings.Contains(string(data), kind) {
				t.Errorf("%s must not render %q", file, kind)
			}
		}
	}
}
