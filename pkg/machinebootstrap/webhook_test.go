package machinebootstrap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	jsonpatch "github.com/evanphx/json-patch"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

type fakeSecrets map[string]*corev1.Secret

func (f fakeSecrets) Get(namespace, name string) (*corev1.Secret, error) {
	if secret, ok := f[namespace+"/"+name]; ok {
		return secret.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(corev1.Resource("secrets"), name)
}

type fakeMachines map[string]*capiv1beta2.Machine

func (f fakeMachines) Get(namespace, name string) (*capiv1beta2.Machine, error) {
	if machine, ok := f[namespace+"/"+name]; ok {
		return machine.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "cluster.x-k8s.io", Resource: "machines"}, name)
}

type fakeClusters map[string]*provv1.Cluster

func (f fakeClusters) Get(namespace, name string) (*provv1.Cluster, error) {
	if cluster, ok := f[namespace+"/"+name]; ok {
		return cluster.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "provisioning.cattle.io", Resource: "clusters"}, name)
}

type webhookFixture struct {
	webhook  *Webhook
	secret   *corev1.Secret
	secrets  fakeSecrets
	machines fakeMachines
	clusters fakeClusters
}

func newWebhookFixture() *webhookFixture {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-1-machine-bootstrap", Namespace: "cluster-ns"},
		Type:       bootstrapSecretType,
		Data: map[string][]byte{
			bootstrapValueKey: []byte("#!/bin/bash\necho start\ndo_install \"$@\"\nexit 0\n"),
		},
	}
	mtls := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "p2p-mtls", Namespace: "operator-system"},
		Data: map[string][]byte{
			"ca.crt":                []byte("ca"),
			corev1.TLSCertKey:       []byte("cert"),
			corev1.TLSPrivateKeyKey: []byte("key"),
		},
	}
	machine := &capiv1beta2.Machine{ObjectMeta: metav1.ObjectMeta{
		Name: "machine-1", Namespace: "cluster-ns",
		Labels: map[string]string{capiClusterName: "cluster-1"},
	}}
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Name: "cluster-1", Namespace: "cluster-ns",
		Labels: map[string]string{labelEnabled: "true"},
		Annotations: map[string]string{
			annotationSeed:                         "true",
			bootstrap.SchedulerClusterIDAnnotation: "2",
		},
	}}
	builder := NewBuilder(
		fakeSecrets{"operator-system/p2p-mtls": mtls},
		fakeMachines{"cluster-ns/machine-1": machine},
		fakeClusters{"cluster-ns/cluster-1": cluster},
		Options{
			OperatorNamespace: "operator-system",
			MTLSSecretName:    "p2p-mtls",
			ManagerGRPCDomain: "manager.example.com",
			ManagerGRPCPort:   443,
			StaticURL:         "https://static.example.com",
		},
	)
	return &webhookFixture{
		webhook:  NewWebhook(builder),
		secret:   secret,
		secrets:  builder.mtls.(fakeSecrets),
		machines: builder.machines.(fakeMachines),
		clusters: builder.clusters.(fakeClusters),
	}
}

func admissionReviewBody(t *testing.T, secret *corev1.Secret, operation admissionv1.Operation) []byte {
	t.Helper()
	raw, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("marshal Secret: %v", err)
	}
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       types.UID("test-uid"),
			Kind:      metav1.GroupVersionKind{Version: "v1", Kind: "Secret"},
			Resource:  metav1.GroupVersionResource{Version: "v1", Resource: "secrets"},
			Operation: operation,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("marshal AdmissionReview: %v", err)
	}
	return body
}

func runWebhook(t *testing.T, webhook *Webhook, body []byte) (*httptest.ResponseRecorder, *admissionv1.AdmissionReview) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
	webhook.ServeHTTP(recorder, request)
	review := &admissionv1.AdmissionReview{}
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), review); err != nil {
			t.Fatalf("decode response: %v\n%s", err, recorder.Body.String())
		}
	}
	return recorder, review
}

func mutate(t *testing.T, webhook *Webhook, secret *corev1.Secret) *admissionv1.AdmissionResponse {
	t.Helper()
	recorder, review := runWebhook(t, webhook, admissionReviewBody(t, secret, admissionv1.Create))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if review.Response == nil {
		t.Fatal("response is nil")
	}
	return review.Response
}

func applyPatch(t *testing.T, secret *corev1.Secret, patch []byte) *corev1.Secret {
	t.Helper()
	original, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("marshal original: %v", err)
	}
	decoded, err := jsonpatch.DecodePatch(patch)
	if err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	patched, err := decoded.Apply(original)
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	updated := &corev1.Secret{}
	if err := json.Unmarshal(patched, updated); err != nil {
		t.Fatalf("decode patched Secret: %v", err)
	}
	return updated
}

func TestWebhookInjectsP2PBlock(t *testing.T) {
	fixture := newWebhookFixture()
	response := mutate(t, fixture.webhook, fixture.secret)
	if !response.Allowed {
		t.Fatalf("allowed = false: %+v", response.Result)
	}
	if len(response.Patch) == 0 || response.PatchType == nil || *response.PatchType != admissionv1.PatchTypeJSONPatch {
		t.Fatalf("patch = %q, patchType = %v", response.Patch, response.PatchType)
	}
	updated := applyPatch(t, fixture.secret, response.Patch)
	value := string(updated.Data[bootstrapValueKey])
	blockAt := strings.Index(value, bootstrap.Marker)
	doInstallAt := strings.Index(value, `do_install "$@"`)
	if blockAt < 0 || doInstallAt < 0 || blockAt > doInstallAt {
		t.Fatalf("bootstrap block is not before do_install: %q", value)
	}
	for _, want := range []string{
		"set -a\n",
		"set +a\n",
		`curl -sfL "https://static.example.com/p2p-install.sh" | bash || true`,
		"MANAGER_GRPC='https://manager.example.com:443'",
		"P2P_DISTRO='k3s'",
		"SCHEDULER_IPS=''",
		"SCHEDULER_CLUSTER_ID='2'",
	} {
		if !strings.Contains(value, want) {
			t.Fatalf("bootstrap value missing %q: %q", want, value)
		}
	}
	if updated.Annotations[bootstrap.ClientCertRevisionAnnotation] == "" {
		t.Error("client revision annotation is empty")
	}
	if updated.Annotations[bootstrap.BootstrapInputsAnnotation] == "" {
		t.Error("bootstrap inputs annotation is empty")
	}
}

func TestWebhookIsIdempotent(t *testing.T) {
	fixture := newWebhookFixture()
	first := mutate(t, fixture.webhook, fixture.secret)
	if len(first.Patch) == 0 {
		t.Fatal("first patch is empty")
	}
	updated := applyPatch(t, fixture.secret, first.Patch)

	second := mutate(t, fixture.webhook, updated)
	if !second.Allowed {
		t.Fatalf("allowed = false: %+v", second.Result)
	}
	if len(second.Patch) != 0 {
		t.Fatalf("second patch = %q, want a no-op", second.Patch)
	}
	if got := string(updated.Data[bootstrapValueKey]); strings.Count(got, bootstrap.Marker+"\n") != 1 {
		t.Fatalf("p2p marker count = %d, want 1", strings.Count(got, bootstrap.Marker+"\n"))
	}
}

func TestWebhookHandlesTruncatedNameViaOwnerReference(t *testing.T) {
	fixture := newWebhookFixture()
	fixture.secret.Name = "long-cluster-name-control-plane-abc12-machine-bootstrap-4f2a1"
	fixture.secret.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "rke.cattle.io/v1",
		Kind:       "RKEBootstrap",
		Name:       "machine-1",
	}}
	response := mutate(t, fixture.webhook, fixture.secret)
	if !response.Allowed {
		t.Fatalf("allowed = false: %+v", response.Result)
	}
	if len(response.Patch) == 0 {
		t.Fatal("patch is empty, want the p2p block for a truncated name")
	}
	updated := applyPatch(t, fixture.secret, response.Patch)
	if !strings.Contains(string(updated.Data[bootstrapValueKey]), bootstrap.Marker) {
		t.Fatalf("bootstrap block missing: %q", updated.Data[bootstrapValueKey])
	}
}

func TestWebhookIgnoresNotApplicableSecrets(t *testing.T) {
	tests := map[string]func(*webhookFixture){
		"wrong type": func(f *webhookFixture) {
			f.secret.Type = corev1.SecretTypeOpaque
		},
		"no name suffix": func(f *webhookFixture) {
			f.secret.Name = "some-other-secret"
		},
		"non-gated cluster": func(f *webhookFixture) {
			f.clusters["cluster-ns/cluster-1"].Annotations[annotationSeed] = "false"
		},
		"machine without cluster label": func(f *webhookFixture) {
			f.machines["cluster-ns/machine-1"].Labels = nil
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newWebhookFixture()
			prepare(fixture)
			response := mutate(t, fixture.webhook, fixture.secret)
			if !response.Allowed {
				t.Fatalf("allowed = false: %+v", response.Result)
			}
			if len(response.Patch) != 0 {
				t.Fatalf("patch = %q, want a no-op", response.Patch)
			}
		})
	}
}

func TestWebhookCreatesMissingDataField(t *testing.T) {
	fixture := newWebhookFixture()
	fixture.secret.Data = nil
	response := mutate(t, fixture.webhook, fixture.secret)
	if !response.Allowed || len(response.Patch) == 0 {
		t.Fatalf("response = %+v, want an allowed patch", response)
	}
	updated := applyPatch(t, fixture.secret, response.Patch)
	if !strings.Contains(string(updated.Data[bootstrapValueKey]), bootstrap.Marker) {
		t.Fatalf("bootstrap block missing after creating data: %q", updated.Data[bootstrapValueKey])
	}
}

func TestWebhookDeniesMalformedInput(t *testing.T) {
	fixture := newWebhookFixture()

	t.Run("invalid admission review", func(t *testing.T) {
		recorder, _ := runWebhook(t, fixture.webhook, []byte("{"))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
	})
	t.Run("missing request", func(t *testing.T) {
		recorder, _ := runWebhook(t, fixture.webhook, []byte(`{"apiVersion":"admission.k8s.io/v1","kind":"AdmissionReview"}`))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
	})
	t.Run("invalid secret object", func(t *testing.T) {
		review := admissionv1.AdmissionReview{
			TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
			Request: &admissionv1.AdmissionRequest{
				UID:    types.UID("test-uid"),
				Object: runtime.RawExtension{Raw: []byte("[]")},
			},
		}
		body, _ := json.Marshal(review)
		recorder, response := runWebhook(t, fixture.webhook, body)
		if recorder.Code != http.StatusOK || response.Response == nil || response.Response.Allowed {
			t.Fatalf("status = %d, response = %+v, want an admission denial", recorder.Code, response.Response)
		}
		if !strings.Contains(response.Response.Result.Message, "decode Secret") {
			t.Fatalf("message = %q", response.Response.Result.Message)
		}
	})
	t.Run("machine not found", func(t *testing.T) {
		delete(fixture.machines, "cluster-ns/machine-1")
		response := mutate(t, fixture.webhook, fixture.secret)
		if response.Allowed {
			t.Fatal("allowed = true, want a denial")
		}
		if !strings.Contains(response.Result.Message, "CAPI Machine") {
			t.Fatalf("message = %q", response.Result.Message)
		}
	})
	t.Run("mtls secret not found", func(t *testing.T) {
		fixture := newWebhookFixture()
		delete(fixture.secrets, "operator-system/p2p-mtls")
		response := mutate(t, fixture.webhook, fixture.secret)
		if response.Allowed {
			t.Fatal("allowed = true, want a denial")
		}
		if !strings.Contains(response.Result.Message, "mTLS Secret") {
			t.Fatalf("message = %q", response.Result.Message)
		}
	})
	t.Run("unrecognized existing marker", func(t *testing.T) {
		fixture := newWebhookFixture()
		fixture.secret.Data[bootstrapValueKey] = []byte("#!/bin/bash\n" + bootstrap.Marker + "\ndo_install \"$@\"\n")
		response := mutate(t, fixture.webhook, fixture.secret)
		if response.Allowed {
			t.Fatal("allowed = true, want a denial")
		}
		if !strings.Contains(response.Result.Message, "unrecognized") {
			t.Fatalf("message = %q", response.Result.Message)
		}
	})
	t.Run("wrong method", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		fixture.webhook.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, WebhookPath, nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", recorder.Code)
		}
	})
}

func TestWebhookInjectsPreinstallBeforeP2P(t *testing.T) {
	fixture := newWebhookFixture()
	fixture.clusters["cluster-ns/cluster-1"].Annotations[bootstrap.PreinstallAnnotation] = "echo pre"
	response := mutate(t, fixture.webhook, fixture.secret)
	updated := applyPatch(t, fixture.secret, response.Patch)
	value := string(updated.Data[bootstrapValueKey])
	if strings.Count(value, bootstrap.PreinstallMarker+"\n") != 1 ||
		strings.Count(value, bootstrap.PreinstallEndMarker+"\n") != 1 {
		t.Fatalf("pre-install markers are missing: %q", value)
	}
	if strings.Index(value, bootstrap.PreinstallMarker) > strings.Index(value, bootstrap.Marker) {
		t.Fatalf("pre-install block is not before the p2p block: %q", value)
	}
	if !strings.Contains(value, "echo pre") {
		t.Fatalf("pre-install body is missing: %q", value)
	}
}

func TestWebhookInvalidPreinstallIsSkipped(t *testing.T) {
	bodies := map[string]string{
		"NUL":          "echo bad\x00",
		"start marker": "echo ok\n" + bootstrap.PreinstallMarker + "\necho bad",
		"end marker":   bootstrap.PreinstallEndMarker,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			fixture := newWebhookFixture()
			fixture.clusters["cluster-ns/cluster-1"].Annotations[bootstrap.PreinstallAnnotation] = body
			response := mutate(t, fixture.webhook, fixture.secret)
			if !response.Allowed || len(response.Patch) == 0 {
				t.Fatalf("response = %+v, want an allowed patch", response)
			}
			value := string(applyPatch(t, fixture.secret, response.Patch).Data[bootstrapValueKey])
			if strings.Contains(value, bootstrap.PreinstallMarker) {
				t.Fatalf("pre-install block was injected for an invalid body: %q", value)
			}
			if strings.Contains(value, "echo bad") {
				t.Fatalf("invalid body leaked into the bootstrap value: %q", value)
			}
			if !strings.Contains(value, bootstrap.Marker) {
				t.Fatalf("p2p block is missing: %q", value)
			}
		})
	}
}

func TestWebhookOversizedPreinstallAnnotationIsSkipped(t *testing.T) {
	fixture := newWebhookFixture()
	body := strings.Repeat("x", bootstrap.MaxPreinstallAnnotationBytes+1)
	fixture.clusters["cluster-ns/cluster-1"].Annotations[bootstrap.PreinstallAnnotation] = body
	response := mutate(t, fixture.webhook, fixture.secret)
	value := string(applyPatch(t, fixture.secret, response.Patch).Data[bootstrapValueKey])
	if strings.Contains(value, bootstrap.PreinstallMarker) {
		t.Fatalf("pre-install block was injected for an oversized annotation: %q", value)
	}
	if strings.Contains(value, body) {
		t.Fatal("oversized annotation was truncated into the bootstrap value")
	}
	if !strings.Contains(value, bootstrap.Marker) {
		t.Fatalf("p2p block is missing: %q", value)
	}
}

func TestWebhookOversizedSerializedSecretSkipsPreinstall(t *testing.T) {
	fixture := newWebhookFixture()
	base := "#!/bin/bash\n#" + strings.Repeat("x", 620*1024) + "\ndo_install \"$@\"\nexit 0\n"
	fixture.secret.Data[bootstrapValueKey] = []byte(base)
	fixture.clusters["cluster-ns/cluster-1"].Annotations[bootstrap.PreinstallAnnotation] =
		"echo " + strings.Repeat("y", 200*1024)

	response := mutate(t, fixture.webhook, fixture.secret)
	if !response.Allowed || len(response.Patch) == 0 {
		t.Fatalf("response = %+v, want an allowed patch", response)
	}
	updated := applyPatch(t, fixture.secret, response.Patch)
	value := string(updated.Data[bootstrapValueKey])
	if strings.Contains(value, bootstrap.PreinstallMarker) {
		t.Fatal("pre-install block was injected into an oversized Secret")
	}
	if strings.Contains(value, "yyyy") {
		t.Fatal("pre-install body leaked into an oversized Secret")
	}
	if !strings.Contains(value, bootstrap.Marker) {
		t.Fatalf("p2p block is missing: %q", value[:min(len(value), 200)])
	}
	if len(value) < 620*1024 {
		t.Fatalf("bootstrap value was truncated to %d bytes", len(value))
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		t.Fatalf("marshal Secret: %v", err)
	}
	if len(raw) > 1<<20 {
		t.Fatalf("serialized Secret is %d bytes, want <= 1 MiB", len(raw))
	}
}

func TestWebhookMigratesLegacyBlock(t *testing.T) {
	fixture := newWebhookFixture()
	legacy := strings.TrimSuffix(inlineInstallBlockFor("echo legacy\n", "SCHEDULER_IPS=''\n"), bootstrap.EndMarker+"\n")
	fixture.secret.Data[bootstrapValueKey] = []byte("#!/bin/bash\n" + legacy + `do_install "$@"` + "\nexit 0\n")

	response := mutate(t, fixture.webhook, fixture.secret)
	if !response.Allowed || len(response.Patch) == 0 {
		t.Fatalf("response = %+v, want an allowed patch", response)
	}
	value := string(applyPatch(t, fixture.secret, response.Patch).Data[bootstrapValueKey])
	if strings.Contains(value, "echo legacy") {
		t.Errorf("legacy block was not removed: %q", value)
	}
	if !strings.Contains(value, shortInstallSignature) {
		t.Errorf("short block was not inserted: %q", value)
	}
	if strings.Count(value, bootstrap.Marker+"\n") != 1 || strings.Count(value, bootstrap.EndMarker+"\n") != 1 {
		t.Errorf("marker count is not exactly one: %q", value)
	}
	if !strings.Contains(value, `do_install "$@"`+"\n") {
		t.Errorf("do_install line was lost: %q", value)
	}
}

func TestWebhookMissingSchedulerIDUsesPlaceholder(t *testing.T) {
	fixture := newWebhookFixture()
	delete(fixture.clusters["cluster-ns/cluster-1"].Annotations, bootstrap.SchedulerClusterIDAnnotation)
	response := mutate(t, fixture.webhook, fixture.secret)
	value := string(applyPatch(t, fixture.secret, response.Patch).Data[bootstrapValueKey])
	if !strings.Contains(value, "SCHEDULER_CLUSTER_ID='0'\n") {
		t.Fatalf("bootstrap env is missing the scheduler ID placeholder: %q", value)
	}
	if !strings.Contains(value, bootstrap.Marker) {
		t.Fatalf("p2p block is missing: %q", value)
	}
}

func TestWebhookRebuildsBlockOnClientRevisionChange(t *testing.T) {
	fixture := newWebhookFixture()
	first := mutate(t, fixture.webhook, fixture.secret)
	updated := applyPatch(t, fixture.secret, first.Patch)
	previousRevision := updated.Annotations[bootstrap.ClientCertRevisionAnnotation]
	if previousRevision == "" {
		t.Fatal("client revision annotation is empty")
	}

	fixture.secrets["operator-system/p2p-mtls"].Data[corev1.TLSCertKey] = []byte("rotated-cert")
	second := mutate(t, fixture.webhook, updated)
	if !second.Allowed || len(second.Patch) == 0 {
		t.Fatalf("response = %+v, want an allowed patch after rotation", second)
	}
	rotated := applyPatch(t, updated, second.Patch)
	if got := rotated.Annotations[bootstrap.ClientCertRevisionAnnotation]; got == previousRevision {
		t.Error("client revision annotation did not change")
	}
	value := string(rotated.Data[bootstrapValueKey])
	if strings.Count(value, bootstrap.Marker+"\n") != 1 || strings.Count(value, bootstrap.EndMarker+"\n") != 1 {
		t.Errorf("bootstrap block markers were not replaced exactly once: %q", value)
	}
}

func TestShortInstallBlockFormat(t *testing.T) {
	block := shortInstallBlock("https://static.example.com/", "A=1\nB=two\n")
	want := bootstrap.Marker + "\n" +
		"set -a\n" +
		"A='1'\n" +
		"B='two'\n" +
		"set +a\n" +
		`curl -sfL "https://static.example.com/p2p-install.sh" | bash || true` + "\n" +
		bootstrap.EndMarker + "\n"
	if block != want {
		t.Fatalf("short block = %q, want %q", block, want)
	}
	if strings.Contains(block, "?cfg=") || strings.Contains(block, "D7Y_P2P_EOF") || strings.Contains(block, "d7y-p2p-install.sh") {
		t.Fatalf("short block still embeds a query or the installer body: %q", block)
	}
}

func TestShortInstallBlockParsesUnderPOSIXShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available")
	}
	block := shortInstallBlock("https://static.example.com", "A=1\n")
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(block)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n rejected the block: %v\n%s", err, out)
	}
}

func TestBootstrapInputsFingerprintTracksPreinstall(t *testing.T) {
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-1", Namespace: "cluster-ns"}}
	base := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "")
	first := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "echo first")
	second := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "echo second")
	if base == first || first == second {
		t.Fatalf("fingerprints do not track the pre-install body: %q %q %q", base, first, second)
	}
	padded := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "  echo first\n")
	if padded != first {
		t.Error("fingerprint is sensitive to surrounding whitespace")
	}
}

func TestBootstrapInputsFingerprintPreservesLegacyEmptyPreinstall(t *testing.T) {
	cluster := &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster-1", Namespace: "cluster-ns",
			Annotations: map[string]string{
				bootstrap.SchedulerIPsAnnotation:       "203.0.113.10",
				bootstrap.SchedulerClusterIDAnnotation: "2",
			},
		},
		Spec: provv1.ClusterSpec{KubernetesVersion: "v1.30.0+rke2r1"},
	}
	legacy := sha256.Sum256([]byte("rev" + "\x00" + "203.0.113.10" + "\x00" + "2" + "\x00" + "rke2" +
		"\x00" + "v1" + "\x00" + "v2" + "\x00" + "v3"))
	want := hex.EncodeToString(legacy[:])
	if got := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", ""); got != want {
		t.Fatalf("empty preinstall fingerprint = %s, want legacy %s", got, want)
	}
	if got := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "echo pre"); got == want {
		t.Fatal("non-empty preinstall fingerprint still matches the legacy hash")
	}
}
