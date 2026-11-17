package registryproxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

type fakeRegistry struct {
	manifests map[string]Manifest
	blobs     map[string][]byte
	blobSizes map[string]int64
	puts      map[string]int
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		manifests: map[string]Manifest{},
		blobs:     map[string][]byte{},
		blobSizes: map[string]int64{},
		puts:      map[string]int{},
	}
}

func (f *fakeRegistry) GetManifest(_ context.Context, repo, ref string) (Manifest, error) {
	return f.manifests[repo+":"+ref], nil
}
func (f *fakeRegistry) GetBlob(_ context.Context, repo, digest, _ string) (io.ReadCloser, int64, string, error) {
	body, ok := f.blobs[repo+":"+digest]
	if !ok {
		return nil, 0, "", &RegistryError{Status: http.StatusNotFound, Op: "get blob"}
	}
	return io.NopCloser(bytes.NewReader(body)), int64(len(body)), "", nil
}
func (f *fakeRegistry) PutBlob(_ context.Context, repo, digest string, body io.Reader, size int64) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.puts["blob:"+repo+":"+digest]++
	f.blobs[repo+":"+digest] = data
	f.blobSizes[repo+":"+digest] = size
	return nil
}
func (f *fakeRegistry) PutManifest(_ context.Context, repo, ref, _ string, body []byte) error {
	f.puts["manifest:"+repo+":"+ref]++
	f.manifests[repo+":"+ref] = Manifest{Digest: "sha256:mirror", Body: body}
	return nil
}
func (f *fakeRegistry) Referrers(context.Context, string, string) ([]byte, error) {
	return []byte(`{"schemaVersion":2,"manifests":[]}`), nil
}

func TestCopierCopiesToMirrorAndLeavesSourceUntouched(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"},"layers":[{"digest":"sha256:layer"}]}`)
	source.manifests["dockerio/library/nginx:1.27"] = Manifest{Digest: "sha256:source", MediaType: "application/vnd.oci.image.manifest.v1+json", Body: body}
	source.blobs["dockerio/library/nginx:sha256:config"] = []byte("config")
	source.blobs["dockerio/library/nginx:sha256:layer"] = []byte("layer")

	copier := &Copier{Source: source, Mirror: mirror, MirrorProject: "mirror"}
	digest, err := copier.Copy(context.Background(), "docker.io", "library/nginx", "1.27")
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if digest != "sha256:source" {
		t.Errorf("digest = %q", digest)
	}
	if _, ok := mirror.manifests["mirror/docker.io/library/nginx:1.27"]; !ok {
		t.Error("mirror manifest was not written")
	}
	for _, d := range []string{"sha256:config", "sha256:layer"} {
		if string(mirror.blobs["mirror/docker.io/library/nginx:"+d]) == "" {
			t.Errorf("mirror blob %s was not written", d)
		}
	}
	for key := range source.puts {
		t.Errorf("source registry was written: %s", key)
	}
}

func TestCopierCopiesOCIIndexChildren(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	index := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:child1","platform":{"architecture":"amd64","os":"linux"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:child2","platform":{"architecture":"arm64","os":"linux"}}]}`)
	source.manifests["dockerio/library/nginx:1.27"] = Manifest{Digest: "sha256:index", MediaType: ociIndexType, Body: index}
	children := map[string]string{
		"sha256:child1": `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config1"},"layers":[{"digest":"sha256:layer1"}]}`,
		"sha256:child2": `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config2"},"layers":[{"digest":"sha256:layer2"}]}`,
	}
	for digest, body := range children {
		source.manifests["dockerio/library/nginx:"+digest] = Manifest{Digest: digest, MediaType: ociManifestType, Body: []byte(body)}
	}
	for _, digest := range []string{"sha256:config1", "sha256:layer1", "sha256:config2", "sha256:layer2"} {
		source.blobs["dockerio/library/nginx:"+digest] = []byte(digest)
	}

	copier := &Copier{Source: source, Mirror: mirror, MirrorProject: "mirror"}
	digest, err := copier.Copy(context.Background(), "docker.io", "library/nginx", "1.27")
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if digest != "sha256:index" {
		t.Errorf("digest = %q", digest)
	}
	top, ok := mirror.manifests["mirror/docker.io/library/nginx:1.27"]
	if !ok || string(top.Body) != string(index) {
		t.Error("mirror index was not written with the original descriptors")
	}
	for _, child := range []string{"sha256:child1", "sha256:child2"} {
		if _, ok := mirror.manifests["mirror/docker.io/library/nginx:"+child]; !ok {
			t.Errorf("mirror child manifest %s was not written", child)
		}
	}
	for _, blob := range []string{"sha256:config1", "sha256:layer1", "sha256:config2", "sha256:layer2"} {
		key := "mirror/docker.io/library/nginx:" + blob
		if len(mirror.blobs[key]) == 0 {
			t.Errorf("mirror blob %s was not written", blob)
		}
		if mirror.blobSizes[key] != int64(len(source.blobs["dockerio/library/nginx:"+blob])) {
			t.Errorf("mirror blob %s size = %d", blob, mirror.blobSizes[key])
		}
	}
	if _, err := copier.Copy(context.Background(), "docker.io", "library/nginx", "1.27"); err != nil {
		t.Fatalf("Copy(idempotent) error = %v", err)
	}
	for key := range source.puts {
		t.Errorf("source registry was written: %s", key)
	}
}
