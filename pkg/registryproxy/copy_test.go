package registryproxy

import (
	"context"
	"testing"
)

type fakeRegistry struct {
	manifests map[string]Manifest
	blobs     map[string][]byte
	puts      map[string]int
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{manifests: map[string]Manifest{}, blobs: map[string][]byte{}, puts: map[string]int{}}
}

func (f *fakeRegistry) GetManifest(_ context.Context, repo, ref string) (Manifest, error) {
	return f.manifests[repo+":"+ref], nil
}
func (f *fakeRegistry) GetBlob(_ context.Context, repo, digest string) ([]byte, error) {
	return f.blobs[repo+":"+digest], nil
}
func (f *fakeRegistry) PutBlob(_ context.Context, repo, digest string, body []byte) error {
	f.puts["blob:"+repo+":"+digest]++
	f.blobs[repo+":"+digest] = body
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
