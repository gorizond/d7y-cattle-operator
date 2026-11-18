// Package admission rewrites Pod image references to the smart registry proxy
// and, optionally, injects proxy pull credentials. It is opt-in per cluster
// (p2p.dragonfly.io/admission) and independent from Harbor/proxy/seed.
package admission

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const maxBodyBytes = 4 << 20

// Config is the rewrite/injection policy rendered by the admission chart.
type Config struct {
	// ProxyDomain is the public proxy host prepended to rewritten images.
	ProxyDomain string
	// ImagePullSecret is injected only when the pod pulls through the proxy.
	// Empty disables injection (network-only mode).
	ImagePullSecret string
	// ServiceAccountName is injected when the pod uses the default service
	// account and the value is non-empty.
	ServiceAccountName string
	// NetworkOnly disables all credential injection.
	NetworkOnly bool
	// ExcludedNamespaces are never mutated. "cattle-" is handled as a prefix.
	ExcludedNamespaces []string
}

// Rewriter implements the Pod CREATE mutation.
type Rewriter struct {
	config   Config
	excluded map[string]struct{}
}

// New builds a Rewriter. Any explicit registry and unqualified Docker Hub
// references are rewritten; only proxy/Harbor refs are skipped.
func New(config Config) *Rewriter {
	config.ProxyDomain = strings.TrimSpace(strings.ToLower(config.ProxyDomain))
	excluded := make(map[string]struct{}, len(config.ExcludedNamespaces)+4)
	for _, namespace := range config.ExcludedNamespaces {
		excluded[namespace] = struct{}{}
	}
	for _, namespace := range []string{"kube-system", "fleet-system", "kube-node-lease", "kube-public"} {
		excluded[namespace] = struct{}{}
	}
	return &Rewriter{config: config, excluded: excluded}
}

// RewriteImage returns the proxy image and whether it changed. Explicit
// registries are preserved as the first path segment; unqualified Docker Hub
// references are normalized to docker.io (with the implicit library namespace).
func (r *Rewriter) RewriteImage(image string) (string, bool) {
	if r.config.ProxyDomain == "" {
		return image, false
	}
	registry, _ := imageRegistry(image)
	if isProxyOrHarbor(registry, r.config.ProxyDomain) {
		return image, false
	}
	normalized := normalizeImage(image)
	if normalized == "" {
		return image, false
	}
	return r.config.ProxyDomain + "/" + normalized, true
}

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// PatchPod returns a JSONPatch for the pod or nil when nothing changed.
func (r *Rewriter) PatchPod(pod *corev1.Pod) ([]byte, error) {
	if pod == nil {
		return nil, nil
	}
	operations := make([]patchOperation, 0, 4)
	usesProxy := false

	rewrite := func(path string, image *string) {
		registry, _ := imageRegistry(*image)
		if isProxyOrHarbor(registry, r.config.ProxyDomain) {
			usesProxy = true
			return
		}
		if rewritten, changed := r.RewriteImage(*image); changed {
			*image = rewritten
			usesProxy = true
			operations = append(operations, patchOperation{Op: "replace", Path: path, Value: rewritten})
		}
	}

	for i := range pod.Spec.Containers {
		rewrite(fmt.Sprintf("/spec/containers/%d/image", i), &pod.Spec.Containers[i].Image)
	}
	for i := range pod.Spec.InitContainers {
		rewrite(fmt.Sprintf("/spec/initContainers/%d/image", i), &pod.Spec.InitContainers[i].Image)
	}
	for i := range pod.Spec.EphemeralContainers {
		rewrite(fmt.Sprintf("/spec/ephemeralContainers/%d/image", i), &pod.Spec.EphemeralContainers[i].Image)
	}

	if usesProxy && !r.config.NetworkOnly {
		if r.config.ImagePullSecret != "" && !hasPullSecret(pod, r.config.ImagePullSecret) {
			secret := corev1.LocalObjectReference{Name: r.config.ImagePullSecret}
			if len(pod.Spec.ImagePullSecrets) == 0 {
				operations = append(operations, patchOperation{Op: "add", Path: "/spec/imagePullSecrets", Value: []corev1.LocalObjectReference{secret}})
			} else {
				operations = append(operations, patchOperation{Op: "add", Path: "/spec/imagePullSecrets/-", Value: secret})
			}
		}
		if r.config.ServiceAccountName != "" {
			switch pod.Spec.ServiceAccountName {
			case "", "default":
				operations = append(operations, patchOperation{Op: "add", Path: "/spec/serviceAccountName", Value: r.config.ServiceAccountName})
			}
		}
	}

	if len(operations) == 0 {
		return nil, nil
	}
	return json.Marshal(operations)
}

// ServeHTTP answers AdmissionReview requests on the configured path.
func (r *Rewriter) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read admission review", http.StatusBadRequest)
		return
	}
	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		http.Error(w, "decode admission review", http.StatusBadRequest)
		return
	}
	response := admissionv1.AdmissionReview{TypeMeta: review.TypeMeta}
	if review.Request == nil {
		http.Error(w, "admission review has no request", http.StatusBadRequest)
		return
	}
	response.Response = &admissionv1.AdmissionResponse{UID: review.Request.UID, Allowed: true}

	if review.Request.Operation == admissionv1.Create && !r.skipNamespace(review.Request.Namespace) {
		pod := &corev1.Pod{}
		if err := json.Unmarshal(review.Request.Object.Raw, pod); err != nil {
			response.Response.Allowed = false
			response.Response.Result = &metav1.Status{Message: "invalid Pod object"}
		} else if patch, err := r.PatchPod(pod); err != nil {
			response.Response.Allowed = false
			response.Response.Result = &metav1.Status{Message: err.Error()}
		} else if len(patch) > 0 {
			patchType := admissionv1.PatchTypeJSONPatch
			response.Response.PatchType = &patchType
			response.Response.Patch = patch
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "encode admission response", http.StatusInternalServerError)
	}
}

func (r *Rewriter) skipNamespace(namespace string) bool {
	if strings.HasPrefix(namespace, "cattle-") {
		return true
	}
	_, excluded := r.excluded[namespace]
	return excluded
}

func hasPullSecret(pod *corev1.Pod, name string) bool {
	for _, secret := range pod.Spec.ImagePullSecrets {
		if secret.Name == name {
			return true
		}
	}
	return false
}

// imageRegistry returns the explicit registry host of an image reference.
// Unqualified references (implicit Docker Hub) are not rewritten.
func imageRegistry(image string) (string, bool) {
	name := image
	if digest := strings.IndexByte(name, '@'); digest >= 0 {
		name = name[:digest]
	}
	slash := strings.IndexByte(name, '/')
	if slash <= 0 {
		return "", false
	}
	host := strings.ToLower(name[:slash])
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "", false
	}
	return host, true
}

// normalizeImage expands unqualified Docker Hub references to their canonical
// docker.io form while preserving tags and digests.
func normalizeImage(image string) string {
	name := strings.TrimSpace(image)
	if name == "" {
		return ""
	}
	digest := ""
	if idx := strings.IndexByte(name, '@'); idx >= 0 {
		digest = name[idx:]
		name = name[:idx]
	}
	if name == "" {
		return ""
	}
	if _, ok := imageRegistry(name); ok {
		return name + digest
	}
	if strings.Contains(name, "/") {
		return "docker.io/" + name + digest
	}
	return "docker.io/library/" + name + digest
}

func isProxyOrHarbor(registry, proxyDomain string) bool {
	if registry == "" {
		return false
	}
	if registry == proxyDomain {
		return true
	}
	return registry == "harbor" || strings.HasPrefix(registry, "harbor.") || strings.HasPrefix(registry, "harbor-")
}
