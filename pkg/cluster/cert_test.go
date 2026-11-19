package cluster

import (
	"bytes"
	"crypto/x509"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func TestEnsureMTLSCertificateResolvesHostAndSANs(t *testing.T) {
	controller, _, _ := newTestController(t)
	controller.opts.Resolver.(*fakeResolver).ips = []string{
		"203.0.113.10",
		"2001:db8::1",
		"10.0.0.5",
		"127.0.0.1",
		"169.254.1.1",
		"224.0.0.1",
		"fc00::1",
	}
	cluster := testSeedCluster("42")

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	resolver := controller.opts.Resolver.(*fakeResolver)
	if len(resolver.hosts) == 0 || resolver.hosts[0] != "d7y-scheduler.c-m-test.example.com" {
		t.Errorf("resolver hosts = %v, want d7y-scheduler.c-m-test.example.com", resolver.hosts)
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
	if len(leaf.IPAddresses) != 0 {
		t.Errorf("leaf IP SANs = %v, want none", leaf.IPAddresses)
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

func TestEnsureMTLSCertificateDNSFailureKeepsCertificate(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}
	before := certPEM(t, controller)

	resolver := controller.opts.Resolver.(*fakeResolver)
	resolver.err = errors.New("dns unavailable")
	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want DNS failure")
	}
	if got := certPEM(t, controller); string(got) != string(before) {
		t.Error("certificate changed after DNS failure")
	}
	if !hasEventReason(events.events, "SchedulerDNSResolutionFailed") {
		t.Errorf("events = %+v, want SchedulerDNSResolutionFailed", events.events)
	}
}

func TestEnsureMTLSCertificateEmptyResolutionKeepsCertificate(t *testing.T) {
	controller, _, events := newTestController(t)
	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}
	before := certPEM(t, controller)

	resolver := controller.opts.Resolver.(*fakeResolver)
	resolver.ips = nil
	if _, err := controller.reconcile("", cluster); err == nil {
		t.Fatal("reconcile() error = nil, want empty-resolution failure")
	}
	if got := certPEM(t, controller); string(got) != string(before) {
		t.Error("certificate changed after an empty DNS answer")
	}
	if !hasEventReason(events.events, "SchedulerDNSResolutionFailed") {
		t.Errorf("events = %+v, want SchedulerDNSResolutionFailed", events.events)
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
	resolver := controller.opts.Resolver.(*fakeResolver)
	if len(resolver.hosts) == 0 || resolver.hosts[0] != "d7y-scheduler.c-m-fallback.example.com" {
		t.Errorf("resolver hosts = %v, want fallback host", resolver.hosts)
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

func TestEnsureMTLSCertificateDNSOnlySANsAndPublicIPAnnotation(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	clusterStore := controller.clusters.(*fakeClusterStore)
	clusterStore.set(cluster)

	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}

	leaf := firstCertificate(t, certPEM(t, controller))
	if len(leaf.IPAddresses) != 0 {
		t.Errorf("leaf IP SANs = %v, want none", leaf.IPAddresses)
	}
	for _, want := range []string{
		"grpc.example.com",
		"manager-api.example.com",
		"d7y-scheduler.c-m-test.example.com",
	} {
		if !slices.Contains(leaf.DNSNames, want) {
			t.Errorf("leaf DNS SANs %v do not contain %q", leaf.DNSNames, want)
		}
	}
	if got := clusterStore.annotation(helmop.AnnotationSchedulerPublicIP); got != "203.0.113.10" {
		t.Errorf("scheduler public IP annotation = %q, want the resolved IP", got)
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
	if len(leaf.IPAddresses) != 0 {
		t.Errorf("bootstrap leaf IP SANs = %v, want none", leaf.IPAddresses)
	}
}

func TestValidPublicIPsFiltersNonPublic(t *testing.T) {
	got := validPublicIPs([]string{
		"203.0.113.10",
		"2001:db8::1",
		"10.0.0.5",
		"127.0.0.1",
		"169.254.1.1",
		"224.0.0.1",
		"fc00::1",
	})
	want := []string{"203.0.113.10", "2001:db8::1"}
	if len(got) != len(want) {
		t.Fatalf("validPublicIPs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("validPublicIPs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
