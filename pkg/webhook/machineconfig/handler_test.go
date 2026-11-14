package machineconfig

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	wranglerwebhook "github.com/rancher/wrangler/v3/pkg/webhook"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const testUserData = "#cloud-config\nruncmd:\n  - echo hi\n"

func TestCreateInjectsPayload(t *testing.T) {
	handler, _, _ := newTestHandler()
	req := testRequest(admissionv1.Create, "c1-pool1", "Amazonec2Config", testUserData, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if !resp.Allowed {
		t.Fatal("response is not allowed")
	}
	userData := patchedUserData(t, resp)
	if !strings.Contains(userData, "d7y-cattle-operator:p2p-install") {
		t.Errorf("patched userData = %q", userData)
	}
	if !strings.Contains(userData, "echo hi") {
		t.Errorf("existing runcmd was lost: %q", userData)
	}

	cfg := extractCFG(t, userData)
	raw, err := base64.URLEncoding.DecodeString(cfg)
	if err != nil {
		t.Fatalf("decode cfg: %v", err)
	}
	env := string(raw)
	if !strings.Contains(env, "MANAGER_GRPC=https://grpc.example.com:65003") ||
		!strings.Contains(env, "STATIC_URL=http://static.example.com") ||
		!strings.Contains(env, "SCHEDULER_CLUSTER_ID=2") ||
		!strings.Contains(env, "CA_CRT_B64=") {
		t.Errorf("payload env = %q", env)
	}
}

func TestUpdateNeverPatches(t *testing.T) {
	handler, _, _ := newTestHandler()
	req := testRequest(admissionv1.Update, "c1-pool1", "Amazonec2Config", testUserData, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if len(resp.Patch) != 0 {
		t.Errorf("UPDATE returned patch %s, want none", resp.Patch)
	}
}

func TestNotApplicableNoPatch(t *testing.T) {
	handler, clusters, _ := newTestHandler()
	clusters.clusters["fleet-default/c1"].Labels = map[string]string{}
	req := testRequest(admissionv1.Create, "c1-pool1", "Amazonec2Config", testUserData, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if len(resp.Patch) != 0 {
		t.Errorf("unrelated cluster returned patch %s", resp.Patch)
	}
}

func TestConfigAnnotationApplicability(t *testing.T) {
	handler, clusters, _ := newTestHandler()
	clusters.clusters["fleet-default/c1"].Labels = map[string]string{}
	req := testRequest(admissionv1.Create, "c1-pool1", "Amazonec2Config", testUserData,
		map[string]string{LabelEnabled: "true"},
		map[string]string{AnnotationSeed: "true", AnnotationSchedulerCluster: "7"})
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	userData := patchedUserData(t, resp)
	cfg := extractCFG(t, userData)
	raw, err := base64.URLEncoding.DecodeString(cfg)
	if err != nil {
		t.Fatalf("decode cfg: %v", err)
	}
	if !strings.Contains(string(raw), "SCHEDULER_CLUSTER_ID=7") {
		t.Errorf("payload env = %q", raw)
	}
}

func TestRepeatInjectionNoPatch(t *testing.T) {
	handler, _, _ := newTestHandler()
	already := testUserData + "  - curl -sfL \"http://static.example.com/p2p-install.sh?cfg=x\" | bash  # d7y-cattle-operator:p2p-install\n"
	req := testRequest(admissionv1.Create, "c1-pool1", "Amazonec2Config", already, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if len(resp.Patch) != 0 {
		t.Errorf("already-injected userData returned patch %s", resp.Patch)
	}
}

func TestBase64UserDataPatched(t *testing.T) {
	handler, _, _ := newTestHandler()
	encoded := base64.StdEncoding.EncodeToString([]byte(testUserData))
	req := testRequest(admissionv1.Create, "c1-pool1", "Amazonec2Config", encoded, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	patched := patchedUserData(t, resp)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(patched))
	if err != nil {
		t.Fatalf("patched userData is not base64: %v", err)
	}
	if !strings.Contains(string(decoded), "d7y-cattle-operator:p2p-install") {
		t.Errorf("decoded patch = %q", decoded)
	}
}

func TestOversizePayloadNoPatch(t *testing.T) {
	handler, _, events := newTestHandler()
	handler.Secrets.(*fakeSecrets).secret.Data["ca.crt"] = []byte(strings.Repeat("ca", 9000))
	req := testRequest(admissionv1.Create, "c1-pool1", "Amazonec2Config", testUserData, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if len(resp.Patch) != 0 {
		t.Errorf("oversize payload returned patch %s", resp.Patch)
	}
	if len(events.events) != 1 || events.events[0].Reason != "PayloadBuildFailed" {
		t.Errorf("events = %+v", events.events)
	}
}

func TestNonConfigKindNoPatch(t *testing.T) {
	handler, _, _ := newTestHandler()
	req := testRequest(admissionv1.Create, "c1-pool1", "SomethingElse", testUserData, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if len(resp.Patch) != 0 {
		t.Errorf("non-Config kind returned patch %s", resp.Patch)
	}
}

func TestCustomNodeConfigKeyPostBootScript(t *testing.T) {
	handler, clusters, _ := newTestHandler()
	clusters.clusters["fleet-default/c1"].Annotations[AnnotationNodeConfigKey] = "postBootScript"
	raw := "#!/bin/bash\necho hello\n"
	req := testRequestWithNodeConfig(admissionv1.Create, "c1-pool1", "CloudblastConfig", "postBootScript", raw, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	patched := patchedNodeConfig(t, resp, "postBootScript")
	if !strings.HasPrefix(patched, raw) {
		t.Errorf("existing postBootScript was modified: %q", patched)
	}
	if !strings.Contains(patched, "d7y-cattle-operator:p2p-install") {
		t.Errorf("patched postBootScript = %q", patched)
	}
	if strings.Contains(patched, "runcmd:") {
		t.Errorf("postBootScript was converted to cloud-config: %q", patched)
	}
}

func TestInvalidNodeConfigKeyNoPatch(t *testing.T) {
	handler, clusters, events := newTestHandler()
	clusters.clusters["fleet-default/c1"].Annotations[AnnotationNodeConfigKey] = "bad-key"
	req := testRequest(admissionv1.Create, "c1-pool1", "Amazonec2Config", testUserData, nil, nil)
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if len(resp.Patch) != 0 {
		t.Errorf("invalid node-config key returned patch %s", resp.Patch)
	}
	if len(events.events) != 1 || events.events[0].Reason != "NodeConfigKeyInvalid" {
		t.Errorf("events = %+v", events.events)
	}
}

func TestAbsentClusterDefaultsToUserData(t *testing.T) {
	handler, _, _ := newTestHandler()
	req := testRequest(admissionv1.Create, "other-pool1", "Amazonec2Config", testUserData, nil,
		map[string]string{AnnotationSeed: "true", AnnotationSchedulerCluster: "2"})
	resp := &wranglerwebhook.Response{AdmissionResponse: admissionv1.AdmissionResponse{Allowed: true}}

	if err := handler.Admit(resp, req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	userData := patchedUserData(t, resp)
	if !strings.Contains(userData, "d7y-cattle-operator:p2p-install") {
		t.Errorf("default userData was not patched: %q", userData)
	}
}

func newTestHandler() (*Handler, *fakeClusters, *fakeEvents) {
	clusters := &fakeClusters{clusters: map[string]*provv1.Cluster{
		"fleet-default/c1": {
			ObjectMeta: metav1.ObjectMeta{
				Name:      "c1",
				Namespace: "fleet-default",
				Labels:    map[string]string{LabelEnabled: "true"},
				Annotations: map[string]string{
					AnnotationSeed:             "true",
					AnnotationSchedulerCluster: "2",
				},
			},
		},
	}}
	secrets := &fakeSecrets{secret: &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Data: map[string][]byte{
			"ca.crt":                []byte("ca"),
			corev1.TLSCertKey:       []byte("cert"),
			corev1.TLSPrivateKeyKey: []byte("key"),
		},
	}}
	events := &fakeEvents{}
	return &Handler{
		Clusters:          clusters,
		Secrets:           secrets,
		Events:            events,
		Namespace:         "dragonfly-system",
		MTLSSecretName:    "dragonfly-p2p-mtls",
		ManagerGRPCDomain: "grpc.example.com",
		StaticURL:         "http://static.example.com",
	}, clusters, events
}

func testRequest(operation admissionv1.Operation, name, kind, userData string, extraLabels, extraAnnotations map[string]string) *wranglerwebhook.Request {
	return testRequestWithNodeConfig(operation, name, kind, "userData", userData, extraLabels, extraAnnotations)
}

func testRequestWithNodeConfig(operation admissionv1.Operation, name, kind, nodeConfigKey, nodeConfig string, extraLabels, extraAnnotations map[string]string) *wranglerwebhook.Request {
	labelsMap := map[string]any{"p2p-dragonfly-io": "true"}
	annotations := map[string]any{}
	for key, value := range extraLabels {
		labelsMap[key] = value
	}
	for key, value := range extraAnnotations {
		annotations[key] = value
	}
	object := map[string]any{
		"apiVersion": MachineConfigGroup + "/" + MachineConfigVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":        name,
			"namespace":   "fleet-default",
			"labels":      labelsMap,
			"annotations": annotations,
		},
		"spec": map[string]any{nodeConfigKey: nodeConfig},
	}
	raw, _ := json.Marshal(object)
	return &wranglerwebhook.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:       "uid",
			Operation: operation,
			Kind:      metav1.GroupVersionKind{Group: MachineConfigGroup, Version: MachineConfigVersion, Kind: kind},
			Resource:  metav1.GroupVersionResource{Group: MachineConfigGroup, Version: MachineConfigVersion, Resource: "amazonec2configs"},
			Namespace: "fleet-default",
			Name:      name,
			Object:    runtime.RawExtension{Raw: raw},
		},
		ObjTemplate: &unstructured.Unstructured{},
	}
}

func patchedUserData(t *testing.T, resp *wranglerwebhook.Response) string {
	t.Helper()
	return patchedNodeConfig(t, resp, "userData")
}

func patchedNodeConfig(t *testing.T, resp *wranglerwebhook.Response, key string) string {
	t.Helper()
	if len(resp.Patch) == 0 {
		t.Fatal("response has no patch")
	}
	var patch map[string]any
	if err := json.Unmarshal(resp.Patch, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	spec, ok := patch["spec"].(map[string]any)
	if !ok {
		t.Fatalf("patch spec = %#v", patch["spec"])
	}
	nodeConfig, ok := spec[key].(string)
	if !ok {
		t.Fatalf("patch %s = %#v", key, spec[key])
	}
	return nodeConfig
}

func extractCFG(t *testing.T, userData string) string {
	t.Helper()
	const marker = "cfg="
	start := strings.Index(userData, marker)
	if start < 0 {
		t.Fatalf("cfg not found in %q", userData)
	}
	value := userData[start+len(marker):]
	end := strings.IndexAny(value, "\"& ")
	if end >= 0 {
		value = value[:end]
	}
	return value
}

type fakeClusters struct {
	clusters map[string]*provv1.Cluster
}

func (f *fakeClusters) Get(namespace, name string) (*provv1.Cluster, error) {
	cluster, ok := f.clusters[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "provisioning.cattle.io", Resource: "clusters"}, name)
	}
	return cluster, nil
}

func (f *fakeClusters) List(namespace string, _ labels.Selector) ([]*provv1.Cluster, error) {
	var result []*provv1.Cluster
	for _, cluster := range f.clusters {
		if cluster.Namespace == namespace {
			result = append(result, cluster)
		}
	}
	return result, nil
}

type fakeSecrets struct {
	secret *corev1.Secret
}

func (f *fakeSecrets) Get(namespace, name string) (*corev1.Secret, error) {
	if f.secret == nil || f.secret.Namespace != namespace || f.secret.Name != name {
		return nil, apierrors.NewNotFound(corev1.Resource("secrets"), name)
	}
	return f.secret, nil
}

type fakeEvents struct {
	events []*corev1.Event
}

func (f *fakeEvents) Create(event *corev1.Event) (*corev1.Event, error) {
	f.events = append(f.events, event)
	return event, nil
}
