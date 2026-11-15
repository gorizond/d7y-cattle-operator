package cluster

import (
	"bytes"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEnsureMTLSSecretReplicatesToClusterAndTargetNamespace(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.OperatorNamespace = "d7y-system"
	store := controller.secrets.(*fakeSecretStore)
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "d7y-system"},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":                []byte("ca"),
			corev1.TLSCertKey:       []byte("cert"),
			corev1.TLSPrivateKeyKey: []byte("key"),
		},
	}
	store.put(source)

	if err := controller.ensureMTLSSecret(testSeedCluster("42")); err != nil {
		t.Fatalf("ensureMTLSSecret() error = %v", err)
	}

	namespaces := []string{"d7y-system", "fleet-default", "dragonfly-system"}
	for _, namespace := range namespaces {
		copy, err := store.Get(namespace, source.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get %s: %v", namespace, err)
		}
		if !bytes.Equal(copy.Data[corev1.TLSCertKey], source.Data[corev1.TLSCertKey]) ||
			!bytes.Equal(copy.Data["ca.crt"], source.Data["ca.crt"]) {
			t.Errorf("%s copy data differs from source", namespace)
		}
		if namespace != "d7y-system" {
			if _, ok := copy.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
				t.Errorf("%s copy has last-applied annotation", namespace)
			}
		}
	}

	rotated := source.DeepCopy()
	rotated.Data[corev1.TLSCertKey] = []byte("rotated-cert")
	rotated.Data["ca.crt"] = []byte("rotated-ca")
	store.put(rotated)
	// Seed a stale annotation on one copy; replication must strip it on update.
	fleetCopy, err := store.Get("fleet-default", source.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get fleet copy: %v", err)
	}
	fleetCopy.Annotations = map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"tls.key":"private"}}`,
		"keep": "yes",
	}
	store.put(fleetCopy)

	if err := controller.ensureMTLSSecret(testSeedCluster("42")); err != nil {
		t.Fatalf("ensureMTLSSecret(rotation) error = %v", err)
	}
	for _, namespace := range namespaces {
		copy, err := store.Get(namespace, source.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get %s: %v", namespace, err)
		}
		if !bytes.Equal(copy.Data[corev1.TLSCertKey], []byte("rotated-cert")) ||
			!bytes.Equal(copy.Data["ca.crt"], []byte("rotated-ca")) {
			t.Errorf("%s copy was not rotated", namespace)
		}
		if _, ok := copy.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
			t.Errorf("%s copy kept last-applied annotation", namespace)
		}
	}
	fleetCopy, err = store.Get("fleet-default", source.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get fleet copy: %v", err)
	}
	if fleetCopy.Annotations["keep"] != "yes" {
		t.Error("unrelated annotation was removed from fleet-default copy")
	}
}
