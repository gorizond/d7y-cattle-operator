package webhook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorChartWebhookContract(t *testing.T) {
	root := filepath.Join("..", "..", "charts", "d7y-cattle-operator")
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	webhook := read("templates/webhook.yaml")
	for _, want := range []string{
		`operations: ["CREATE"]`,
		`resources: ["*"]`,
		`sideEffects: None`,
		`failurePolicy:`,
		`timeoutSeconds: 10`,
		`admissionReviewVersions: ["v1"]`,
	} {
		if !strings.Contains(webhook, want) {
			t.Errorf("webhook template lacks %q", want)
		}
	}

	service := read("templates/service.yaml")
	if !strings.Contains(service, "need-a-cert.cattle.io/secret-name") {
		t.Error("webhook Service lacks the needacert annotation")
	}

	deployment := read("templates/deployment.yaml")
	for _, want := range []string{
		"WEBHOOK_CERT_DIR",
		"WEBHOOK_SERVICE_NAME",
		"webhook-certs",
		"optional: true",
	} {
		if !strings.Contains(deployment, want) {
			t.Errorf("deployment lacks %q", want)
		}
	}

	values := read("values.yaml")
	if !strings.Contains(values, "enabled: true") || !strings.Contains(values, "certDir: /etc/webhook/certs") {
		t.Error("chart values do not enable the webhook and cert mount")
	}
}

func TestAdmissionWebhookChartContract(t *testing.T) {
	root := filepath.Join("..", "..", "charts", "admission-webhook")
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	webhook := read("templates/webhook.yaml")
	for _, want := range []string{
		`$serviceName := .Release.Name`,
		`(printf "%s.%s.svc" $serviceName .Release.Namespace)`,
		`(printf "%s.%s.svc.cluster.local" $serviceName .Release.Namespace)`,
		`d7y.gorizond.io/tls-sans`,
		`- key: kubernetes.io/metadata.name`,
		`with .Values.namespaceSelector.excludedNamespaces`,
		`- {{ .Release.Namespace | quote }}`,
		`name: {{ .Release.Name }}`,
	} {
		if !strings.Contains(webhook, want) {
			t.Errorf("admission webhook template lacks %q", want)
		}
	}

	service := read("templates/service.yaml")
	if !strings.Contains(service, "name: {{ .Release.Name }}") {
		t.Error("admission Service name is not .Release.Name")
	}
	if strings.Contains(service, "-tls") {
		t.Error("admission chart must not render an alias TLS Service")
	}

	values := read("values.yaml")
	if !strings.Contains(values, "excludedNamespaces: []") {
		t.Error("admission chart values lack the empty excludedNamespaces default")
	}
}
