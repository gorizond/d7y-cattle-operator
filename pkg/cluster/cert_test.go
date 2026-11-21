package cluster

import (
	"bytes"
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

func TestSchedulerHost(t *testing.T) {
	tests := map[string]struct {
		domain  string
		cluster string
		want    string
	}{
		"normal": {"example.com", "c-m-test", "d7y-scheduler.c-m-test.example.com"},
		"dotted": {".example.com.", "c1", "d7y-scheduler.c1.example.com"},
		"empty":  {"", "c1", ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := schedulerHost(tc.domain, tc.cluster); got != tc.want {
				t.Errorf("schedulerHost(%q, %q) = %q, want %q", tc.domain, tc.cluster, got, tc.want)
			}
		})
	}
}

func TestEnsureMTLSCertificateUsesMachineIPs(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.machines.(*fakeMachineCache).machines = []*capiv1beta2.Machine{{
		ObjectMeta: metav1.ObjectMeta{Name: "c1-0", Namespace: "fleet-default", Labels: map[string]string{capiClusterName: "c1"}},
		Status: capiv1beta2.MachineStatus{Addresses: []capiv1beta2.MachineAddress{
			{Type: capiv1beta2.MachineExternalIP, Address: "203.0.113.10"},
			{Type: capiv1beta2.MachineInternalIP, Address: "2001:db8::1"},
			{Type: capiv1beta2.MachineInternalIP, Address: "10.0.0.5"},
			{Type: capiv1beta2.MachineInternalIP, Address: "127.0.0.1"},
		}},
	}}
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	secret, err := controller.secrets.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	certs, err := pki.ParseCertificates(secret.Data[corev1.TLSCertKey])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	leaf := certs[0]
	for _, want := range []string{
		"grpc.example.com",
		"manager-api.example.com",
		"d7y-scheduler.c-m-test.example.com",
	} {
		if !containsString(leaf.DNSNames, want) {
			t.Errorf("leaf DNS SANs %v do not contain %q", leaf.DNSNames, want)
		}
	}
	if !certHasIP(leaf.IPAddresses, "203.0.113.10") {
		t.Errorf("leaf IP SANs %v do not contain the machine IP", leaf.IPAddresses)
	}
	for _, forbidden := range []string{"10.0.0.5", "2001:db8::1", "62.83.79.230"} {
		if certHasIP(leaf.IPAddresses, forbidden) {
			t.Errorf("leaf contains forbidden/non-machine IP %s", forbidden)
		}
	}
}

func TestEnsureMTLSCertificateKeepsOtherSchedulerHosts(t *testing.T) {
	controller, _, _ := newTestController(t)

	clusterA := testSeedCluster("42")
	clusterA.Name = "seed-a"
	clusterA.Status.ClusterName = "c-m-a"
	if _, err := controller.reconcile("", clusterA); err != nil {
		t.Fatalf("reconcile(seed A) error = %v", err)
	}

	clusterB := testSeedCluster("42")
	clusterB.Name = "seed-b"
	clusterB.Status.ClusterName = "c-m-b"
	if _, err := controller.reconcile("", clusterB); err != nil {
		t.Fatalf("reconcile(seed B) error = %v", err)
	}

	leaf := firstCertificate(t, certPEM(t, controller))
	for _, want := range []string{
		"d7y-scheduler.c-m-a.example.com",
		"d7y-scheduler.c-m-b.example.com",
	} {
		if !slices.Contains(leaf.DNSNames, want) {
			t.Errorf("leaf DNS SANs %v do not contain %q", leaf.DNSNames, want)
		}
	}

	stable := certPEM(t, controller)
	if _, err := controller.reconcile("", clusterB); err != nil {
		t.Fatalf("stable reconcile(seed B) error = %v", err)
	}
	if got := certPEM(t, controller); !bytes.Equal(got, stable) {
		t.Error("certificate rotated when the scheduler SAN set was unchanged")
	}
}

func TestEnsureMTLSCertificateMachineListFailureKeepsCertificate(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}
	before := certPEM(t, controller)

	controller.machines.(*fakeMachineCache).err = errors.New("machine cache unavailable")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v, want machine failure tolerated", err)
	}
	if got := certPEM(t, controller); string(got) != string(before) {
		t.Error("certificate changed after machine list failure")
	}
	if !hasEventReason(events.events, "SchedulerIPsUnavailable") {
		t.Errorf("events = %+v, want SchedulerMachineIPsFailed", events.events)
	}
}

func TestEnsureMTLSCertificateNoMachinesKeepsAccumulatedIPs(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}
	before := certPEM(t, controller)

	controller.machines.(*fakeMachineCache).machines = nil
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := certPEM(t, controller); string(got) != string(before) {
		t.Error("certificate changed when machines disappeared but existing SANs remained")
	}
	leaf := firstCertificate(t, before)
	if !certHasIP(leaf.IPAddresses, "203.0.113.10") {
		t.Errorf("leaf IP SANs %v do not contain the accumulated machine IP", leaf.IPAddresses)
	}
}

func TestEnsureMTLSCertificateBootstrapsThenAddsMachineIPs(t *testing.T) {
	controller, store, _ := newTestController(t)
	delete(controller.secrets.(*fakeSecretStore).secrets, "dragonfly-system/dragonfly-p2p-mtls")
	controller.machines.(*fakeMachineCache).machines = nil
	cluster := testSeedCluster("42")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if !store.has("fleet-default/" + helmop.SharedExtrasName()) {
		t.Error("shared extras HelmOp was not applied")
	}
	if got := clusterStore.annotation(AnnotationSchedulerIPs); got != "" {
		t.Errorf("scheduler IPs without machines = %q, want empty", got)
	}

	before := certPEM(t, controller)
	leaf := firstCertificate(t, before)
	if !containsString(leaf.DNSNames, "d7y-scheduler.c-m-test.example.com") {
		t.Errorf("leaf DNS SANs = %v, want the scheduler host", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) != 2 || !certHasIP(leaf.IPAddresses, "127.0.0.1") || !certHasIP(leaf.IPAddresses, "::1") {
		t.Errorf("leaf IP SANs = %v, want only loopbacks before machines appear", leaf.IPAddresses)
	}

	controller.machines.(*fakeMachineCache).machines = []*capiv1beta2.Machine{seedMachine("c1", "c1-0", "203.0.113.10")}
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("machine reconcile() error = %v", err)
	}
	if got := clusterStore.annotation(AnnotationSchedulerIPs); got != "203.0.113.10" {
		t.Errorf("scheduler IPs = %q, want machine IP", got)
	}
	rotated := certPEM(t, controller)
	if bytes.Equal(rotated, before) {
		t.Fatal("certificate did not rotate after machine IPs appeared")
	}
	leaf = firstCertificate(t, rotated)
	if !certHasIP(leaf.IPAddresses, "203.0.113.10") {
		t.Errorf("leaf IP SANs = %v, want the machine IP", leaf.IPAddresses)
	}
	if !containsString(leaf.DNSNames, "d7y-scheduler.c-m-test.example.com") {
		t.Errorf("leaf DNS SANs = %v, want the scheduler host", leaf.DNSNames)
	}

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("stable reconcile() error = %v", err)
	}
	if got := certPEM(t, controller); !bytes.Equal(got, rotated) {
		t.Error("certificate rotated without a SAN change")
	}
}

func TestEnsureMTLSCertificateClusterIDFallback(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.ClusterIDFallback = "c-m-fallback"
	cluster := testSeedCluster("42")
	cluster.Status.ClusterName = ""

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	leaf := firstCertificate(t, certPEM(t, controller))
	if !containsString(leaf.DNSNames, "d7y-scheduler.c-m-fallback.example.com") {
		t.Errorf("leaf DNS SANs = %v, want fallback scheduler host", leaf.DNSNames)
	}
}

func TestEnsureMTLSCertificateMissingClusterID(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	cluster.Status.ClusterName = ""
	before := certPEM(t, controller)

	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want missing cluster ID error")
	}
	if got := certPEM(t, controller); string(got) != string(before) {
		t.Error("certificate changed after a missing cluster ID")
	}
	if !hasEventReason(events.events, "SchedulerHostUnavailable") {
		t.Errorf("events = %+v, want SchedulerHostUnavailable", events.events)
	}
}

func TestEnsureMTLSCertificatePublicIPRotationPreservesOldIP(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}
	first := certPEM(t, controller)

	controller.machines.(*fakeMachineCache).machines = []*capiv1beta2.Machine{seedMachine("c1", "c1-0", "203.0.113.11")}
	controller.downstream.(*fakeSchedulerPodLister).pods = []schedulerPod{{NodeName: "c1-node", HostIP: "203.0.113.11"}}
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("rotation reconcile() error = %v", err)
	}
	rotated := certPEM(t, controller)
	if string(rotated) == string(first) {
		t.Fatal("certificate did not rotate after a DNS change")
	}
	leaf := firstCertificate(t, rotated)
	for _, want := range []string{"203.0.113.10", "203.0.113.11"} {
		if !certHasIP(leaf.IPAddresses, want) {
			t.Errorf("rotated leaf IP SANs %v do not contain %q", leaf.IPAddresses, want)
		}
	}

	// A stable DNS answer after the rotation is a no-op.
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("stable reconcile() error = %v", err)
	}
	if got := certPEM(t, controller); string(got) != string(rotated) {
		t.Error("certificate rotated without a SAN change")
	}
}

func certPEM(t *testing.T, controller *Controller) []byte {
	t.Helper()
	secret, err := controller.secrets.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	return secret.Data[corev1.TLSCertKey]
}

func firstCertificate(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	certs, err := pki.ParseCertificates(certPEM)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return certs[0]
}

func certHasIP(ips []net.IP, want string) bool {
	for _, ip := range ips {
		if ip.String() == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasEventReason(events []*corev1.Event, reason string) bool {
	for _, event := range events {
		if event.Reason == reason {
			return true
		}
	}
	return false
}

func TestManagerOnlyBootstrapsMissingCertificate(t *testing.T) {
	controller, _, _ := newTestController(t)
	delete(controller.secrets.(*fakeSecretStore).secrets, "dragonfly-system/dragonfly-p2p-mtls")
	cluster := testSeedCluster("")
	delete(cluster.Annotations, AnnotationSeed)
	cluster.Annotations[AnnotationManager] = "true"

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	secret, err := controller.secrets.Get("dragonfly-system", "dragonfly-p2p-mtls", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	certs, err := pki.ParseCertificates(secret.Data[corev1.TLSCertKey])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	leaf := certs[0]
	if !containsString(leaf.DNSNames, "grpc.example.com") {
		t.Errorf("bootstrap leaf DNS SANs = %v", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) != 2 {
		t.Errorf("bootstrap leaf IP SANs = %v, want only loopbacks", leaf.IPAddresses)
	}
	if certHasIP(leaf.IPAddresses, "203.0.113.10") {
		t.Error("manager-only bootstrap unexpectedly added a public IP")
	}
}
