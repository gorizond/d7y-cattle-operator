package cluster

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

func TestPKISecretLabelsValid(t *testing.T) {
	controller, _, _ := newTestController(t)
	installLegacyClient(t, controller, "c-m-a")
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{seedMachine("c1", "c1-0", "203.0.113.10")}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{{NodeName: "c1-node", HostIP: "203.0.113.10"}}}
	cluster := pkiSeed("c1", "c-m-a")
	cluster.Annotations[AnnotationManager] = "true"
	reconcileCluster(t, controller, cluster)

	store := controller.secrets.(*fakeSecretStore)
	for key, secret := range store.secrets {
		for name, value := range secret.Labels {
			if msgs := validation.IsValidLabelValue(value); len(msgs) > 0 {
				t.Errorf("%s label %s=%q is invalid: %v", key, name, value, msgs)
			}
		}
		if secret.Name == legacyServerSecretName {
			if got := secret.Labels[pki.CAFingerprintLabel]; len(got) == 0 || len(got) > 63 {
				t.Errorf("legacy fingerprint label = %q, want 1..63 bytes", got)
			}
		}
	}
}
