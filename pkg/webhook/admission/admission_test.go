package admission

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestRewriteImage(t *testing.T) {
	rewriter := New(Config{ProxyDomain: "proxy.example.com"})
	tests := []struct {
		in      string
		want    string
		changed bool
	}{
		{"registry.gitlab.com/gitlab-org/cli:v1.104.0", "proxy.example.com/registry.gitlab.com/gitlab-org/cli:v1.104.0", true},
		{"docker.io/library/nginx:1.27", "proxy.example.com/docker.io/library/nginx:1.27", true},
		{"ghcr.io/gorizond/d7y:v1", "proxy.example.com/ghcr.io/gorizond/d7y:v1", true},
		{"quay.io/org/image:v1", "proxy.example.com/quay.io/org/image:v1", true},
		{"registry.example.org/team/app:v2", "proxy.example.com/registry.example.org/team/app:v2", true},
		{"registry.gitlab.com/gitlab-org/cli:v1.104.0@sha256:abc", "proxy.example.com/registry.gitlab.com/gitlab-org/cli:v1.104.0@sha256:abc", true},
		{"registry.gitlab.com/gitlab-org/cli@sha256:abc", "proxy.example.com/registry.gitlab.com/gitlab-org/cli@sha256:abc", true},
		{"nginx:1.27", "proxy.example.com/docker.io/library/nginx:1.27", true},
		{"library/nginx:1.27", "proxy.example.com/docker.io/library/nginx:1.27", true},
		{"nginx@sha256:abc", "proxy.example.com/docker.io/library/nginx@sha256:abc", true},
		{"proxy.example.com/registry.gitlab.com/gitlab-org/cli:v1", "proxy.example.com/registry.gitlab.com/gitlab-org/cli:v1", false},
		{"harbor.example.com/library/nginx:v1", "harbor.example.com/library/nginx:v1", false},
	}
	for _, test := range tests {
		got, changed := rewriter.RewriteImage(test.in)
		if got != test.want || changed != test.changed {
			t.Errorf("RewriteImage(%q) = %q, %t; want %q, %t", test.in, got, changed, test.want, test.changed)
		}
	}
}

func TestPatchPodRewritesAllContainerKinds(t *testing.T) {
	rewriter := New(Config{ProxyDomain: "proxy.example.com", NetworkOnly: true})
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers:     []corev1.Container{{Name: "app", Image: "docker.io/library/nginx:1"}},
		InitContainers: []corev1.Container{{Name: "init", Image: "ghcr.io/org/init:1"}},
		EphemeralContainers: []corev1.EphemeralContainer{{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "registry.gitlab.com/org/debug:1"},
		}},
	}}
	patch, err := rewriter.PatchPod(pod)
	if err != nil {
		t.Fatal(err)
	}
	var operations []patchOperation
	if err := json.Unmarshal(patch, &operations); err != nil {
		t.Fatalf("patch is not a JSONPatch: %v", err)
	}
	want := map[string]string{
		"/spec/containers/0/image":          "proxy.example.com/docker.io/library/nginx:1",
		"/spec/initContainers/0/image":      "proxy.example.com/ghcr.io/org/init:1",
		"/spec/ephemeralContainers/0/image": "proxy.example.com/registry.gitlab.com/org/debug:1",
	}
	for _, operation := range operations {
		if operation.Op != "replace" {
			t.Errorf("op = %q, want replace", operation.Op)
		}
		if want[operation.Path] != operation.Value {
			t.Errorf("%s = %v, want %s", operation.Path, operation.Value, want[operation.Path])
		}
		delete(want, operation.Path)
	}
	if len(want) != 0 {
		t.Errorf("missing patch paths: %v", want)
	}
}

func TestPatchPodNoop(t *testing.T) {
	rewriter := New(Config{ProxyDomain: "proxy.example.com"})
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: "proxy", Image: "proxy.example.com/docker.io/library/nginx:1"},
			{Name: "harbor", Image: "harbor.example.com/library/nginx:1"},
		},
	}}
	patch, err := rewriter.PatchPod(pod)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 0 {
		t.Errorf("no-op patch = %s, want empty", patch)
	}
}

func TestPatchPodInjectsCredentials(t *testing.T) {
	rewriter := New(Config{
		ProxyDomain:        "proxy.example.com",
		ImagePullSecret:    "proxy-pull",
		ServiceAccountName: "proxy-sa",
	})
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app", Image: "ghcr.io/org/app:1"}},
	}}
	patch, err := rewriter.PatchPod(pod)
	if err != nil {
		t.Fatal(err)
	}
	var operations []patchOperation
	if err := json.Unmarshal(patch, &operations); err != nil {
		t.Fatal(err)
	}
	foundSecret, foundSA := false, false
	for _, operation := range operations {
		switch operation.Path {
		case "/spec/imagePullSecrets":
			foundSecret = true
		case "/spec/serviceAccountName":
			foundSA = true
		}
	}
	if !foundSecret || !foundSA {
		t.Errorf("patch = %s, want imagePullSecrets and serviceAccountName", patch)
	}

	// A second admission of the already-patched pod is a complete no-op.
	pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "proxy-pull"}}
	pod.Spec.ServiceAccountName = "proxy-sa"
	patch, err = rewriter.PatchPod(pod)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 0 {
		t.Errorf("second patch = %s, want empty", patch)
	}
}

func TestPatchPodNetworkOnlySkipsCredentials(t *testing.T) {
	rewriter := New(Config{ProxyDomain: "proxy.example.com", ImagePullSecret: "proxy-pull", NetworkOnly: true})
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app", Image: "ghcr.io/org/app:1"}},
	}}
	patch, err := rewriter.PatchPod(pod)
	if err != nil {
		t.Fatal(err)
	}
	var operations []patchOperation
	if err := json.Unmarshal(patch, &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Path != "/spec/containers/0/image" {
		t.Errorf("network-only patch = %s, want only the image rewrite", patch)
	}
}

func TestServeHTTPExcludedNamespace(t *testing.T) {
	rewriter := New(Config{ProxyDomain: "proxy.example.com"})
	pod := corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "ghcr.io/org/app:1"}}}}
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	review := admissionv1.AdmissionReview{Request: &admissionv1.AdmissionRequest{
		UID: "uid", Operation: admissionv1.Create, Namespace: "cattle-monitoring-system", Object: runtime.RawExtension{Raw: raw},
	}}
	body, _ := json.Marshal(review)
	recorder := httptest.NewRecorder()
	rewriter.ServeHTTP(recorder, httptest.NewRequest("POST", "/mutate", bytes.NewReader(body)))
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response admissionv1.AdmissionReview
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Response == nil || !response.Response.Allowed || len(response.Response.Patch) != 0 {
		t.Errorf("response = %+v, want allowed without patch", response.Response)
	}
}

func TestPatchPodRewritesAnyRegistryAndDockerHub(t *testing.T) {
	rewriter := New(Config{ProxyDomain: "proxy.example.com", NetworkOnly: true})
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: "quay", Image: "quay.io/org/image:v1"},
			{Name: "hub", Image: "nginx:1.27"},
		},
	}}
	patch, err := rewriter.PatchPod(pod)
	if err != nil {
		t.Fatal(err)
	}
	var operations []patchOperation
	if err := json.Unmarshal(patch, &operations); err != nil {
		t.Fatalf("patch is not a JSONPatch: %v", err)
	}
	want := map[string]string{
		"/spec/containers/0/image": "proxy.example.com/quay.io/org/image:v1",
		"/spec/containers/1/image": "proxy.example.com/docker.io/library/nginx:1.27",
	}
	for _, operation := range operations {
		if operation.Value != want[operation.Path] {
			t.Errorf("%s = %v, want %s", operation.Path, operation.Value, want[operation.Path])
		}
		delete(want, operation.Path)
	}
	if len(want) != 0 {
		t.Errorf("missing patch paths: %v", want)
	}
}
