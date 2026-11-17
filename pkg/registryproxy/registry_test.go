package registryproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRegistryGetBlobRange(t *testing.T) {
	var gotRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/library/nginx/blobs/sha256:large" {
			http.NotFound(w, r)
			return
		}
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 0-2/9")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("blo"))
	}))
	defer server.Close()

	body, size, contentRange, err := (&HTTPRegistry{BaseURL: server.URL}).
		GetBlob(context.Background(), "library/nginx", "sha256:large", "bytes=0-2")
	if err != nil {
		t.Fatalf("GetBlob() error = %v", err)
	}
	defer body.Close()
	data, _ := io.ReadAll(body)
	if gotRange != "bytes=0-2" {
		t.Errorf("Range forwarded = %q", gotRange)
	}
	if string(data) != "blo" || size != 3 || contentRange != "bytes 0-2/9" {
		t.Errorf("blob = %q size=%d range=%q", data, size, contentRange)
	}
}

func TestHTTPRegistryGetBlobNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, _, _, err := (&HTTPRegistry{BaseURL: server.URL}).
		GetBlob(context.Background(), "library/nginx", "sha256:missing", "")
	if !IsNotFound(err) {
		t.Errorf("GetBlob() error = %v, want 404", err)
	}
}

func TestHTTPRegistryPutBlobContentLength(t *testing.T) {
	const payload = "blob-data"
	var putLength int64
	var putBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/v2/library/nginx/blobs/sha256:large":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/library/nginx/blobs/uploads/":
			w.Header().Set("Location", "http://"+r.Host+"/upload")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && r.URL.Path == "/upload":
			putLength = r.ContentLength
			data, _ := io.ReadAll(r.Body)
			putBody = string(data)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := (&HTTPRegistry{BaseURL: server.URL}).PutBlob(context.Background(),
		"library/nginx", "sha256:large", strings.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}
	if putLength != int64(len(payload)) {
		t.Errorf("PUT Content-Length = %d, want %d", putLength, len(payload))
	}
	if putBody != payload {
		t.Errorf("PUT body = %q", putBody)
	}
}
