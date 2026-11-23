package cluster

import (
	"crypto/x509"
	"net"
	"slices"
	"testing"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	seedA.Annotations[AnnotationManager] = "true"
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

	opA := controller.helmops.(*fakeHelmOpStore).object("fleet-default/" + helmop.SchedulerCertName(seedA.Name))
	if opA == nil {
		t.Fatal("scheduler certificate HelmOp is missing")
	}
	if got := nestedString(t, opA, "spec", "namespace"); got != helmop.TargetNamespace {
		t.Errorf("scheduler certificate targetNamespace = %q", got)
	}
	if got := nestedString(t, opA, "spec", "helm", "chart"); got != "p2p-scheduler-cert" {
		t.Errorf("scheduler certificate chart = %q", got)
	}
	if got := nestedString(t, opA, "spec", "helm", "values", "tls", "crt"); got != string(schedulerAAfter.Data[corev1.TLSCertKey]) {
		t.Errorf("scheduler certificate tls.crt does not match the leaf")
	}
	labels, _, _ := unstructured.NestedStringMap(opA.Object, "spec", "labels")
	if labels["p2p-dragonfly-io"] != "p2p-mtls" || labels["p2p.dragonfly.io/cluster-name"] != "c1" {
		t.Errorf("scheduler certificate Bundle labels = %v", labels)
	}
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerBundleName(seedA.Name)) {
		t.Error("new seed still has a raw scheduler Bundle")
	}
}

func TestSchedulerCertMigratesLegacyBundleOnlyWhenReady(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := pkiSeed("c1", "c-m-a")
	legacyName := helmop.SchedulerBundleName(cluster.Name)
	controller.bundles.(*fakeHelmOpStore).objects["fleet-default/"+legacyName] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "Bundle",
		"metadata": map[string]any{
			"name":      legacyName,
			"namespace": "fleet-default",
			"labels": map[string]any{
				"p2p-dragonfly-io":              "p2p-mtls",
				"p2p.dragonfly.io/cluster-name": "c1",
			},
		},
	}}

	// First pass creates the HelmOp. The legacy Bundle must survive until the
	// HelmOp-generated Bundle is ready, so the Secret never disappears.
	reconcileCluster(t, controller, cluster)
	if !controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName("c1")) {
		t.Fatal("scheduler certificate HelmOp was not created")
	}
	if !controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + legacyName) {
		t.Fatal("legacy Bundle was removed before the replacement was ready")
	}

	markSchedulerCertReady(t, controller, cluster.Name)
	keepObserved := false
	controller.bundles.(*fakeHelmOpStore).deleteHook = func(object *unstructured.Unstructured) {
		if object.GetName() != legacyName {
			return
		}
		keep, _, _ := unstructured.NestedBool(object.Object, "spec", "keepResources")
		keepObserved = keep
		if !keep {
			// A Bundle without keepResources deletes the Secret on removal.
			controller.downstream.(*fakeSchedulerPodLister).secretMissing = true
		}
	}
	reconcileCluster(t, controller, cluster)
	if bundle := controller.bundles.(*fakeHelmOpStore).object("fleet-default/" + legacyName); bundle == nil {
		t.Fatal("legacy Bundle disappeared before it was orphaned")
	} else {
		if keep, _, _ := unstructured.NestedBool(bundle.Object, "spec", "keepResources"); !keep {
			t.Fatal("legacy Bundle keepResources was not patched before deletion")
		}
		if force, _, _ := unstructured.NestedInt64(bundle.Object, "spec", "forceSyncGeneration"); force == 0 {
			t.Fatal("legacy Bundle redeploy was not forced after the keepResources patch")
		}
	}
	// A redeploy still in progress must not allow deletion.
	reconcileCluster(t, controller, cluster)
	if !controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + legacyName) {
		t.Fatal("legacy Bundle was deleted before its orphan redeploy was ready")
	}
	markLegacySchedulerBundleReady(t, controller, cluster.Name)
	reconcileCluster(t, controller, cluster)
	if !keepObserved {
		t.Error("legacy scheduler Bundle was deleted without keepResources")
	}
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + legacyName) {
		t.Error("legacy scheduler Bundle remains after the HelmOp Bundle is ready")
	}
	op := controller.helmops.(*fakeHelmOpStore).object("fleet-default/" + helmop.SchedulerCertName("c1"))
	if take, _, _ := unstructured.NestedBool(op.Object, "spec", "helm", "takeOwnership"); !take {
		t.Error("scheduler certificate HelmOp does not take ownership of the Secret")
	}
	if !controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName("c1")) {
		t.Error("scheduler certificate HelmOp disappeared during migration")
	}
	if controller.downstream.(*fakeSchedulerPodLister).secretMissing {
		t.Error("downstream scheduler Secret disappeared during migration")
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(helmop.AnnotationMTLSCertRevision); got == "" {
		t.Error("scheduler revision was not set after migration")
	}

	// Idempotent: a second pass keeps the HelmOp and does not recreate the raw
	// Bundle or fail on the already-removed legacy object.
	reconcileCluster(t, controller, cluster)
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + legacyName) {
		t.Error("legacy scheduler Bundle was recreated")
	}
	if !controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName("c1")) {
		t.Error("scheduler certificate HelmOp was removed on a repeated reconcile")
	}
}

func TestSchedulerCertWaitsForSecretBeforeMigration(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := pkiSeed("c1", "c-m-a")
	legacyName := helmop.SchedulerBundleName(cluster.Name)
	controller.bundles.(*fakeHelmOpStore).objects["fleet-default/"+legacyName] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "Bundle",
		"metadata": map[string]any{
			"name":      legacyName,
			"namespace": "fleet-default",
			"labels": map[string]any{
				"p2p-dragonfly-io":              "p2p-mtls",
				"p2p.dragonfly.io/cluster-name": "c1",
			},
		},
	}}
	controller.downstream.(*fakeSchedulerPodLister).secretMissing = true

	reconcileCluster(t, controller, cluster)
	markSchedulerCertReady(t, controller, cluster.Name)
	reconcileCluster(t, controller, cluster)

	if !controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + legacyName) {
		t.Fatal("legacy Bundle was deleted while the downstream Secret was missing")
	}
	op := controller.helmops.(*fakeHelmOpStore).object("fleet-default/" + helmop.SchedulerCertName("c1"))
	force, _, _ := unstructured.NestedInt64(op.Object, "spec", "forceSyncGeneration")
	if force == 0 {
		t.Error("missing Secret did not force a HelmOp redeploy")
	}
	reconcileCluster(t, controller, cluster)
	op = controller.helmops.(*fakeHelmOpStore).object("fleet-default/" + helmop.SchedulerCertName("c1"))
	again, _, _ := unstructured.NestedInt64(op.Object, "spec", "forceSyncGeneration")
	if again != force {
		t.Errorf("forceSyncGeneration changed from %d to %d while the Secret was still missing", force, again)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(helmop.AnnotationMTLSCertRevision); got != "" {
		t.Errorf("scheduler revision = %q while the Secret is missing", got)
	}

	controller.downstream.(*fakeSchedulerPodLister).secretMissing = false
	reconcileCluster(t, controller, cluster)
	markLegacySchedulerBundleReady(t, controller, cluster.Name)
	reconcileCluster(t, controller, cluster)
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + legacyName) {
		t.Error("legacy Bundle remains after the Secret returned")
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(helmop.AnnotationMTLSCertRevision); got == "" {
		t.Error("scheduler revision was not set after the Secret returned")
	}
}

func TestSchedulerCertRecreatesSecretAfterLegacyDelete(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := pkiSeed("c1", "c-m-a")
	legacyName := helmop.SchedulerBundleName(cluster.Name)
	controller.bundles.(*fakeHelmOpStore).objects["fleet-default/"+legacyName] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "Bundle",
		"metadata": map[string]any{
			"name":      legacyName,
			"namespace": "fleet-default",
			"labels": map[string]any{
				"p2p-dragonfly-io":              "p2p-mtls",
				"p2p.dragonfly.io/cluster-name": "c1",
			},
		},
	}}
	controller.bundles.(*fakeHelmOpStore).deleteHook = func(object *unstructured.Unstructured) {
		// Simulate a legacy cleanup that still removes the Secret despite
		// keepResources; the operator must force the new HelmOp to recreate it.
		if object.GetName() == legacyName {
			controller.downstream.(*fakeSchedulerPodLister).secretMissing = true
		}
	}

	reconcileCluster(t, controller, cluster)
	markSchedulerCertReady(t, controller, cluster.Name)
	reconcileCluster(t, controller, cluster)
	markLegacySchedulerBundleReady(t, controller, cluster.Name)
	reconcileCluster(t, controller, cluster)

	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + legacyName) {
		t.Fatal("legacy Bundle was not deleted after the new Bundle was ready")
	}
	op := controller.helmops.(*fakeHelmOpStore).object("fleet-default/" + helmop.SchedulerCertName("c1"))
	if force, _, _ := unstructured.NestedInt64(op.Object, "spec", "forceSyncGeneration"); force == 0 {
		t.Error("missing Secret after legacy delete did not force a HelmOp redeploy")
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(helmop.AnnotationMTLSCertRevision); got != "" {
		t.Errorf("scheduler revision = %q before the Secret was recreated", got)
	}

	controller.downstream.(*fakeSchedulerPodLister).secretMissing = false
	reconcileCluster(t, controller, cluster)
	if got := controller.clusters.(*fakeClusterStore).annotation(helmop.AnnotationMTLSCertRevision); got == "" {
		t.Error("scheduler revision was not set after the Secret was recreated")
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
	seedA.Annotations[AnnotationManager] = "true"
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
	if controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName(cluster.Name)) {
		t.Error("manager-only cluster has a scheduler certificate HelmOp")
	}
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerBundleName(cluster.Name)) {
		t.Error("manager-only cluster has a legacy scheduler Bundle")
	}
	if _, err := controller.secrets.Get(cluster.Namespace, "dragonfly-p2p-mtls", metav1.GetOptions{}); err == nil {
		t.Error("manager-only cluster replicated the client identity")
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
	legacyLeaf, err := legacyCA.Sign(pki.IssueOptions{
		CommonName:  "legacy",
		DNSNames:    []string{"d7y-scheduler.c-m-a.example.com", "c1-control-scheduler.dragonfly-system.svc", "grpc.example.com", "api.example.com", "dragonfly-manager.dragonfly-system.svc", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		ServerAuth:  true,
		ClientAuth:  true,
	})
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
	if got := ca.Annotations[pki.CAAnnotationLegacyTrustDeadline]; got == "" {
		t.Error("Phase A did not set the global legacy trust deadline")
	}
	if got := controller.clusters.(*fakeClusterStore).annotations()["p2p.dragonfly.io/legacy-trust-deadline"]; got != "" {
		t.Errorf("legacy trust deadline was written to the Cluster: %q", got)
	}
	client := getSecret(t, controller, "dragonfly-system", "dragonfly-p2p-mtls")
	if string(client.Data[corev1.TLSCertKey]) != string(legacyLeaf.CertPEM) {
		t.Error("Phase A did not preserve the legacy client identity")
	}
	if got := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName("c1")); string(got.Data[corev1.TLSCertKey]) != string(legacyLeaf.CertPEM) {
		t.Error("Phase A did not preserve the legacy scheduler identity")
	}

	ca.Annotations[pki.CAAnnotationLegacyTrustDeadline] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	controller.secrets.(*fakeSecretStore).put(ca)
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

func nestedString(t *testing.T, obj *unstructured.Unstructured, fields ...string) string {
	t.Helper()
	value, _, err := unstructured.NestedString(obj.Object, fields...)
	if err != nil {
		t.Fatalf("read %v: %v", fields, err)
	}
	return value
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

func TestSchedulerRevisionAnnotatedOnlyAfterCertReady(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := pkiSeed("c1", "c-m-a")
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(helmop.AnnotationMTLSCertRevision); got != "" {
		t.Errorf("scheduler revision = %q, want empty until the HelmOp Bundle is ready", got)
	}
	markSchedulerCertReady(t, controller, cluster.Name)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(ready) error = %v", err)
	}
	if got := controller.clusters.(*fakeClusterStore).annotation(helmop.AnnotationMTLSCertRevision); got == "" {
		t.Error("scheduler revision was not set after HelmOp Bundle readiness")
	}
}

func installLegacyClient(t *testing.T, controller *Controller, clusterID string) (*pki.CA, *pki.Bundle) {
	t.Helper()
	legacyCA, err := pki.NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(legacy) error = %v", err)
	}
	legacyLeaf, err := legacyCA.Sign(pki.IssueOptions{
		CommonName: "legacy",
		DNSNames: []string{
			"d7y-scheduler." + clusterID + ".example.com",
			"c1-control-scheduler.dragonfly-system.svc",
			"grpc.example.com", "api.example.com",
			"dragonfly-manager.dragonfly-system.svc", "localhost",
		},
		IPAddresses: []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		ServerAuth:  true,
		ClientAuth:  true,
	})
	if err != nil {
		t.Fatalf("Sign(legacy) error = %v", err)
	}
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"ca.crt": legacyCA.CertPEM, "tls.crt": legacyLeaf.CertPEM, "tls.key": legacyLeaf.KeyPEM},
	})
	return legacyCA, legacyLeaf
}

func TestNewSeedDuringPhaseA(t *testing.T) {
	controller, _, _ := newTestController(t)
	_, legacyLeaf := installLegacyClient(t, controller, "c-m-a")
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		seedMachine("c1", "c1-0", "203.0.113.10"),
		seedMachine("seed-b", "seed-b-0", "203.0.113.11"),
	}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{
		{NodeName: "c1-node", HostIP: "203.0.113.10"},
		{NodeName: "seed-b-node", HostIP: "203.0.113.11"},
	}}
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seed-b-kubeconfig", Namespace: "fleet-default"},
		Data:       map[string][]byte{"value": []byte("fake")},
	})
	seedA := pkiSeed("c1", "c-m-a")
	reconcileCluster(t, controller, seedA)
	seedB := pkiSeed("seed-b", "c-m-b")
	reconcileCluster(t, controller, seedB)

	schedulerB := leaf(t, getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName("seed-b")))
	if string(schedulerB.Raw) == string(leaf(t, &corev1.Secret{Data: map[string][]byte{corev1.TLSCertKey: legacyLeaf.CertPEM}}).Raw) {
		t.Error("new seed reused a legacy scheduler identity without its SANs")
	}
	if !slices.Contains(schedulerB.DNSNames, "d7y-scheduler.c-m-b.example.com") {
		t.Errorf("new seed scheduler SANs = %v", schedulerB.DNSNames)
	}
	ca, _ := pki.ParseCertificates(getSecret(t, controller, "dragonfly-system", pki.CASecretName).Data["ca.crt"])
	if err := schedulerB.CheckSignatureFrom(ca[0]); err != nil {
		t.Errorf("new seed scheduler is not signed by the active CA: %v", err)
	}
	client := leaf(t, getSecret(t, controller, "dragonfly-system", "dragonfly-p2p-mtls"))
	if string(client.Raw) != string(leaf(t, &corev1.Secret{Data: map[string][]byte{corev1.TLSCertKey: legacyLeaf.CertPEM}}).Raw) {
		t.Error("Phase A client identity was re-signed for a new seed")
	}
}

func TestPhaseAAfterClientReSignUsesLegacyServerSecret(t *testing.T) {
	controller, _, _ := newTestController(t)
	_, legacyLeaf := installLegacyClient(t, controller, "c-m-a")
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{seedMachine("c1", "c1-0", "203.0.113.10")}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{{NodeName: "c1-node", HostIP: "203.0.113.10"}}}
	cluster := pkiSeed("c1", "c-m-a")
	cluster.Annotations[AnnotationManager] = "true"
	reconcileCluster(t, controller, cluster)

	caSecret := getSecret(t, controller, "dragonfly-system", pki.CASecretName)
	ca, err := pki.LoadCA(caSecret.Data["ca.crt"], caSecret.Data[pki.CAKey], caSecret.Data[pki.CABundleKey])
	if err != nil {
		t.Fatalf("LoadCA() error = %v", err)
	}
	if _, err := pki.EnsureLeaf(controller.secrets, "dragonfly-system", "dragonfly-p2p-mtls", ca,
		pki.IssueOptions{CommonName: "active-client", ClientAuth: true}, false); err != nil {
		t.Fatalf("re-sign client: %v", err)
	}
	store := controller.secrets.(*fakeSecretStore)
	delete(store.secrets, "dragonfly-system/"+helmop.ManagerMTLSSecretName)
	delete(store.secrets, "dragonfly-system/"+helmop.SchedulerSourceSecretName("c1"))
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	manager := leaf(t, getSecret(t, controller, "dragonfly-system", helmop.ManagerMTLSSecretName))
	scheduler := leaf(t, getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName("c1")))
	want := leaf(t, &corev1.Secret{Data: map[string][]byte{corev1.TLSCertKey: legacyLeaf.CertPEM}})
	if string(manager.Raw) != string(want.Raw) || string(scheduler.Raw) != string(want.Raw) {
		t.Error("missing manager/scheduler Secrets were not restored from the preserved legacy server leaf")
	}
}

func TestPKICAGuardClientOnly(t *testing.T) {
	controller, _, events := newTestController(t)
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system",
			Labels: map[string]string{pki.LeafGenerationLabel: pki.LeafGenerationV2},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"ca.crt": []byte("ca"), "tls.crt": []byte("cert"), "tls.key": []byte("key")},
	})
	cluster := pkiSeed("c1", "c-m-a")
	controller.clusters.(*fakeClusterStore).set(cluster)
	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want CA unavailable for a labeled client-only leaf")
	}
	if _, err := controller.secrets.Get("dragonfly-system", pki.CASecretName, metav1.GetOptions{}); err == nil {
		t.Error("CA was created while a labeled client leaf existed")
	}
	if !hasEventReason(events.events, "PKICAUnavailable") {
		t.Errorf("events = %+v, want PKICAUnavailable", events.events)
	}
}

func TestSeedOnlyDoesNotCreateManager(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := pkiSeed("c1", "c-m-a")
	reconcileCluster(t, controller, cluster)
	if _, err := controller.secrets.Get("dragonfly-system", helmop.ManagerMTLSSecretName, metav1.GetOptions{}); err == nil {
		t.Error("seed-only cluster created a manager identity Secret")
	}
	if _, err := controller.secrets.Get("fleet-default", helmop.ManagerMTLSSecretName, metav1.GetOptions{}); err == nil {
		t.Error("seed-only cluster replicated a manager identity Secret")
	}
}

func TestDeleteSchedulerPKIRespectsBundleOwnership(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := pkiSeed("c1", "c-m-a")
	reconcileCluster(t, controller, cluster)
	store := controller.bundles.(*fakeHelmOpStore)
	unrelated := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "Bundle",
		"metadata": map[string]any{
			"name":      helmop.SchedulerBundleName("c1"),
			"namespace": "fleet-default",
			"labels":    map[string]any{"p2p-dragonfly-io": "foreign"},
		},
	}}
	store.objects["fleet-default/"+helmop.SchedulerBundleName("c1")] = unrelated
	if err := controller.deleteSchedulerPKI(cluster); err != nil {
		t.Fatalf("deleteSchedulerPKI() error = %v", err)
	}
	if !store.has("fleet-default/" + helmop.SchedulerBundleName("c1")) {
		t.Error("deleteSchedulerPKI removed a legacy Bundle it does not own")
	}
	if controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName("c1")) {
		t.Error("deleteSchedulerPKI left the scheduler certificate HelmOp")
	}
}

func TestInvalidTrustDeadlineKeepsPhaseAAndEvents(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := pkiSeed("c1", "c-m-a")
	reconcileCluster(t, controller, cluster)
	ca := getSecret(t, controller, "dragonfly-system", pki.CASecretName)
	if ca.Annotations == nil {
		ca.Annotations = map[string]string{}
	}
	ca.Annotations[pki.CAAnnotationLegacyTrustDeadline] = "not-a-time"
	controller.secrets.(*fakeSecretStore).put(ca)
	before := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName("c1"))
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	after := getSecret(t, controller, "dragonfly-system", helmop.SchedulerSourceSecretName("c1"))
	if string(after.Data[corev1.TLSCertKey]) != string(before.Data[corev1.TLSCertKey]) {
		t.Error("invalid deadline caused a Phase B re-sign")
	}
	if !hasEventReason(events.events, "LegacyTrustDeadlineInvalid") {
		t.Errorf("events = %+v, want LegacyTrustDeadlineInvalid", events.events)
	}
}

func markSchedulerCertReady(t *testing.T, controller *Controller, clusterName string) {
	t.Helper()
	name := helmop.SchedulerCertName(clusterName)
	bundle := controller.bundles.(*fakeHelmOpStore).object("fleet-default/" + name)
	if bundle == nil {
		bundle = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "fleet.cattle.io/v1alpha1",
			"kind":       "Bundle",
			"metadata": map[string]any{
				"name":      name,
				"namespace": "fleet-default",
			},
		}}
		controller.bundles.(*fakeHelmOpStore).objects["fleet-default/"+name] = bundle
	}
	bundle.SetGeneration(1)
	_ = unstructured.SetNestedField(bundle.Object, int64(1), "status", "observedGeneration")
	_ = unstructured.SetNestedField(bundle.Object, int64(1), "status", "summary", "ready")
	_ = unstructured.SetNestedField(bundle.Object, int64(1), "status", "summary", "desiredReady")
}

func markLegacySchedulerBundleReady(t *testing.T, controller *Controller, clusterName string) {
	t.Helper()
	name := helmop.SchedulerBundleName(clusterName)
	bundle := controller.bundles.(*fakeHelmOpStore).object("fleet-default/" + name)
	if bundle == nil {
		t.Fatalf("legacy Bundle %s is missing", name)
	}
	if bundle.GetGeneration() == 0 {
		bundle.SetGeneration(1)
	}
	generation := bundle.GetGeneration()
	_ = unstructured.SetNestedField(bundle.Object, generation, "status", "observedGeneration")
	_ = unstructured.SetNestedField(bundle.Object, int64(1), "status", "summary", "ready")
	_ = unstructured.SetNestedField(bundle.Object, int64(1), "status", "summary", "desiredReady")
}

func TestSchedulerRevisionOnlyChangesForAffectedCluster(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{
		seedMachine("c1", "c1-0", "203.0.113.10"),
		seedMachine("seed-b", "seed-b-0", "203.0.113.11"),
	}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{
		{NodeName: "c1-node", HostIP: "203.0.113.10"},
		{NodeName: "seed-b-node", HostIP: "203.0.113.11"},
	}}
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "seed-b-kubeconfig", Namespace: "fleet-default"},
		Data:       map[string][]byte{"value": []byte("fake")},
	})
	clusterA := pkiSeed("c1", "c-m-a")
	clusterB := pkiSeed("seed-b", "c-m-b")
	controller.clusters.(*fakeClusterStore).set(clusterA)
	if _, err := controller.reconcile("", clusterA); err != nil {
		t.Fatalf("reconcile A: %v", err)
	}
	markSchedulerCertReady(t, controller, clusterA.Name)
	updatedA, err := controller.reconcile("", clusterA)
	if err != nil {
		t.Fatalf("reconcile A ready: %v", err)
	}
	revA := updatedA.Annotations[helmop.AnnotationMTLSCertRevision]
	if revA == "" {
		t.Fatal("cluster A scheduler revision is empty")
	}

	controller.clusters.(*fakeClusterStore).set(clusterB)
	if _, err := controller.reconcile("", clusterB); err != nil {
		t.Fatalf("reconcile B: %v", err)
	}
	markSchedulerCertReady(t, controller, clusterB.Name)
	updatedB, err := controller.reconcile("", clusterB)
	if err != nil {
		t.Fatalf("reconcile B ready: %v", err)
	}
	revB := updatedB.Annotations[helmop.AnnotationMTLSCertRevision]
	if revB == "" || revB == revA {
		t.Fatalf("cluster revisions A=%q B=%q, want distinct non-empty", revA, revB)
	}

	controller.machines.(*fakeMachineCache).machines[0] = seedMachine("c1", "c1-0", "203.0.113.12")
	controller.downstream.(*fakeSchedulerPodLister).pods[0].HostIP = "203.0.113.12"
	controller.clusters.(*fakeClusterStore).set(clusterA)
	if _, err := controller.reconcile("", clusterA); err != nil {
		t.Fatalf("reconcile A rotation: %v", err)
	}
	markSchedulerCertReady(t, controller, clusterA.Name)
	rotatedA, err := controller.reconcile("", clusterA)
	if err != nil {
		t.Fatalf("reconcile A rotated: %v", err)
	}
	if got := rotatedA.Annotations[helmop.AnnotationMTLSCertRevision]; got == revA {
		t.Error("cluster A scheduler revision did not change")
	}
	if updatedB.Annotations[helmop.AnnotationMTLSCertRevision] != revB {
		t.Error("cluster B revision changed while only cluster A was reconciled")
	}
}

func TestPhaseARepairsCorruptServerLeaf(t *testing.T) {
	controller, _, _ := newTestController(t)
	_, legacyLeaf := installLegacyClient(t, controller, "c-m-a")
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{seedMachine("c1", "c1-0", "203.0.113.10")}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{{NodeName: "c1-node", HostIP: "203.0.113.10"}}}
	cluster := pkiSeed("c1", "c-m-a")
	cluster.Annotations[AnnotationManager] = "true"
	reconcileCluster(t, controller, cluster)
	manager := getSecret(t, controller, "dragonfly-system", helmop.ManagerMTLSSecretName)
	manager.Data[corev1.TLSPrivateKeyKey] = []byte("corrupt")
	controller.secrets.(*fakeSecretStore).put(manager)
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	repaired := getSecret(t, controller, "dragonfly-system", helmop.ManagerMTLSSecretName)
	if string(repaired.Data[corev1.TLSCertKey]) != string(legacyLeaf.CertPEM) || string(repaired.Data[corev1.TLSPrivateKeyKey]) != string(legacyLeaf.KeyPEM) {
		t.Error("corrupt Phase A manager leaf was not repaired from the preserved legacy server leaf")
	}
}

func TestDeleteSchedulerPKICleanup(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := pkiSeed("c1", "c-m-a")
	cluster.Annotations[AnnotationManager] = "true"
	reconcileCluster(t, controller, cluster)
	if !controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName("c1")) {
		t.Fatal("scheduler certificate HelmOp was not created")
	}
	delete(cluster.Annotations, AnnotationSeed)
	cluster.Annotations[AnnotationManager] = "true"
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile(seed removed) error = %v", err)
	}
	if controller.helmops.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerCertName("c1")) {
		t.Error("scheduler certificate HelmOp was not deleted with the seed role")
	}
	if controller.bundles.(*fakeHelmOpStore).has("fleet-default/" + helmop.SchedulerBundleName("c1")) {
		t.Error("legacy scheduler Bundle was not deleted with the seed role")
	}
	if _, err := controller.secrets.Get("dragonfly-system", helmop.SchedulerSourceSecretName("c1"), metav1.GetOptions{}); err == nil {
		t.Error("scheduler source Secret was not deleted with the seed role")
	}
}

func putCAState(t *testing.T, controller *Controller, ca *pki.CA, bundle []byte, deadline string) {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: pki.CASecretName, Namespace: "dragonfly-system",
			Annotations: map[string]string{pki.CAAnnotationLegacyTrustDeadline: deadline},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca.crt":        ca.CertPEM,
			pki.CAKey:       ca.KeyPEM,
			pki.CABundleKey: bundle,
		},
	}
	controller.secrets.(*fakeSecretStore).put(secret)
}

func TestSecondRotationRejectsStaleLegacyServerLeaf(t *testing.T) {
	controller, _, _ := newTestController(t)
	oldCA, err := pki.NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(old) error = %v", err)
	}
	staleCA, err := pki.NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(stale) error = %v", err)
	}
	stale, err := staleCA.Sign(pki.IssueOptions{
		CommonName:  "stale",
		DNSNames:    []string{"d7y-scheduler.c-m-a.example.com", "c1-control-scheduler.dragonfly-system.svc", "grpc.example.com", "api.example.com", "dragonfly-manager.dragonfly-system.svc", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		ServerAuth:  true,
		ClientAuth:  true,
	})
	if err != nil {
		t.Fatalf("Sign(stale) error = %v", err)
	}
	activeCA, err := pki.NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(active) error = %v", err)
	}
	bundle := append(append([]byte(nil), activeCA.CertPEM...), oldCA.CertPEM...)
	putCAState(t, controller, activeCA, bundle, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	controller.secrets.(*fakeSecretStore).put(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: legacyServerSecretName, Namespace: "dragonfly-system"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"ca.crt": bundle, "tls.crt": stale.CertPEM, "tls.key": stale.KeyPEM},
	})
	cluster := pkiSeed("c1", "c-m-a")
	cluster.Annotations[AnnotationManager] = "true"
	reconcileCluster(t, controller, cluster)

	active := mustCerts(t, activeCA.CertPEM)
	for _, name := range []string{helmop.ManagerMTLSSecretName, helmop.SchedulerSourceSecretName("c1")} {
		secret := getSecret(t, controller, "dragonfly-system", name)
		leafCert := leaf(t, secret)
		if leafCert.CheckSignatureFrom(active[0]) != nil {
			t.Errorf("%s is not signed by the active CA", name)
		}
		if !pki.CertTrustedByBundle(secret.Data[corev1.TLSCertKey], bundle) {
			t.Errorf("%s is not trusted by the new bundle", name)
		}
	}
}

func mustCerts(t *testing.T, pemBytes []byte) []*x509.Certificate {
	t.Helper()
	certs, err := pki.ParseCertificates(pemBytes)
	if err != nil {
		t.Fatalf("parse certs: %v", err)
	}
	return certs
}

func TestLegacyServerCleanupOnlyAfterPhaseB(t *testing.T) {
	controller, _, _ := newTestController(t)
	installLegacyClient(t, controller, "c-m-a")
	controller.machines = &fakeMachineCache{machines: []*capiv1beta2.Machine{seedMachine("c1", "c1-0", "203.0.113.10")}}
	controller.downstream = &fakeSchedulerPodLister{pods: []schedulerPod{{NodeName: "c1-node", HostIP: "203.0.113.10"}}}
	cluster := pkiSeed("c1", "c-m-a")
	reconcileCluster(t, controller, cluster)
	if _, err := controller.secrets.Get("dragonfly-system", legacyServerSecretName, metav1.GetOptions{}); err != nil {
		t.Fatalf("legacy server Secret missing during Phase A: %v", err)
	}
	ca := getSecret(t, controller, "dragonfly-system", pki.CASecretName)
	if ca.Annotations == nil {
		ca.Annotations = map[string]string{}
	}
	ca.Annotations[pki.CAAnnotationLegacyTrustDeadline] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	controller.secrets.(*fakeSecretStore).put(ca)
	reconcileCluster(t, controller, cluster)
	if _, err := controller.secrets.Get("dragonfly-system", legacyServerSecretName, metav1.GetOptions{}); err == nil {
		t.Error("legacy server Secret was not cleaned up after Phase B")
	}
}

func TestServerLeafMustBeTrustedByBundle(t *testing.T) {
	caA, err := pki.NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(A) error = %v", err)
	}
	caB, err := pki.NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA(B) error = %v", err)
	}
	leafBundle, err := caA.Sign(pki.IssueOptions{CommonName: "legacy", ServerAuth: true, ClientAuth: true})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	secret := &corev1.Secret{Data: map[string][]byte{"tls.crt": leafBundle.CertPEM, "tls.key": leafBundle.KeyPEM}}
	if serverLeafCovers(secret, &pki.CA{BundlePEM: caB.CertPEM}, pki.IssueOptions{}) {
		t.Error("serverLeafCovers accepted a leaf not present in the bundle")
	}
	if !serverLeafCovers(secret, &pki.CA{BundlePEM: append(append([]byte(nil), caB.CertPEM...), caA.CertPEM...)}, pki.IssueOptions{}) {
		t.Error("serverLeafCovers rejected a leaf trusted by the bundle")
	}
}

func TestSchedulerSANIPsIncludePrivateAddresses(t *testing.T) {
	ips := schedulerSANIPs([]string{"10.48.0.45", "62.83.78.181", "127.0.0.1", "169.254.1.1", "not-an-ip"})
	if !certHasIP(ips, "10.48.0.45") {
		t.Errorf("scheduler SANs = %v, want the private local address", ips)
	}
	if !certHasIP(ips, "62.83.78.181") {
		t.Errorf("scheduler SANs = %v, want the public address", ips)
	}
	if certHasIP(ips, "127.0.0.1") || certHasIP(ips, "169.254.1.1") {
		t.Errorf("scheduler SANs = %v, want loopback and link-local excluded", ips)
	}
}

func TestExistingSchedulerIPsKeepPrivateAddresses(t *testing.T) {
	ca, err := pki.NewCA(nil)
	if err != nil {
		t.Fatalf("NewCA() error = %v", err)
	}
	bundle, err := ca.Sign(pki.IssueOptions{
		CommonName:  "scheduler",
		ServerAuth:  true,
		IPAddresses: []net.IP{net.ParseIP("10.48.0.45"), net.ParseIP("62.83.78.181"), net.ParseIP("127.0.0.1")},
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	secret := &corev1.Secret{Data: map[string][]byte{corev1.TLSCertKey: bundle.CertPEM}}
	ips := existingSchedulerIPs(secret)
	if !certHasIP(ips, "10.48.0.45") {
		t.Errorf("existing SANs = %v, want the previously issued private address", ips)
	}
	if !certHasIP(ips, "62.83.78.181") {
		t.Errorf("existing SANs = %v, want the previously issued public address", ips)
	}
	if certHasIP(ips, "127.0.0.1") {
		t.Errorf("existing SANs = %v, want loopback excluded", ips)
	}
}
