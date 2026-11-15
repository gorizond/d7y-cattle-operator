package cluster

import (
	"crypto/x509"
	"errors"
	"net"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	if !certHasIP(leaf.IPAddresses, "203.0.113.10") {
		t.Errorf("leaf IP SANs %v do not contain the resolved public IP", leaf.IPAddresses)
	}
	for _, want := range []string{"grpc.example.com", "manager-api.example.com"} {
		if !containsString(leaf.DNSNames, want) {
			t.Errorf("leaf DNS SANs %v do not contain %q", leaf.DNSNames, want)
		}
	}
	if certHasIP(leaf.IPAddresses, "10.42.0.1") {
		t.Error("leaf contains a node IP")
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

func TestEnsureMTLSCertificatePublicIPRotationPreservesOldIP(t *testing.T) {
	controller, _, _ := newTestController(t)
	cluster := testSeedCluster("42")
	if _, err := controller.reconcile("", cluster); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}
	first := certPEM(t, controller)

	resolver := controller.opts.Resolver.(*fakeResolver)
	resolver.ips = []string{"203.0.113.11"}
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
