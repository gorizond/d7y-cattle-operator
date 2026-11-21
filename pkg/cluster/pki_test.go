package cluster

import (
	"crypto/x509"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

func pkiSeed(name, clusterID string) *provv1.Cluster {
	cluster := testSeedCluster("42")
	cluster.Name = name
	cluster.Status.ClusterName = clusterID
	return cluster
}

func reconcileCluster(t *testing.T, controller *Controller, cluster *provv1.Cluster) {
	t.Helper()
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(%s) error = %v", cluster.Name, err)
	}
}

func getSecret(t *testing.T, controller *Controller, namespace, name string) *corev1.Secret {
	t.Helper()
	secret, err := controller.secrets.Get(namespace, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Secret %s/%s: %v", namespace, name, err)
	}
	return secret
}

func leaf(t *testing.T, secret *corev1.Secret) *x509.Certificate {
	t.Helper()
	certs, err := pki.ParseCertificates(secret.Data[corev1.TLSCertKey])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return certs[0]
}

func TestPKIStableCAAndSplitLeaves(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		seedMachine("c1", "c1-0", "203.0.113.10"),
		seedMachine("seed-b", "seed-b-0", "203.0.113.11"),
	}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{
		{NodeName: "c1-node", HostIP: "203.0.113.10"},
		{NodeName: "seed-b-node", HostIP: "203.0.113.11"},
	}}

	seedA := pkiSeed("c1", "c-m-a")
	seedB := pkiSeed("seed-b", "c-m-b")
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seed-b-kubeconfig", Namespace: "fleet-default"},
		Data:       map[string][]byte{"value": []byte("fake")},
	})
	reconcileCluster(t, controller, seedA)
	caBefore := getSecret(t, controller, "dragonfly-system", pki.CASecretName)
	clientBefore := getSecret(t, controller, "dragonfly-system", "dragonfly-p2p-mtls")
	managerBefore := getSecret(t, controller, "dragonfly-system", helmop.ManagerMTLSSecretName)
	schedulerABefore := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName(seedA.Name))

	reconcileCluster(t, controller, seedB)
	caAfter := getSecret(t, controller, "dragonfly-system", pki.CASecretName)
	clientAfter := getSecret(t, controller, "dragonfly-system", "dragonfly-p2p-mtls")
	managerAfter := getSecret(t, controller, "dragonfly-system", helmop.ManagerMTLSSecretName)
	schedulerAAfter := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName(seedA.Name))
	schedulerB := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName(seedB.Name))

	if string(caAfter.Data["ca.crt"]) != string(caBefore.Data["ca.crt"]) || string(caAfter.Data["ca.key"]) != string(caBefore.Data["ca.key"]) {
		t.Error("stable CA rotated while adding a second seed")
	}
	if string(clientAfter.Data[corev1.TLSCertKey]) != string(clientBefore.Data[corev1.TLSCertKey]) {
		t.Error("shared client leaf rotated while adding a second seed")
	}
	if string(managerAfter.Data[corev1.TLSCertKey]) != string(managerBefore.Data[corev1.TLSCertKey]) {
		t.Error("manager leaf rotated while adding a second seed")
	}
	if string(schedulerAAfter.Data[corev1.TLSCertKey]) != string(schedulerABefore.Data[corev1.TLSCertKey]) {
		t.Error("seed A scheduler leaf rotated when seed B was added")
	}

	clientCert := leaf(t, clientAfter)
	if len(clientCert.DNSNames) != 0 || len(clientCert.IPAddresses) != 0 {
		t.Errorf("client SANs = %v / %v, want none", clientCert.DNSNames, clientCert.IPAddresses)
	}
	if !slices.Equal(clientCert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		t.Errorf("client EKU = %v, want ClientAuth only", clientCert.ExtKeyUsage)
	}
	managerCert := leaf(t, managerAfter)
	for _, want := range []string{"grpc.example.com", "api.example.com", "dragonfly-manager.dragonfly-system.svc", "localhost"} {
		if !slices.Contains(managerCert.DNSNames, want) {
			t.Errorf("manager SANs = %v, missing %q", managerCert.DNSNames, want)
		}
	}
	if slices.Contains(managerCert.DNSNames, "d7y-scheduler.c-m-a.example.com") {
		t.Errorf("manager SANs unexpectedly contain a scheduler host: %v", managerCert.DNSNames)
	}
	schedulerACert := leaf(t, schedulerAAfter)
	schedulerBCert := leaf(t, schedulerB)
	for _, want := range []string{"d7y-scheduler.c-m-a.example.com", "c1-control-scheduler.dragonfly-system.svc"} {
		if !slices.Contains(schedulerACert.DNSNames, want) {
			t.Errorf("scheduler A SANs = %v, missing %q", schedulerACert.DNSNames, want)
		}
	}
	if slices.Contains(schedulerACert.DNSNames, "d7y-scheduler.c-m-b.example.com") {
		t.Errorf("scheduler A SANs leak seed B: %v", schedulerACert.DNSNames)
	}
	if !slices.Contains(schedulerBCert.DNSNames, "d7y-scheduler.c-m-b.example.com") {
		t.Errorf("scheduler B SANs = %v", schedulerBCert.DNSNames)
	}
	if !certHasIP(schedulerACert.IPAddresses, "203.0.113.10") || !certHasIP(schedulerBCert.IPAddresses, "203.0.113.11") {
		t.Errorf("scheduler IP SANs = %v / %v", schedulerACert.IPAddresses, schedulerBCert.IPAddresses)
	}

	bundleA := getBundle(t, controller, seedA.Namespace, helmop.SchedulerBundleName(seedA.Name))
	if bundleA.GetLabels()["p2p-dragonfly-io"] != "p2p-mtls" || bundleA.GetLabels()["p2p.dragonfly.io/cluster-name"] != "c1" {
		t.Errorf("scheduler Bundle labels = %v", bundleA.GetLabels())
	}
	if bundleA.Spec.TargetNamespace != helmop.TargetNamespace || len(bundleA.Spec.Targets) != 1 || bundleA.Spec.Targets[0].ClusterName != "c1" {
		t.Errorf("scheduler Bundle target = %+v", bundleA.Spec)
	}
	if len(bundleA.Spec.Resources) != 1 || !strings.Contains(bundleA.Spec.Resources[0].Content, "name: dragonfly-p2p-scheduler") {
		t.Errorf("scheduler Bundle resources = %+v", bundleA.Spec.Resources)
	}
}

func TestSchedulerRotationAffectsOnlyThatSeed(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		seedMachine("c1", "c1-0", "203.0.113.10"),
		seedMachine("seed-b", "seed-b-0", "203.0.113.11"),
	}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{
		{NodeName: "c1-node", HostIP: "203.0.113.10"},
		{NodeName: "seed-b-node", HostIP: "203.0.113.11"},
	}}
	seedA := pkiSeed("c1", "c-m-a")
	seedB := pkiSeed("seed-b", "c-m-b")
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seed-b-kubeconfig", Namespace: "fleet-default"},
		Data:       map[string][]byte{"value": []byte("fake")},
	})
	reconcileCluster(t, controller, seedA)
	reconcileCluster(t, controller, seedB)
	clientBefore := getSecret(t, controller, "dragonfly-system", "dragonfly-p2p-mtls")
	managerBefore := getSecret(t, controller, "dragonfly-system", helmop.ManagerMTLSSecretName)
	schedulerABefore := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName(seedA.Name))
	schedulerBBefore := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName(seedB.Name))

	controller.machines.(*fakeMachineCache).machines[0] = seedMachine("c1", "c1-0", "203.0.113.12")
	controller.downstream.(*fakeSchedulerPodLister).pods[0].HostIP = "203.0.113.12"
	reconcileCluster(t, controller, seedA)

	if got := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName(seedA.Name)); string(got.Data[corev1.TLSCertKey]) == string(schedulerABefore.Data[corev1.TLSCertKey]) {
		t.Error("scheduler A leaf did not rotate after its IP changed")
	}
	if got := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName(seedB.Name)); string(got.Data[corev1.TLSCertKey]) != string(schedulerBBefore.Data[corev1.TLSCertKey]) {
		t.Error("scheduler B leaf rotated when seed A changed")
	}
	if got := getSecret(t, controller, "dragonfly-system", "dragonfly-p2p-mtls"); string(got.Data[corev1.TLSCertKey]) != string(clientBefore.Data[corev1.TLSCertKey]) {
		t.Error("client leaf rotated when a seed SAN changed")
	}
	if got := getSecret(t, controller, "dragonfly-system", helmop.ManagerMTLSSecretName); string(got.Data[corev1.TLSCertKey]) != string(managerBefore.Data[corev1.TLSCertKey]) {
		t.Error("manager leaf rotated when a seed SAN changed")
	}
}

func TestManagerOnlyPKI(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := pkiSeed("manager-only", "c-m-manager")
	delete(cluster.Annotations, AnnotationSeed)
	cluster.Annotations[AnnotationManager] = "true"
	reconcileCluster(t, controller, cluster)

	if _, err := controller.secrets.Get("dragonfly-system", helmop.SchedulerSourceSecretName(cluster.Name), metav1.GetOptions{}); err == nil {
		t.Error("manager-only cluster has a scheduler Secret")
	}
	if err := controller.bundles.Get(controller.ctx, cluster.Namespace, helmop.SchedulerBundleName(cluster.Name), &fleetv1alpha1.Bundle{}, metav1.GetOptions{}); err == nil {
		t.Error("manager-only cluster has a scheduler Bundle")
	}
	managerCert := leaf(t, getSecret(t, controller, "dragonfly-system", helmop.ManagerMTLSSecretName))
	for _, want := range []string{"grpc.example.com", "api.example.com", "localhost"} {
		if !slices.Contains(managerCert.DNSNames, want) {
			t.Errorf("manager SANs = %v, missing %q", managerCert.DNSNames, want)
		}
	}
}

func TestPKICAGuard(t *testing.T) {
	controller, _, events := newTestController(t)
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: helmop.ManagerMTLSSecretName, Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"ca.crt": []byte("ca"), "tls.crt": []byte("cert"), "tls.key": []byte("key")},
	})
	cluster := pkiSeed("c1", "c-m-a")
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want CA unavailable")
	}
	if _, err := controller.secrets.Get("dragonfly-system", pki.CASecretName, metav1.GetOptions{}); err == nil {
		t.Error("CA was created while a split leaf existed")
	}
	if !hasEventReason(events.events, "PKICAUnavailable") {
		t.Errorf("events = %+v, want PKICAUnavailable", events.events)
	}
}

func TestLegacyTrustPhaseAThenPhaseB(t *testing.T) {
	controller, _, _ := newTestController(t)
	legacyCA, err := pki.NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(legacy) error = %v", err)
	}
	legacyLeaf, err := legacyCA.Sign(pki.IssueOptions{CommonName: "legacy", ServerAuth: true, ClientAuth: true})
	if err != nil {
		t.Fatalf("Sign(legacy) error = %v", err)
	}
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"ca.crt": legacyCA.CertPEM, "tls.crt": legacyLeaf.CertPEM, "tls.key": legacyLeaf.KeyPEM},
	})
	cluster := pkiSeed("c1", "c-m-a")
	reconcileCluster(t, controller, cluster)

	ca := getSecret(t, controller, "dragonfly-system", pki.CASecretName)
	bundleCerts, err := pki.ParseCertificates(ca.Data[pki.CABundleKey])
	if err != nil || len(bundleCerts) != 2 {
		t.Fatalf("Phase A CA bundle = %d certs, error %v", len(bundleCerts), err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(AnnotationLegacyTrustDeadline); got == "" {
		t.Error("Phase A did not set legacy trust deadline")
	}
	client := getSecret(t, controller, "dragonfly-system", "dragonfly-p2p-mtls")
	if string(client.Data[corev1.TLSCertKey]) != string(legacyLeaf.CertPEM) {
		t.Error("Phase A did not preserve the legacy client identity")
	}
	if got := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName("c1")); string(got.Data[corev1.TLSCertKey]) != string(legacyLeaf.CertPEM) {
		t.Error("Phase A did not preserve the legacy scheduler identity")
	}

	cluster = controller.clusters.(*fakeClusterStore).cluster.DeepCopy()
	cluster.Annotations[AnnotationLegacyTrustDeadline] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("Phase B reconcile error = %v", err)
	}
	client = getSecret(t, controller, "dragonfly-system", "dragonfly-p2p-mtls")
	if string(client.Data[corev1.TLSCertKey]) == string(legacyLeaf.CertPEM) {
		t.Error("Phase B did not re-sign the client identity")
	}
	scheduler := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName("c1"))
	if string(scheduler.Data[corev1.TLSCertKey]) == string(legacyLeaf.CertPEM) {
		t.Error("Phase B did not re-sign the scheduler identity")
	}
	caAfter := getSecret(t, controller, "dragonfly-system", pki.CASecretName)
	if string(caAfter.Data["ca.crt"]) != string(ca.Data["ca.crt"]) {
		t.Error("Phase B rotated the stable CA")
	}
}

func getBundle(t *testing.T, controller *Controller, namespace, name string) *fleetv1alpha1.Bundle {
	t.Helper()
	raw := &unstructured.Unstructured{}
	if err := controller.bundles.Get(controller.ctx, namespace, name, raw, metav1.GetOptions{}); err != nil {
		t.Fatalf("get Bundle %s/%s: %v", namespace, name, err)
	}
	bundle := &fleetv1alpha1.Bundle{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, bundle); err != nil {
		t.Fatalf("decode Bundle %s/%s: %v", namespace, name, err)
	}
	return bundle
}

func certHasIP(ips []net.IP, want string) bool {
	for _, ip := range ips {
		if ip.String() == want {
			return true
		}
	}
	return false
}

func TestPKICAGuardSeesOtherSeedLeaf(t *testing.T) {
	controller, _, events := newTestController(t)
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: helmop.SchedulerSourceSecretName("seed-b"), Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"ca.crt": []byte("ca"), "tls.crt": []byte("cert"), "tls.key": []byte("key")},
	})
	other := pkiSeed("seed-b", "c-m-b")
	controller.clusterCache = &fakeClusterCache{clusters: []*provv1.Cluster{other}}
	controller.clusters.(*fakeClusterStore).set(other)
	cluster := pkiSeed("c1", "c-m-a")
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want CA unavailable for another seed leaf")
	}
	if !hasEventReason(events.events, "PKICAUnavailable") {
		t.Errorf("events = %+v, want PKICAUnavailable", events.events)
	}
}
