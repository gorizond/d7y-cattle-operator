package registryproxy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testProxyURL = "https://proxy.example.test"

func nydusManifestBody(sourceRef string) []byte {
	annotations := ""
	if sourceRef != "" {
		annotations = fmt.Sprintf(`,"annotations":{"%s":%q}`, sourceReferenceKey, sourceRef)
	}
	return []byte(`{"schemaVersion":2,"mediaType":"` + ociManifestType + `"` + annotations +
		`,"layers":[{"digest":"sha256:bootstrap","annotations":{"` + nydusBootstrapKey + `":""}}]}`)
}

func singleSourceManifest(t *testing.T, registry *fakeRegistry, digest string, body []byte) {
	t.Helper()
	registry.manifests["mirror/docker.io/library/nginx:"+digest] = Manifest{
		Digest: digest, MediaType: ociManifestType, Body: body,
	}
	registry.manifests["mirror/docker.io/library/nginx:1.27"] = Manifest{
		Digest: digest, MediaType: ociManifestType, Body: body,
	}
	registry.blobs["mirror/docker.io/library/nginx:sha256:config"] = []byte(`{"architecture":"amd64","os":"linux"}`)
}

func findNydusDescriptor(t *testing.T, index indexDocument) indexDescriptor {
	t.Helper()
	for _, descriptor := range index.Manifests {
		if descriptor.ArtifactType == nydusArtifactType {
			return descriptor
		}
	}
	t.Fatalf("no nydus descriptor in index: %+v", index.Manifests)
	return indexDescriptor{}
}

func TestConverterVerifiesAndMovesIndex(t *testing.T) {
	registry := newFakeRegistry()
	sourceBody := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"},"layers":[]}`)
	singleSourceManifest(t, registry, "sha256:source-manifest", sourceBody)
	index := indexDocument{
		SchemaVersion: 2,
		MediaType:     ociIndexType,
		Manifests: []indexDescriptor{
			{MediaType: ociManifestType, Digest: "sha256:oci", Size: 1234, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
			{MediaType: ociManifestType, Digest: "sha256:nydus", Size: 5678, ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		},
	}
	indexBody, _ := json.Marshal(index)
	registry.manifests["mirror/docker.io/library/nginx:1.27-merged"] = Manifest{
		Digest: "sha256:index", MediaType: ociIndexType, Body: indexBody,
	}
	registry.manifests["mirror/docker.io/library/nginx:sha256:nydus"] = Manifest{
		Digest: "sha256:nydus", MediaType: ociManifestType, Body: nydusManifestBody(""),
	}
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "nydusify", NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
		Command: func(context.Context, string, ...string) *exec.Cmd { return exec.Command("true") },
	}
	result, err := converter.Convert(context.Background(), "docker.io", "library/nginx", "1.27", "sha256:source")
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !strings.HasPrefix(result.IndexDigest, "sha256:") || result.SourceDigest != "sha256:source" {
		t.Errorf("result = %+v", result)
	}
	moved := registry.manifests["mirror/docker.io/library/nginx:1.27"]
	var movedIndex indexDocument
	if err := json.Unmarshal(moved.Body, &movedIndex); err != nil {
		t.Fatal(err)
	}
	if movedIndex.Annotations[sourceDigestKey] != "sha256:source" {
		t.Errorf("source digest annotation = %q", movedIndex.Annotations[sourceDigestKey])
	}
	if got := movedIndex.Annotations[sourceReferenceKey]; got != "proxy.example.test/docker.io/library/nginx:1.27" {
		t.Errorf("index source reference = %q", got)
	}
	if len(movedIndex.Manifests) != 2 {
		t.Fatalf("published manifests = %+v", movedIndex.Manifests)
	}
	if movedIndex.Manifests[0].Digest != "sha256:source-manifest" || movedIndex.Manifests[0].Size != int64(len(sourceBody)) {
		t.Errorf("source descriptor = %+v", movedIndex.Manifests[0])
	}
	nydus := findNydusDescriptor(t, movedIndex)
	if nydus.Platform == nil || nydus.Platform.OS != "linux" || nydus.Platform.Architecture != "amd64" {
		t.Errorf("nydus descriptor platform = %+v", nydus.Platform)
	}
	rewritten := registry.manifests["mirror/docker.io/library/nginx:"+nydus.Digest]
	if nydus.Size != int64(len(rewritten.Body)) {
		t.Errorf("nydus descriptor size = %d, want %d", nydus.Size, len(rewritten.Body))
	}
}

func TestConverterCommandBuildsNydusifyArgs(t *testing.T) {
	registry := newFakeRegistry()
	singleSourceManifest(t, registry, "sha256:source-manifest", []byte(`{"schemaVersion":2,"config":{"digest":"sha256:config"},"layers":[]}`))
	indexBody, _ := json.Marshal(indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: "sha256:oci", Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
	}})
	registry.manifests["mirror/docker.io/library/nginx:1.27-merged"] = Manifest{Digest: "sha256:index", MediaType: ociIndexType, Body: indexBody}
	registry.manifests["mirror/docker.io/library/nginx:sha256:nydus"] = Manifest{MediaType: ociManifestType, Body: nydusManifestBody("")}
	var captured []string
	name := ""
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "/usr/local/bin/nydusify",
		NydusImage:   "/usr/local/bin/nydus-image", Registry: registry,
		Command: func(_ context.Context, command string, args ...string) *exec.Cmd {
			name = command
			captured = args
			return exec.Command("true")
		},
	}
	if _, err := converter.Convert(context.Background(), "docker.io", "library/nginx", "1.27", "sha256:source"); err != nil {
		t.Fatal(err)
	}
	if name != "/usr/local/bin/nydusify" {
		t.Errorf("command = %q", name)
	}
	if strings.Contains(strings.Join(captured, " "), "--source-username") {
		t.Error("invalid nydusify username flag was passed")
	}
	joined := strings.Join(captured, " ")
	for _, want := range []string{
		"convert",
		"--source harbor.example.com/mirror/docker.io/library/nginx:1.27",
		"--target harbor.example.com/mirror/docker.io/library/nginx:1.27-merged",
		"--platform linux/amd64",
		"--merge-platform",
		"--oci",
		"--fs-version 6",
		"--nydus-image /usr/local/bin/nydus-image",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
}

func TestWriteDockerConfigDoesNotExposeSecrets(t *testing.T) {
	username := fmt.Sprintf("test-user-%d", time.Now().UnixNano())
	password := fmt.Sprintf("test-pass-%d", time.Now().UnixNano())
	dir, err := writeDockerConfig("harbor.example.com", username, password)
	if err != nil {
		t.Fatalf("writeDockerConfig() error = %v", err)
	}
	defer os.RemoveAll(dir)
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("config mode = %o, want 0600", info.Mode().Perm())
	}
	body, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	if config.Auths["harbor.example.com"].Auth != want {
		t.Error("docker auth entry is wrong")
	}
	if strings.Contains(string(body), password) {
		t.Error("plaintext secret leaked into docker config")
	}
}

func TestConverterFailureLeavesMirrorTag(t *testing.T) {
	registry := newFakeRegistry()
	original := Manifest{Digest: "sha256:source", MediaType: ociManifestType, Body: []byte(`{"schemaVersion":2}`)}
	singleSourceManifest(t, registry, "sha256:source", original.Body)
	registry.manifests["mirror/docker.io/library/nginx:1.27"] = original
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "nydusify",
		NydusImage:   "/usr/local/bin/nydus-image", Registry: registry,
		Command: func(context.Context, string, ...string) *exec.Cmd { return exec.Command("false") },
	}
	if _, err := converter.Convert(context.Background(), "docker.io", "library/nginx", "1.27", "sha256:source"); err == nil {
		t.Fatal("Convert() error = nil")
	}
	if got := registry.manifests["mirror/docker.io/library/nginx:1.27"].Body; string(got) != string(original.Body) {
		t.Error("mirror tag was changed on conversion failure")
	}
}

func TestVerifyIndexFindsNydusByArtifactType(t *testing.T) {
	registry := newFakeRegistry()
	// The nydus artifact deliberately comes first: verification must not rely
	// on fixed descriptor positions.
	index := indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		{MediaType: ociManifestType, Digest: "sha256:oci", Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
	}}
	body, _ := json.Marshal(index)
	registry.manifests["mirror/x/y:sha256:nydus"] = Manifest{MediaType: ociManifestType, Body: nydusManifestBody("")}
	verified, err := verifyIndex(context.Background(), registry, "mirror/x/y",
		Manifest{MediaType: ociIndexType, Body: body}, "linux/amd64")
	if err != nil {
		t.Fatalf("verifyIndex() error = %v", err)
	}
	if verified.Descriptor.Digest != "sha256:nydus" || verified.Descriptor.ArtifactType != nydusArtifactType {
		t.Errorf("verified descriptor = %+v", verified.Descriptor)
	}
}

func TestVerifyIndexRejectsMissingPlatform(t *testing.T) {
	registry := newFakeRegistry()
	index := indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "arm64"}},
	}}
	body, _ := json.Marshal(index)
	if _, err := verifyIndex(context.Background(), registry, "mirror/x/y",
		Manifest{MediaType: ociIndexType, Body: body}, "linux/amd64"); err == nil {
		t.Fatal("verifyIndex() error = nil for a missing platform artifact")
	}
}

func TestConverterDigestReferenceArgs(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const targetTag = "nd-0123456789ab"
	registry := newFakeRegistry()
	registry.manifests["mirror/docker.io/library/nginx:"+digest] = Manifest{
		Digest: digest, MediaType: ociManifestType,
		Body: []byte(`{"schemaVersion":2,"config":{"digest":"sha256:config"},"layers":[]}`),
	}
	registry.blobs["mirror/docker.io/library/nginx:sha256:config"] = []byte(`{"architecture":"amd64","os":"linux"}`)
	indexBody, _ := json.Marshal(indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: "sha256:oci", Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
	}})
	registry.manifests["mirror/docker.io/library/nginx:"+targetTag+"-merged"] = Manifest{Digest: "sha256:index", MediaType: ociIndexType, Body: indexBody}
	registry.manifests["mirror/docker.io/library/nginx:sha256:nydus"] = Manifest{
		MediaType: ociManifestType, Body: nydusManifestBody(""),
	}
	var captured []string
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "/usr/local/bin/nydusify",
		NydusImage:   "/usr/local/bin/nydus-image", Registry: registry,
		Command: func(_ context.Context, _ string, args ...string) *exec.Cmd {
			captured = args
			return exec.Command("true")
		},
	}
	if _, err := converter.Convert(context.Background(), "docker.io", "library/nginx", digest, digest); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	joined := strings.Join(captured, " ")
	for _, want := range []string{
		"--source harbor.example.com/mirror/docker.io/library/nginx@" + digest,
		"--target harbor.example.com/mirror/docker.io/library/nginx:" + targetTag,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if _, ok := registry.manifests["mirror/docker.io/library/nginx:"+targetTag]; !ok {
		t.Error("converted index was not written under the short digest tag")
	}
	if _, ok := registry.manifests["mirror/docker.io/library/nginx:"+digest]; ok &&
		registry.puts["manifest:mirror/docker.io/library/nginx:"+digest] > 0 {
		t.Error("converted index was written under an invalid digest tag")
	}
}

func TestProxySourceReference(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		registry  string
		repo      string
		ref       string
		want      string
		wantErr   bool
	}{
		{
			name: "tag", publicURL: testProxyURL,
			registry: "docker.io", repo: "library/nginx", ref: "1.27",
			want: "proxy.example.test/docker.io/library/nginx:1.27",
		},
		{
			name: "base path", publicURL: "https://proxy.example.test/proxy/",
			registry: "docker.io", repo: "/library/nginx", ref: "1.27",
			want: "proxy.example.test/proxy/docker.io/library/nginx:1.27",
		},
		{
			name: "digest", publicURL: testProxyURL,
			registry: "docker.io", repo: "library/nginx", ref: "sha256:abc",
			want: "proxy.example.test/docker.io/library/nginx@sha256:abc",
		},
		{
			name: "invalid URL", publicURL: "proxy.example.test",
			registry: "docker.io", repo: "library/nginx", ref: "1.27",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := proxySourceReference(test.publicURL, test.registry, test.repo, test.ref)
			if test.wantErr {
				if err == nil {
					t.Fatal("proxySourceReference() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("proxySourceReference() error = %v", err)
			}
			if got != test.want {
				t.Errorf("proxySourceReference() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConverterRewritesNydusSourceReference(t *testing.T) {
	registry := newFakeRegistry()
	singleSourceManifest(t, registry, "sha256:source-manifest", []byte(`{"schemaVersion":2,"config":{"digest":"sha256:config"},"layers":[]}`))
	nydusBody := nydusManifestBody("registry.example.test/team/app:1.0")
	index := indexDocument{
		SchemaVersion: 2,
		MediaType:     ociIndexType,
		Manifests: []indexDescriptor{
			{MediaType: ociManifestType, Digest: "sha256:oci", Size: 1234, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
			{MediaType: ociManifestType, Digest: "sha256:nydus", Size: int64(len(nydusBody)), ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		},
	}
	indexBody, _ := json.Marshal(index)
	registry.manifests["mirror/docker.io/library/nginx:1.27-merged"] = Manifest{
		Digest: "sha256:index", MediaType: ociIndexType, Body: indexBody,
	}
	registry.manifests["mirror/docker.io/library/nginx:sha256:nydus"] = Manifest{
		Digest: "sha256:nydus", MediaType: ociManifestType, Body: nydusBody,
	}
	converter := &Converter{
		HarborURL: "https://harbor.example.test", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "nydusify", NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
		Command: func(context.Context, string, ...string) *exec.Cmd { return exec.Command("true") },
	}
	if _, err := converter.Convert(context.Background(), "docker.io", "library/nginx", "1.27", "sha256:source"); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	moved := registry.manifests["mirror/docker.io/library/nginx:1.27"]
	var movedIndex indexDocument
	if err := json.Unmarshal(moved.Body, &movedIndex); err != nil {
		t.Fatal(err)
	}
	nydusDesc := findNydusDescriptor(t, movedIndex)
	rewritten, ok := registry.manifests["mirror/docker.io/library/nginx:"+nydusDesc.Digest]
	if !ok || len(rewritten.Body) == 0 {
		t.Fatalf("rewritten nydus manifest was not pushed under %s", nydusDesc.Digest)
	}
	var nydusDoc struct {
		Annotations map[string]string `json:"annotations"`
		Layers      []struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(rewritten.Body, &nydusDoc); err != nil {
		t.Fatal(err)
	}
	wantRef := "proxy.example.test/docker.io/library/nginx:1.27"
	if got := nydusDoc.Annotations[sourceReferenceKey]; got != wantRef {
		t.Errorf("nydus source reference = %q, want %q", got, wantRef)
	}
	if got := movedIndex.Annotations[sourceReferenceKey]; got != wantRef {
		t.Errorf("index source reference = %q, want %q", got, wantRef)
	}
	if _, ok := nydusDoc.Layers[len(nydusDoc.Layers)-1].Annotations[nydusBootstrapKey]; !ok {
		t.Error("bootstrap layer annotation was lost")
	}
	sum := sha256.Sum256(rewritten.Body)
	if want := "sha256:" + hex.EncodeToString(sum[:]); nydusDesc.Digest != want {
		t.Errorf("index nydus digest = %q, want %q", nydusDesc.Digest, want)
	}
	if nydusDesc.Size != int64(len(rewritten.Body)) {
		t.Errorf("index nydus size = %d, want %d", nydusDesc.Size, len(rewritten.Body))
	}
	if got := movedIndex.Annotations[sourceDigestKey]; got != "sha256:source" {
		t.Errorf("source digest annotation = %q", got)
	}
	original := registry.manifests["mirror/docker.io/library/nginx:sha256:nydus"]
	if !strings.Contains(string(original.Body), "registry.example.test/team/app:1.0") {
		t.Error("original nydus manifest was modified")
	}
}

func TestConverterMultiPlatformPublishesOnlyPresentTargetChild(t *testing.T) {
	registry := newFakeRegistry()
	sourceIndex := indexDocument{
		SchemaVersion: 2,
		MediaType:     ociIndexType,
		Manifests: []indexDescriptor{
			{MediaType: ociManifestType, Digest: "sha256:amd64", Size: 100, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
			{MediaType: ociManifestType, Digest: "sha256:arm64", Size: 200, Platform: &indexPlatform{OS: "linux", Architecture: "arm64", Variant: "v8"}},
			{MediaType: ociManifestType, Digest: "sha256:attestation", Size: 300, ArtifactType: "application/vnd.in-toto+json", Platform: &indexPlatform{OS: "unknown", Architecture: "unknown"}},
		},
	}
	sourceBody, _ := json.Marshal(sourceIndex)
	registry.manifests["mirror/docker.io/library/nginx:1.27"] = Manifest{
		Digest: "sha256:source-index", MediaType: ociIndexType, Body: sourceBody,
	}
	// nydusify copies only the linux/amd64 child into the target repo. Its
	// merged index has the nydus artifact first, proving position-independent
	// verification.
	merged := indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: "sha256:nydus", Size: int64(len(nydusManifestBody(""))), ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		{MediaType: ociManifestType, Digest: "sha256:amd64", Size: 100, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
	}}
	mergedBody, _ := json.Marshal(merged)
	registry.manifests["mirror/docker.io/library/nginx:1.27-merged"] = Manifest{
		Digest: "sha256:merged", MediaType: ociIndexType, Body: mergedBody,
	}
	// The target child and the nydus artifact are present under the mirror
	// repository; other source platforms were never copied.
	registry.manifests["mirror/docker.io/library/nginx:sha256:amd64"] = Manifest{
		Digest: "sha256:amd64", MediaType: ociManifestType,
		Body: []byte(`{"schemaVersion":2,"config":{"digest":"sha256:config"},"layers":[]}`),
	}
	registry.manifests["mirror/docker.io/library/nginx:sha256:nydus"] = Manifest{
		Digest: "sha256:nydus", MediaType: ociManifestType, Body: nydusManifestBody(""),
	}
	var captured []string
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "/usr/local/bin/nydusify", NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
		Command: func(_ context.Context, _ string, args ...string) *exec.Cmd {
			captured = args
			return exec.Command("true")
		},
	}
	if _, err := converter.Convert(context.Background(), "docker.io", "library/nginx", "1.27", "sha256:source-index"); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--source harbor.example.com/mirror/docker.io/library/nginx@sha256:amd64") {
		t.Errorf("nydusify source did not resolve the platform child digest: %s", joined)
	}
	var published indexDocument
	if err := json.Unmarshal(registry.manifests["mirror/docker.io/library/nginx:1.27"].Body, &published); err != nil {
		t.Fatal(err)
	}
	if len(published.Manifests) != 2 {
		t.Fatalf("published manifests = %+v, want only the target child and nydus", published.Manifests)
	}
	nydus := findNydusDescriptor(t, published)
	var runnable *indexDescriptor
	for i, descriptor := range published.Manifests {
		if descriptor.Digest == "sha256:arm64" {
			t.Errorf("uncopied arm64 descriptor leaked into the published index: %+v", descriptor)
		}
		if descriptor.ArtifactType == nydusArtifactType {
			continue
		}
		// Every published non-nydus descriptor must resolve in the target
		// repository; Harbor rejects the index otherwise.
		if _, ok := registry.manifests["mirror/docker.io/library/nginx:"+descriptor.Digest]; !ok {
			t.Errorf("published descriptor %s does not resolve in the target repo", descriptor.Digest)
		}
		runnable = &published.Manifests[i]
	}
	if runnable == nil || runnable.Platform == nil ||
		runnable.Platform.OS != "linux" || runnable.Platform.Architecture != "amd64" {
		t.Errorf("published index has no runnable linux/amd64 source descriptor: %+v", published.Manifests)
	}
	if nydus.Platform == nil || nydus.Platform.Architecture != "amd64" {
		t.Errorf("nydus platform = %+v", nydus.Platform)
	}
	if _, ok := registry.manifests["mirror/docker.io/library/nginx:"+nydus.Digest]; !ok {
		t.Errorf("nydus descriptor %s does not resolve in the target repo", nydus.Digest)
	}
}

func TestConverterMultiPlatformRejectsIndexWithoutTargetChild(t *testing.T) {
	registry := newFakeRegistry()
	sourceBody, _ := json.Marshal(indexDocument{
		SchemaVersion: 2,
		MediaType:     ociIndexType,
		Manifests: []indexDescriptor{
			{MediaType: ociManifestType, Digest: "sha256:arm64", Size: 200, Platform: &indexPlatform{OS: "linux", Architecture: "arm64"}},
		},
	})
	registry.manifests["mirror/docker.io/library/nginx:1.27"] = Manifest{
		Digest: "sha256:source-index", MediaType: ociIndexType, Body: sourceBody,
	}
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "nydusify", NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
		Command: func(context.Context, string, ...string) *exec.Cmd { return exec.Command("true") },
	}
	if _, err := converter.Convert(context.Background(), "docker.io", "library/nginx", "1.27", "sha256:source-index"); err == nil {
		t.Fatal("Convert() error = nil for an index without the target platform child")
	}
}

func TestRewriteNydusSourceReferencesHandlesBareKey(t *testing.T) {
	annotations := map[string]string{"nydus-source-reference": "registry.example.test/team/app:1.0"}
	changed, err := rewriteNydusSourceReferences(annotations, func() (string, error) {
		return "proxy.example.test/team/app:1.0", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("bare nydus-source-reference was not rewritten")
	}
	if got := annotations["nydus-source-reference"]; got != "proxy.example.test/team/app:1.0" {
		t.Errorf("annotation = %q", got)
	}
}

func TestConverterDigestCopiesMissingChildBeforePublishing(t *testing.T) {
	const registryHost = "registry.access.redhat.com"
	const digest = "sha256:d574000000000000000000000000000000000000000000000000000000000000"
	const targetTag = "nd-d57400000000"
	const child = "sha256:ubi-micro-amd64"
	repoPath := "mirror/" + registryHost + "/ubi9/ubi-micro"

	source := newFakeRegistry()
	mirror := newFakeRegistry()
	indexBody, _ := json.Marshal(indexDocument{
		SchemaVersion: 2,
		MediaType:     ociIndexType,
		Manifests: []indexDescriptor{{
			MediaType: ociManifestType, Digest: child, Size: 100,
			Platform: &indexPlatform{OS: "linux", Architecture: "amd64"},
		}},
	})
	mirror.manifests[repoPath+":"+digest] = Manifest{Digest: digest, MediaType: ociIndexType, Body: indexBody}

	childBody := []byte(`{"schemaVersion":2,"mediaType":"` + ociManifestType + `","config":{"digest":"sha256:config"},"layers":[{"digest":"sha256:layer"}]}`)
	sourceRepo := ProjectName(registryHost) + "/ubi9/ubi-micro"
	source.manifests[sourceRepo+":"+child] = Manifest{Digest: child, MediaType: ociManifestType, Body: childBody}
	source.blobs[sourceRepo+":sha256:config"] = []byte("config")
	source.blobs[sourceRepo+":sha256:layer"] = []byte("layer")

	mergedBody, _ := json.Marshal(indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: child, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
	}})
	mirror.manifests[repoPath+":"+targetTag+"-merged"] = Manifest{Digest: "sha256:merged", MediaType: ociIndexType, Body: mergedBody}
	mirror.manifests[repoPath+":sha256:nydus"] = Manifest{MediaType: ociManifestType, Body: nydusManifestBody("")}

	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "nydusify", NydusImage: "/usr/local/bin/nydus-image", Registry: mirror,
		Copier:  &Copier{Source: source, Mirror: mirror, MirrorProject: "mirror"},
		Command: func(context.Context, string, ...string) *exec.Cmd { return exec.Command("true") },
	}
	if _, err := converter.Convert(context.Background(), registryHost, "ubi9/ubi-micro", digest, digest); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if _, ok := mirror.manifests[repoPath+":"+child]; !ok {
		t.Error("missing digest source child was not copied into the target repository")
	}
	for _, blob := range []string{"sha256:config", "sha256:layer"} {
		if len(mirror.blobs[repoPath+":"+blob]) == 0 {
			t.Errorf("child blob %s was not copied", blob)
		}
	}
	published, ok := mirror.manifests[repoPath+":"+targetTag]
	if !ok {
		t.Fatal("digest conversion index was not published")
	}
	var index indexDocument
	if err := json.Unmarshal(published.Body, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 2 || index.Manifests[0].Digest != child {
		t.Errorf("published manifests = %+v, want the copied child plus nydus", index.Manifests)
	}
	if index.Manifests[1].ArtifactType != nydusArtifactType {
		t.Errorf("nydus descriptor = %+v", index.Manifests[1])
	}
}

func TestConverterTagPathDoesNotUseCopier(t *testing.T) {
	registry := newFakeRegistry()
	singleSourceManifest(t, registry, "sha256:source-manifest", []byte(`{"schemaVersion":2,"config":{"digest":"sha256:config"},"layers":[]}`))
	indexBody, _ := json.Marshal(indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: "sha256:source-manifest", Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
	}})
	registry.manifests["mirror/docker.io/library/nginx:1.27-merged"] = Manifest{Digest: "sha256:index", MediaType: ociIndexType, Body: indexBody}
	registry.manifests["mirror/docker.io/library/nginx:sha256:nydus"] = Manifest{MediaType: ociManifestType, Body: nydusManifestBody("")}
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
		NydusifyPath: "nydusify", NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
		// A copier with no source fails if the tag path ever calls it.
		Copier:  &Copier{Mirror: registry, MirrorProject: "mirror"},
		Command: func(context.Context, string, ...string) *exec.Cmd { return exec.Command("true") },
	}
	if _, err := converter.Convert(context.Background(), "docker.io", "library/nginx", "1.27", "sha256:source"); err != nil {
		t.Fatalf("tag conversion used the digest-only copier: %v", err)
	}
}

func TestConverterSingleManifestPlatformDetection(t *testing.T) {
	const digest = "sha256:4f69000000000000000000000000000000000000000000000000000000000000"
	const targetTag = "nd-4f6900000000"
	repoPath := "mirror/registry.access.redhat.com/ubi9/ubi-micro"

	tests := []struct {
		name   string
		os     string
		arch   string
		wantOK bool
	}{
		{name: "amd64 converts", os: "linux", arch: "amd64", wantOK: true},
		{name: "arm64 is not convertible", os: "linux", arch: "arm64"},
		{name: "ppc64le is not convertible", os: "linux", arch: "ppc64le"},
		{name: "s390x is not convertible", os: "linux", arch: "s390x"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := newFakeRegistry()
			registry.manifests[repoPath+":"+digest] = Manifest{
				Digest: digest, MediaType: ociManifestType,
				Body: []byte(`{"schemaVersion":2,"mediaType":"` + ociManifestType + `","config":{"digest":"sha256:config"},"layers":[]}`),
			}
			registry.blobs[repoPath+":sha256:config"] = []byte(`{"os":"` + test.os + `","architecture":"` + test.arch + `"}`)
			nydusCalls := 0
			if test.wantOK {
				mergedBody, _ := json.Marshal(indexDocument{Manifests: []indexDescriptor{
					{MediaType: ociManifestType, Digest: digest, Platform: &indexPlatform{OS: test.os, Architecture: test.arch}},
					{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: test.os, Architecture: test.arch}},
				}})
				registry.manifests[repoPath+":"+targetTag+"-merged"] = Manifest{Digest: "sha256:merged", MediaType: ociIndexType, Body: mergedBody}
				registry.manifests[repoPath+":sha256:nydus"] = Manifest{MediaType: ociManifestType, Body: nydusManifestBody("")}
			}
			converter := &Converter{
				HarborURL: "https://harbor.example.com", MirrorProject: "mirror", ProxyPublicURL: testProxyURL,
				NydusifyPath: "nydusify", NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
				Command: func(context.Context, string, ...string) *exec.Cmd {
					nydusCalls++
					if !test.wantOK {
						t.Errorf("nydusify ran for a non-convertible %s/%s source", test.os, test.arch)
					}
					return exec.Command("true")
				},
			}
			_, err := converter.Convert(context.Background(), "registry.access.redhat.com", "ubi9/ubi-micro", digest, digest)
			if test.wantOK {
				if err != nil {
					t.Fatalf("Convert(amd64) error = %v", err)
				}
				if nydusCalls != 1 {
					t.Errorf("nydusify calls = %d, want 1", nydusCalls)
				}
				published, ok := registry.manifests[repoPath+":"+targetTag]
				if !ok {
					t.Fatal("amd64 digest conversion did not publish a nydus index")
				}
				var index indexDocument
				if err := json.Unmarshal(published.Body, &index); err != nil {
					t.Fatal(err)
				}
				if len(index.Manifests) != 2 || index.Manifests[0].Platform == nil ||
					index.Manifests[0].Platform.OS != "linux" || index.Manifests[0].Platform.Architecture != "amd64" {
					t.Errorf("published manifests = %+v", index.Manifests)
				}
				return
			}
			if !errors.Is(err, ErrNonConvertible) {
				t.Fatalf("Convert(%s) error = %v, want ErrNonConvertible", test.arch, err)
			}
			if nydusCalls != 0 {
				t.Errorf("nydusify calls = %d, want 0", nydusCalls)
			}
			if _, ok := registry.manifests[repoPath+":"+targetTag]; ok {
				t.Error("non-convertible source published a nydus index")
			}
			if _, ok := registry.manifests[repoPath+":"+digest]; !ok {
				t.Error("source manifest must stay available")
			}
		})
	}
}
