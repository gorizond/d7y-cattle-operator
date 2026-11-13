package helmop

import "testing"

func TestReplicasOrOne(t *testing.T) {
	const annotation = "p2p.dragonfly.io/scheduler-replicas"
	template := ReplicasTemplate(annotation)

	if got := ReplicasOrOne(annotation, ""); got != template {
		t.Errorf("empty value = %q, want template", got)
	}
	if got := ReplicasOrOne(annotation, "2"); got != template {
		t.Errorf("valid value = %q, want template", got)
	}
	for _, value := range []string{"0", "-1", "abc", "1.5"} {
		if got := ReplicasOrOne(annotation, value); got != "1" {
			t.Errorf("ReplicasOrOne(%q) = %q, want 1", value, got)
		}
	}
}
