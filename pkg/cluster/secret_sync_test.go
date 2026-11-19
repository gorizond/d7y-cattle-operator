package cluster

import (
	"bytes"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

func TestEnsureMTLSSecretReplicatesToClusterNamespace(t *testing.T) {
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
	cluster := testSeedCluster("42")

	if err := controller.ensureMTLSSecret(cluster); err != nil {
		t.Fatalf("ensureMTLSSecret() error = %v", err)
	}

	copy, err := store.Get(cluster.Namespace, source.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s copy: %v", cluster.Namespace, err)
	}
	if !bytes.Equal(copy.Data[corev1.TLSCertKey], source.Data[corev1.TLSCertKey]) ||
		!bytes.Equal(copy.Data["ca.crt"], source.Data["ca.crt"]) ||
		!bytes.Equal(copy.Data[corev1.TLSPrivateKeyKey], source.Data[corev1.TLSPrivateKeyKey]) {
		t.Error("cluster namespace copy data differs from source")
	}
	if _, ok := copy.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Error("cluster namespace copy has last-applied annotation")
	}

	// Fleet's DownstreamResources entries remain the supported downstream sync.
	op := helmop.Manager(controller.helmopCluster(cluster))
	if len(op.Spec.DownstreamResources) != 2 ||
		op.Spec.DownstreamResources[0].Kind != "Secret" ||
		op.Spec.DownstreamResources[0].Name != source.Name ||
		op.Spec.DownstreamResources[1].Kind != "Secret" ||
		op.Spec.DownstreamResources[1].Name != controller.opts.ManagerAPICredentialsSecret {
		t.Errorf("manager downstreamResources = %+v, want Secrets %s and %s",
			op.Spec.DownstreamResources, source.Name, controller.opts.ManagerAPICredentialsSecret)
	}

	rotated := source.DeepCopy()
	rotated.Data[corev1.TLSCertKey] = []byte("rotated-cert")
	rotated.Data["ca.crt"] = []byte("rotated-ca")
	store.put(rotated)
	// Seed a stale annotation on the cluster copy; replication must strip it.
	clusterCopy, err := store.Get(cluster.Namespace, source.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cluster copy: %v", err)
	}
	clusterCopy.Annotations = map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"tls.key":"private"}}`,
		"keep": "yes",
	}
	store.put(clusterCopy)

	if err := controller.ensureMTLSSecret(cluster); err != nil {
		t.Fatalf("ensureMTLSSecret(rotation) error = %v", err)
	}
	clusterCopy, err = store.Get(cluster.Namespace, source.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cluster copy: %v", err)
	}
	if !bytes.Equal(clusterCopy.Data[corev1.TLSCertKey], []byte("rotated-cert")) ||
		!bytes.Equal(clusterCopy.Data["ca.crt"], []byte("rotated-ca")) {
		t.Error("cluster namespace copy was not rotated")
	}
	if _, ok := clusterCopy.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Error("cluster namespace copy kept last-applied annotation")
	}
	if clusterCopy.Annotations["keep"] != "yes" {
		t.Error("unrelated annotation was removed from cluster copy")
	}
}
