package registryproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	manifest, ok := f.manifests[repo+":"+ref]
	if !ok {
		return Manifest{}, &RegistryError{Status: http.StatusNotFound, Op: "get manifest"}
	}
	return manifest, nil
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

func TestCopierUsesGenericSourceProject(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"},"layers":[{"digest":"sha256:layer"}]}`)
	source.manifests["quayio-generic/skopeo/stable:1.16.0"] = Manifest{
		Digest: "sha256:source", MediaType: ociManifestType, Body: body,
	}
	source.blobs["quayio-generic/skopeo/stable:sha256:config"] = []byte("config")
	source.blobs["quayio-generic/skopeo/stable:sha256:layer"] = []byte("layer")

	copier := &Copier{
		Source: source, Mirror: mirror, MirrorProject: "mirror",
		SourceProject: func(string) string { return "quayio-generic" },
	}
	if _, err := copier.Copy(context.Background(), "quay.io", "skopeo/stable", "1.16.0"); err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if _, ok := mirror.manifests["mirror/quay.io/skopeo/stable:1.16.0"]; !ok {
		t.Error("mirror manifest was not written from the generic project")
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
	if !ok {
		t.Fatal("mirror index was not written")
	}
	var mirrored indexDocument
	if err := json.Unmarshal(top.Body, &mirrored); err != nil {
		t.Fatal(err)
	}
	if len(mirrored.Manifests) != 2 || mirrored.Manifests[0].Digest != "sha256:child1" {
		t.Errorf("mirror index descriptors = %+v", mirrored.Manifests)
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

func TestHarborBackendCopiesOnManifestMiss(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"},"layers":[]}`)
	source.manifests["dockerio/library/nginx:1.27"] = Manifest{
		Digest: "sha256:source", MediaType: ociManifestType, Body: body,
	}
	source.manifests["dockerio/library/nginx:sha256:source"] = source.manifests["dockerio/library/nginx:1.27"]
	source.blobs["dockerio/library/nginx:sha256:config"] = []byte("config")

	copier := &Copier{Source: source, Mirror: mirror, MirrorProject: "mirror"}
	release := make(chan struct{})
	started := make(chan struct{})
	manager := NewManager(func(ctx context.Context, job Job) error {
		close(started)
		<-release
		_, err := copier.Copy(ctx, job.Registry, job.Repository, job.TagOrReference())
		return err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	backend := &HarborBackend{Source: source, Mirror: mirror, MirrorProject: "mirror", Jobs: manager}

	if _, err := backend.GetManifest(ctx, "docker.io", "library/nginx", "1.27"); !errors.Is(err, ErrConverting) {
		t.Fatalf("first GetManifest() error = %v, want ErrConverting", err)
	}
	<-started
	if state := manager.Pending(); state != 1 {
		t.Errorf("pending jobs = %d, want 1", state)
	}
	close(release)
	waitState(t, manager, Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Digest: "sha256:source"}.Key(), "ready")

	manifest, err := backend.GetManifest(ctx, "docker.io", "library/nginx", "1.27")
	if err != nil {
		t.Fatalf("second GetManifest() error = %v", err)
	}
	if manifest.Digest != "sha256:mirror" {
		t.Errorf("second GetManifest() digest = %q, want the mirror manifest", manifest.Digest)
	}
	if _, err := backend.GetManifest(ctx, "docker.io", "library/nginx", "sha256:source"); err != nil {
		t.Fatalf("digest GetManifest() error = %v", err)
	}
	enqueued, succeeded, failed := manager.Metrics()
	if enqueued != 1 || succeeded != 1 || failed != 0 {
		t.Errorf("job metrics = %d/%d/%d, want 1/1/0", enqueued, succeeded, failed)
	}
}

func TestHarborBackendServesSourceOCIAfterFailedConversion(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	source.manifests["dockerio/library/nginx:1.27"] = Manifest{
		Digest: "sha256:source", MediaType: ociManifestType, Body: []byte(`{"schemaVersion":2}`),
	}
	manager := NewManager(nil)
	job := Job{Registry: "docker.io", Repository: "library/nginx", Reference: "1.27", Tag: "1.27", Digest: "sha256:source"}
	manager.setState(job.Key(), "failed", 3, "conversion failed")
	backend := &HarborBackend{Source: source, Mirror: mirror, MirrorProject: "mirror", Jobs: manager}
	manifest, err := backend.GetManifest(context.Background(), "docker.io", "library/nginx", "1.27")
	if err != nil {
		t.Fatalf("GetManifest() error = %v", err)
	}
	if manifest.Digest != "sha256:source" {
		t.Errorf("digest = %q, want source OCI fallback", manifest.Digest)
	}
}

func TestCopierDropsAttestationsFromMultiarchIndex(t *testing.T) {
	source := newFakeRegistry()
	mirror := newFakeRegistry()
	platforms := []struct {
		arch string
		blob string
	}{
		{"amd64", "sha256:layer1"},
		{"arm64", "sha256:layer2"},
		{"arm", "sha256:layer3"},
		{"386", "sha256:layer4"},
		{"ppc64le", "sha256:layer5"},
		{"s390x", "sha256:layer6"},
		{"riscv64", "sha256:layer7"},
		{"mips64le", "sha256:layer8"},
	}
	var descriptors []map[string]any
	var realDigests []string
	for i, platform := range platforms {
		digest := fmt.Sprintf("sha256:platform%d", i)
		realDigests = append(realDigests, digest)
		descriptors = append(descriptors, map[string]any{
			"mediaType": ociManifestType, "digest": digest, "size": 10,
			"platform": map[string]string{"os": "linux", "architecture": platform.arch},
		})
		body := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"digest":"sha256:config%d"},"layers":[{"digest":%q}]}`,
			ociManifestType, i, platform.blob)
		source.manifests["dockerio/library/busybox:"+digest] = Manifest{Digest: digest, MediaType: ociManifestType, Body: []byte(body)}
		source.blobs["dockerio/library/busybox:sha256:config"+fmt.Sprint(i)] = []byte("config")
		source.blobs["dockerio/library/busybox:"+platform.blob] = []byte(platform.blob)
	}
	for i := 0; i < 9; i++ {
		descriptors = append(descriptors, map[string]any{
			"mediaType": ociManifestType, "digest": fmt.Sprintf("sha256:attestation%d", i), "size": 5,
			"artifactType": "application/vnd.in-toto+json",
			"platform":     map[string]string{"os": "unknown", "architecture": "unknown"},
		})
	}
	indexBody, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     ociIndexType,
		"manifests":     descriptors,
	})
	source.manifests["dockerio/library/busybox:1.36"] = Manifest{Digest: "sha256:index", MediaType: ociIndexType, Body: indexBody}

	copier := &Copier{Source: source, Mirror: mirror, MirrorProject: "mirror"}
	if _, err := copier.Copy(context.Background(), "docker.io", "library/busybox", "1.36"); err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	top := mirror.manifests["mirror/docker.io/library/busybox:1.36"]
	var mirrored indexDocument
	if err := json.Unmarshal(top.Body, &mirrored); err != nil {
		t.Fatal(err)
	}
	if len(mirrored.Manifests) != len(platforms) {
		t.Fatalf("mirrored manifests = %d, want %d real platforms", len(mirrored.Manifests), len(platforms))
	}
	for _, digest := range realDigests {
		if _, ok := mirror.manifests["mirror/docker.io/library/busybox:"+digest]; !ok {
			t.Errorf("real platform manifest %s was not copied", digest)
		}
	}
	for i := 0; i < 9; i++ {
		digest := fmt.Sprintf("sha256:attestation%d", i)
		if _, ok := mirror.manifests["mirror/docker.io/library/busybox:"+digest]; ok {
			t.Errorf("attestation manifest %s was copied", digest)
		}
	}
}
