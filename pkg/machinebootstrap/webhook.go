package machinebootstrap

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

const (
	// WebhookPath is the HTTP path used by the MutatingWebhookConfiguration.
	WebhookPath = "/mutate-v1-secret"
	// DefaultWebhookListenAddress is the webhook listen address.
	DefaultWebhookListenAddress = ":9443"
	// DefaultWebhookCertDir holds tls.crt and tls.key mounted from a Secret.
	DefaultWebhookCertDir = "/var/run/d7y-webhook-certs"

	// maxAdmissionReviewBytes bounds a request. Bootstrap Secrets approach the
	// 1 MiB object limit and the review carries the object twice (object and
	// oldObject), so 4 MiB leaves headroom without allowing unbounded reads.
	maxAdmissionReviewBytes = 4 << 20
)

// Webhook is the mutating admission handler for machine bootstrap Secrets.
// It only mutates Secrets that the MutatingWebhookConfiguration already
// filtered: type rke.cattle.io/bootstrap and either the -machine-bootstrap name
// suffix or an RKEBootstrap owner reference for truncated generated names.
type Webhook struct {
	builder *Builder
}

// NewWebhook returns a handler backed by the builder.
func NewWebhook(builder *Builder) *Webhook {
	return &Webhook{builder: builder}
}

// ServeHTTP decodes an AdmissionReview and returns a JSON patch.
func (w *Webhook) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAdmissionReviewBytes+1))
	if err != nil {
		http.Error(rw, "read admission review", http.StatusBadRequest)
		return
	}
	if len(body) > maxAdmissionReviewBytes {
		http.Error(rw, "admission review too large", http.StatusRequestEntityTooLarge)
		return
	}
	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		http.Error(rw, "invalid AdmissionReview", http.StatusBadRequest)
		return
	}
	if review.Request == nil {
		http.Error(rw, "AdmissionReview has no request", http.StatusBadRequest)
		return
	}

	response := w.review(review.Request)
	out := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: admissionv1.SchemeGroupVersion.String(),
			Kind:       "AdmissionReview",
		},
		Response: response,
	}
	rw.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(rw).Encode(out); err != nil {
		slog.Error("write admission response", "error", err)
	}
}

func (w *Webhook) review(request *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	if w == nil || w.builder == nil {
		return denied(request.UID, "machine bootstrap webhook is not configured")
	}
	if len(request.Object.Raw) == 0 {
		return denied(request.UID, "admission request has no object")
	}
	secret := &corev1.Secret{}
	if err := json.Unmarshal(request.Object.Raw, secret); err != nil {
		return denied(request.UID, fmt.Sprintf("decode Secret: %v", err))
	}
	desired, changed, err := w.builder.Build(secret)
	if err != nil {
		return denied(request.UID, fmt.Sprintf("machine bootstrap webhook: %v", err))
	}
	response := &admissionv1.AdmissionResponse{UID: request.UID, Allowed: true}
	if !changed || desired == nil {
		return response
	}
	patch, err := createPatch(secret, desired)
	if err != nil {
		return denied(request.UID, fmt.Sprintf("build Secret patch: %v", err))
	}
	if len(patch) == 0 {
		return response
	}
	patchType := admissionv1.PatchTypeJSONPatch
	response.PatchType = &patchType
	response.Patch = patch
	return response
}

func denied(uid types.UID, message string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		UID:     uid,
		Allowed: false,
		Result: &metav1.Status{
			Status:  metav1.StatusFailure,
			Message: message,
		},
	}
}

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// createPatch builds an RFC 6902 JSON patch. It uses add operations only:
// adding an existing object member replaces its value, which keeps the patch
// valid whether or not the Secret already carries the key.
func createPatch(original, desired *corev1.Secret) ([]byte, error) {
	var operations []patchOperation
	if string(original.Data[bootstrapValueKey]) != string(desired.Data[bootstrapValueKey]) {
		encoded := base64.StdEncoding.EncodeToString(desired.Data[bootstrapValueKey])
		if len(original.Data) > 0 {
			operations = append(operations, patchOperation{
				Op: "add", Path: "/data/" + escapeJSONPointer(bootstrapValueKey), Value: encoded,
			})
		} else {
			// data is omitted from the admission object when empty, so the
			// parent object must be created together with the value.
			operations = append(operations, patchOperation{
				Op: "add", Path: "/data", Value: desired.Data,
			})
		}
	}
	annotationsAdded := false
	for _, key := range []string{
		bootstrap.ClientCertRevisionAnnotation,
		bootstrap.BootstrapInputsAnnotation,
	} {
		want := desired.Annotations[key]
		if original.Annotations[key] == want {
			continue
		}
		if len(original.Annotations) == 0 {
			if annotationsAdded {
				continue
			}
			operations = append(operations, patchOperation{
				Op: "add", Path: "/metadata/annotations", Value: desired.Annotations,
			})
			annotationsAdded = true
			continue
		}
		operations = append(operations, patchOperation{
			Op: "add", Path: "/metadata/annotations/" + escapeJSONPointer(key), Value: want,
		})
	}
	if len(operations) == 0 {
		return nil, nil
	}
	return json.Marshal(operations)
}

// escapeJSONPointer escapes a JSON Pointer reference token (RFC 6901).
func escapeJSONPointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// ServerConfig configures the TLS admission webhook server.
type ServerConfig struct {
	ListenAddress string
	CertDir       string
}

// Serve runs the TLS webhook server until ctx is cancelled. The key pair is
// re-read on every TLS handshake so a rotated mounted Secret takes effect
// without restarting the operator. A missing or unreadable certificate fails
// startup instead of accepting connections that can never complete.
func Serve(ctx context.Context, cfg ServerConfig, handler http.Handler) error {
	address := cfg.ListenAddress
	if address == "" {
		address = DefaultWebhookListenAddress
	}
	certDir := cfg.CertDir
	if certDir == "" {
		certDir = DefaultWebhookCertDir
	}
	if _, err := loadCertificate(certDir); err != nil {
		return fmt.Errorf("webhook certificate: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle(WebhookPath, handler)
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return loadCertificate(certDir)
			},
		},
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("webhook listen: %w", err)
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("machine bootstrap admission webhook listening", "address", address, "certDir", certDir)
	if err := server.Serve(tls.NewListener(listener, server.TLSConfig)); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("webhook server: %w", err)
	}
	return nil
}

func loadCertificate(dir string) (*tls.Certificate, error) {
	certificate, err := tls.LoadX509KeyPair(
		filepath.Join(dir, "tls.crt"),
		filepath.Join(dir, "tls.key"),
	)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", dir, err)
	}
	return &certificate, nil
}
