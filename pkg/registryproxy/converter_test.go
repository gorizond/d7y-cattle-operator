package registryproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConverterVerifiesAndMovesIndex(t *testing.T) {
	registry := newFakeRegistry()
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
		Digest: "sha256:nydus", MediaType: ociManifestType,
		Body: []byte(`{"schemaVersion":2,"layers":[{"digest":"sha256:bootstrap","annotations":{"containerd.io/snapshot/nydus-bootstrap":""}}]}`),
	}
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", NydusifyPath: "nydusify",
		NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
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
	if movedIndex.Manifests[0].MediaType != ociManifestType || movedIndex.Manifests[1].ArtifactType != nydusArtifactType {
		t.Errorf("index order/artifact = %+v", movedIndex.Manifests)
	}
	if movedIndex.Manifests[0].Size != 1234 || movedIndex.Manifests[1].Size != 5678 {
		t.Errorf("index descriptor sizes = %d/%d, want 1234/5678",
			movedIndex.Manifests[0].Size, movedIndex.Manifests[1].Size)
	}
}

func TestConverterCommandBuildsNydusifyArgs(t *testing.T) {
	registry := newFakeRegistry()
	indexBody, _ := json.Marshal(indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: "sha256:oci", Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
	}})
	registry.manifests["mirror/docker.io/library/nginx:1.27-merged"] = Manifest{Digest: "sha256:index", MediaType: ociIndexType, Body: indexBody}
	registry.manifests["mirror/docker.io/library/nginx:sha256:nydus"] = Manifest{MediaType: ociManifestType, Body: []byte(`{"layers":[{"annotations":{"containerd.io/snapshot/nydus-bootstrap":""}}]}`)}
	var captured []string
	name := ""
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", NydusifyPath: "/usr/local/bin/nydusify",
		NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
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
		"--nydus-image /usr/local/bin/nydus-image",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
}

func TestWriteDockerConfigDoesNotExposeSecrets(t *testing.T) {
	dir, err := writeDockerConfig("harbor.example.com", "robot$proxy", "s3cret")
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
	want := base64.StdEncoding.EncodeToString([]byte("robot$proxy:s3cret"))
	if config.Auths["harbor.example.com"].Auth != want {
		t.Error("docker auth entry is wrong")
	}
	if strings.Contains(string(body), "s3cret") {
		t.Error("plaintext secret leaked into docker config")
	}
}

func TestConverterFailureLeavesMirrorTag(t *testing.T) {
	registry := newFakeRegistry()
	original := Manifest{Digest: "sha256:source", MediaType: ociManifestType, Body: []byte(`{"schemaVersion":2}`)}
	registry.manifests["mirror/docker.io/library/nginx:1.27"] = original
	converter := &Converter{
		HarborURL: "https://harbor.example.com", MirrorProject: "mirror", NydusifyPath: "nydusify",
		NydusImage: "/usr/local/bin/nydus-image", Registry: registry,
		Command: func(context.Context, string, ...string) *exec.Cmd { return exec.Command("false") },
	}
	if _, err := converter.Convert(context.Background(), "docker.io", "library/nginx", "1.27", "sha256:source"); err == nil {
		t.Fatal("Convert() error = nil")
	}
	if got := registry.manifests["mirror/docker.io/library/nginx:1.27"].Body; string(got) != string(original.Body) {
		t.Error("mirror tag was changed on conversion failure")
	}
}

func TestVerifyIndexRejectsWrongOrder(t *testing.T) {
	registry := newFakeRegistry()
	bad := indexDocument{Manifests: []indexDescriptor{
		{MediaType: ociManifestType, Digest: "sha256:nydus", ArtifactType: nydusArtifactType, Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
		{MediaType: ociManifestType, Digest: "sha256:oci", Platform: &indexPlatform{OS: "linux", Architecture: "amd64"}},
	}}
	body, _ := json.Marshal(bad)
	if _, err := verifyIndex(context.Background(), registry, "mirror/x/y", Manifest{MediaType: ociIndexType, Body: body}); err == nil {
		t.Fatal("verifyIndex() error = nil for wrong order")
	}
}
