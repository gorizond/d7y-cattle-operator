package cluster

import (
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResolveRoles(t *testing.T) {
	tests := []struct {
		name        string
		labels      map[string]string
		annotations map[string]string
		want        Roles
	}{
		{
			name: "label missing",
			annotations: map[string]string{
				AnnotationManager: "true",
				AnnotationSeed:    "true",
			},
			want: Roles{},
		},
		{
			name:   "label with wrong value",
			labels: map[string]string{LabelEnabled: "yes"},
			want:   Roles{},
		},
		{
			name:   "label only",
			labels: map[string]string{LabelEnabled: "true"},
			want:   Roles{Enabled: true},
		},
		{
			name:        "manager role",
			labels:      map[string]string{LabelEnabled: "true"},
			annotations: map[string]string{AnnotationManager: "true"},
			want:        Roles{Enabled: true, Manager: true},
		},
		{
			name:        "seed role",
			labels:      map[string]string{LabelEnabled: "true"},
			annotations: map[string]string{AnnotationSeed: "true"},
			want:        Roles{Enabled: true, Seed: true},
		},
		{
			name:        "harbor role",
			labels:      map[string]string{LabelEnabled: "true"},
			annotations: map[string]string{AnnotationHarbor: "true", AnnotationHarborDomain: "harbor.example.com"},
			want:        Roles{Enabled: true, Harbor: true, HarborDomain: "harbor.example.com"},
		},
		{
			name:        "proxy role is independent",
			labels:      map[string]string{LabelEnabled: "true"},
			annotations: map[string]string{AnnotationProxy: "true", AnnotationHarborURL: "https://external.example.com"},
			want:        Roles{Enabled: true, Proxy: true, HarborURL: "https://external.example.com"},
		},
		{
			name:   "both roles",
			labels: map[string]string{LabelEnabled: "true"},
			annotations: map[string]string{
				AnnotationManager: "true",
				AnnotationSeed:    "true",
			},
			want: Roles{Enabled: true, Manager: true, Seed: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cluster := &provv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      test.labels,
					Annotations: test.annotations,
				},
			}
			if got := ResolveRoles(cluster); got != test.want {
				t.Errorf("ResolveRoles() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestHarborValidation(t *testing.T) {
	for _, value := range []string{"harbor.example.com", "harbor.example.test"} {
		if err := validateHarborDomain(value); err != nil {
			t.Errorf("validateHarborDomain(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"", "https://harbor.example.com", "harbor.example.com:443", "Bad_Domain"} {
		if err := validateHarborDomain(value); err == nil {
			t.Errorf("validateHarborDomain(%q) = nil, want error", value)
		}
	}
	if err := validateHarborURL("https://harbor.example.com"); err != nil {
		t.Errorf("validateHarborURL() error = %v", err)
	}
	if err := validateHarborURL("http://harbor.example.com"); err == nil {
		t.Error("validateHarborURL(http) = nil, want error")
	}
}

func TestP2PDataTLSEnabled(t *testing.T) {
	tests := map[string]struct {
		annotation string
		want       bool
	}{
		"default":          {want: false},
		"true":             {annotation: "true", want: true},
		"enabled":          {annotation: "enabled", want: true},
		"case insensitive": {annotation: " TRUE ", want: true},
		"false":            {annotation: "false", want: false},
		"disabled":         {annotation: "disabled", want: false},
		"empty":            {annotation: "", want: false},
		"invalid":          {annotation: "yes", want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{AnnotationP2PDataTLS: tc.annotation},
			}}
			if got := p2pDataTLSEnabled(cluster); got != tc.want {
				t.Errorf("p2pDataTLSEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
	if p2pDataTLSEnabled(nil) {
		t.Error("p2pDataTLSEnabled(nil) = true, want false")
	}
}

func TestMonitoringEnabled(t *testing.T) {
	tests := map[string]struct {
		labels      map[string]string
		annotations map[string]string
		want        bool
	}{
		"selector matches label": {
			labels:      map[string]string{"monitoring": "enabled"},
			annotations: map[string]string{AnnotationMonitoring: "monitoring=enabled"},
			want:        true,
		},
		"selector no matching label": {
			labels:      map[string]string{"monitoring": "disabled"},
			annotations: map[string]string{AnnotationMonitoring: "monitoring=enabled"},
			want:        false,
		},
		"label without annotation": {
			labels: map[string]string{"monitoring": "enabled"},
			want:   false,
		},
		"true shortcut": {
			annotations: map[string]string{AnnotationMonitoring: "true"},
			want:        true,
		},
		"enabled shortcut": {
			annotations: map[string]string{AnnotationMonitoring: "enabled"},
			want:        true,
		},
		"invalid selector": {
			labels:      map[string]string{"monitoring": "enabled"},
			annotations: map[string]string{AnnotationMonitoring: "=bad"},
			want:        false,
		},
		"empty annotation": {
			annotations: map[string]string{AnnotationMonitoring: ""},
			want:        false,
		},
		"empty cluster": {want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
				Labels:      tc.labels,
				Annotations: tc.annotations,
			}}
			if got := monitoringEnabled(cluster); got != tc.want {
				t.Errorf("monitoringEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
	if monitoringEnabled(nil) {
		t.Error("monitoringEnabled(nil) = true, want false")
	}
}
