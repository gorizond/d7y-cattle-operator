package registryproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultHTTPTimeout bounds registry HTTP calls that are not already covered
// by a request/job deadline. It is generous because blob streams count
// against the client timeout.
const defaultHTTPTimeout = 30 * time.Minute

var defaultHTTPClient = &http.Client{Timeout: defaultHTTPTimeout}

// Manifest is an OCI manifest or index returned by a registry.
type Manifest struct {
	Digest    string
	MediaType string
	Body      []byte
}

// Registry is the subset of the OCI distribution API used by the proxy.
type Registry interface {
	GetManifest(ctx context.Context, repo, ref string) (Manifest, error)
	GetBlob(ctx context.Context, repo, digest, rangeHeader string) (io.ReadCloser, int64, string, error)
	PutBlob(ctx context.Context, repo, digest string, body io.Reader, size int64) error
	PutManifest(ctx context.Context, repo, ref, mediaType string, body []byte) error
	Referrers(ctx context.Context, repo, digest string) ([]byte, error)
}

// RegistryError carries the OCI registry HTTP status.
type RegistryError struct {
	Status int
	Op     string
	Body   string
}

func (e *RegistryError) Error() string {
	return fmt.Sprintf("registry %s: status %d: %s", e.Op, e.Status, strings.TrimSpace(e.Body))
}

// IsNotFound reports whether an OCI request returned 404.
func IsNotFound(err error) bool {
	var re *RegistryError
	return errors.As(err, &re) && re.Status == http.StatusNotFound
}

// HTTPRegistry talks to a Harbor registry endpoint over the OCI distribution
// API. It never exposes admin API operations.
type HTTPRegistry struct {
	BaseURL  string
	Username string
	Password string
	Client   *http.Client
}

func (r *HTTPRegistry) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return defaultHTTPClient
}

func (r *HTTPRegistry) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	return r.client().Do(req)
}

func (r *HTTPRegistry) GetManifest(ctx context.Context, repo, ref string) (Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(r.BaseURL, "/")+"/v2/"+repo+"/manifests/"+ref, nil)
	if err != nil {
		return Manifest{}, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return Manifest{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, &RegistryError{Status: resp.StatusCode, Op: "get manifest", Body: string(body)}
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		sum := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return Manifest{Digest: digest, MediaType: resp.Header.Get("Content-Type"), Body: body}, nil
}

// GetBlob opens a blob stream. Range is passed through to the registry and
// may yield a 206 response; the returned content length is the selected range
// length and contentRange is non-empty for partial content.
func (r *HTTPRegistry) GetBlob(ctx context.Context, repo, digest, rangeHeader string) (io.ReadCloser, int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(r.BaseURL, "/")+"/v2/"+repo+"/blobs/"+digest, nil)
	if err != nil {
		return nil, 0, "", err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, 0, "", &RegistryError{Status: resp.StatusCode, Op: "get blob", Body: string(body)}
	}
	return resp.Body, resp.ContentLength, resp.Header.Get("Content-Range"), nil
}

func (r *HTTPRegistry) PutBlob(ctx context.Context, repo, digest string, body io.Reader, size int64) error {
	head, err := r.do(ctx, http.MethodHead, "/v2/"+repo+"/blobs/"+digest, nil)
	if err == nil {
		head.Body.Close()
		if head.StatusCode == http.StatusOK {
			return nil
		}
	}
	start, err := r.do(ctx, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil)
	if err != nil {
		return err
	}
	startBody, _ := io.ReadAll(io.LimitReader(start.Body, 64<<10))
	start.Body.Close()
	if start.StatusCode != http.StatusAccepted && start.StatusCode != http.StatusCreated {
		return &RegistryError{Status: start.StatusCode, Op: "start blob upload", Body: string(startBody)}
	}
	location := start.Header.Get("Location")
	if location == "" {
		return fmt.Errorf("registry start blob upload: empty Location")
	}
	upload, err := url.Parse(location)
	if err != nil {
		return fmt.Errorf("registry upload Location: %w", err)
	}
	query := upload.Query()
	query.Set("digest", digest)
	upload.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, upload.String(), body)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	done, err := r.client().Do(req)
	if err != nil {
		return err
	}
	defer done.Body.Close()
	doneBody, _ := io.ReadAll(io.LimitReader(done.Body, 64<<10))
	if done.StatusCode != http.StatusCreated && done.StatusCode != http.StatusNoContent {
		return &RegistryError{Status: done.StatusCode, Op: "complete blob upload", Body: string(doneBody)}
	}
	return nil
}

func (r *HTTPRegistry) PutManifest(ctx context.Context, repo, ref, mediaType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		strings.TrimRight(r.BaseURL, "/")+"/v2/"+repo+"/manifests/"+ref, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if mediaType == "" {
		mediaType = "application/vnd.oci.image.manifest.v1+json"
	}
	req.Header.Set("Content-Type", mediaType)
	if r.Username != "" {
		req.SetBasicAuth(r.Username, r.Password)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return &RegistryError{Status: resp.StatusCode, Op: "put manifest", Body: string(respBody)}
	}
	return nil
}

func (r *HTTPRegistry) Referrers(ctx context.Context, repo, digest string) ([]byte, error) {
	resp, err := r.do(ctx, http.MethodGet, "/v2/"+repo+"/referrers/"+digest, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound {
		return []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`), nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &RegistryError{Status: resp.StatusCode, Op: "referrers", Body: string(body)}
	}
	return body, nil
}

// manifestBlobs returns the config and layer digests from an OCI manifest.
func manifestBlobs(body []byte) ([]string, error) {
	var doc struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	var digests []string
	if doc.Config.Digest != "" {
		digests = append(digests, doc.Config.Digest)
	}
	for _, layer := range doc.Layers {
		if layer.Digest != "" {
			digests = append(digests, layer.Digest)
		}
	}
	return digests, nil
}
