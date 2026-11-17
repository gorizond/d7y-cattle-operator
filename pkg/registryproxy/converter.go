package registryproxy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	nydusArtifactType = "application/vnd.nydus.image.manifest.v1+json"
	nydusBootstrapKey = "containerd.io/snapshot/nydus-bootstrap"
	sourceDigestKey   = "containerd.io/snapshot/nydus-source-digest"
	ociManifestType   = "application/vnd.oci.image.manifest.v1+json"
	ociIndexType      = "application/vnd.oci.image.index.v1+json"
)

// Converter runs nydusify against the writable mirror copy and verifies the
// resulting OCI index before moving it to the user-visible tag.
type Converter struct {
	HarborURL     string
	MirrorProject string
	NydusifyPath  string
	NydusImage    string
	Platform      string
	WorkDir       string
	Timeout       time.Duration
	SourceUser    string
	SourcePass    string
	Registry      Registry
	Command       func(ctx context.Context, name string, args ...string) *exec.Cmd
}

type ConversionResult struct {
	IndexDigest  string
	SourceDigest string
}

type indexDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Manifests     []indexDescriptor `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type indexDescriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Platform     *indexPlatform    `json:"platform,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

type indexPlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

// Convert copies/tag-moves nydusify output to <tag> in the mirror project.
// The source proxy project is not referenced here and is never written.
func (c *Converter) Convert(ctx context.Context, registry, repo, tag, sourceDigest string) (ConversionResult, error) {
	if c.Registry == nil {
		return ConversionResult{}, fmt.Errorf("converter registry is nil")
	}
	if tag == "" {
		return ConversionResult{}, fmt.Errorf("conversion tag is empty")
	}
	repoPath := c.MirrorProject + "/" + strings.TrimSuffix(registry, "/") + "/" + strings.TrimPrefix(repo, "/")
	host := harborHost(c.HarborURL)
	source, targetTag, err := conversionRefs(host, repoPath, tag)
	if err != nil {
		return ConversionResult{}, err
	}
	target := host + "/" + repoPath + ":" + targetTag + "-merged"
	platform := c.Platform
	if platform == "" {
		platform = "linux/amd64"
	}
	workDir := c.WorkDir
	if workDir == "" {
		workDir = filepath.Join(os.TempDir(), "nydusify")
	}
	if err := os.MkdirAll(workDir, 0700); err != nil {
		return ConversionResult{}, err
	}
	args := []string{
		"convert",
		"--source", source,
		"--target", target,
		"--platform", platform,
		"--merge-platform",
		"--oci",
		"--nydus-image", c.NydusImage,
		"--work-dir", workDir,
	}
	dockerConfigDir, err := writeDockerConfig(host, c.SourceUser, c.SourcePass)
	if err != nil {
		return ConversionResult{}, err
	}
	if dockerConfigDir != "" {
		defer os.RemoveAll(dockerConfigDir)
	}
	command := c.Command
	if command == nil {
		command = exec.CommandContext
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := command(runCtx, c.NydusifyPath, args...)
	if dockerConfigDir != "" {
		cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+dockerConfigDir)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ConversionResult{}, fmt.Errorf("nydusify convert: %w: %s", err, strings.TrimSpace(string(output)))
	}

	merged, err := c.Registry.GetManifest(runCtx, repoPath, targetTag+"-merged")
	if err != nil {
		return ConversionResult{}, fmt.Errorf("read merged index: %w", err)
	}
	index, err := verifyIndex(runCtx, c.Registry, repoPath, merged)
	if err != nil {
		return ConversionResult{}, err
	}
	if index.Annotations == nil {
		index.Annotations = map[string]string{}
	}
	changed := false
	if index.Annotations[sourceDigestKey] == "" {
		index.Annotations[sourceDigestKey] = sourceDigest
		changed = true
	}
	body, err := json.Marshal(index)
	if err != nil {
		return ConversionResult{}, err
	}
	if err := c.Registry.PutManifest(runCtx, repoPath, targetTag, ociIndexType, body); err != nil {
		return ConversionResult{}, fmt.Errorf("move merged index to tag %s: %w", targetTag, err)
	}
	indexDigest := merged.Digest
	if changed {
		sum := sha256.Sum256(body)
		indexDigest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return ConversionResult{IndexDigest: indexDigest, SourceDigest: sourceDigest}, nil
}

// conversionRefs builds nydusify source/target references. A digest source
// must use repo@sha256:...; its converted index is tagged with a short,
// valid tag because repo:sha256:...-merged is not a legal reference.
func conversionRefs(host, repoPath, ref string) (source, targetTag string, err error) {
	if strings.HasPrefix(ref, "sha256:") {
		short := strings.TrimPrefix(ref, "sha256:")
		if len(short) > 12 {
			short = short[:12]
		}
		if short == "" {
			return "", "", fmt.Errorf("invalid digest reference %q", ref)
		}
		return host + "/" + repoPath + "@" + ref, "nd-" + short, nil
	}
	return host + "/" + repoPath + ":" + ref, ref, nil
}

func verifyIndex(ctx context.Context, registry Registry, repoPath string, manifest Manifest) (*indexDocument, error) {
	if !strings.Contains(manifest.MediaType, "index") {
		return nil, fmt.Errorf("conversion output is not an OCI index (media type %q)", manifest.MediaType)
	}
	var index indexDocument
	if err := json.Unmarshal(manifest.Body, &index); err != nil {
		return nil, fmt.Errorf("parse converted index: %w", err)
	}
	if len(index.Manifests) < 2 {
		return nil, fmt.Errorf("converted index has %d manifests, want OCI + nydus", len(index.Manifests))
	}
	oci := index.Manifests[0]
	if oci.MediaType != ociManifestType || oci.Platform == nil ||
		oci.Platform.OS != "linux" || oci.Platform.Architecture != "amd64" {
		return nil, fmt.Errorf("first index child is not the linux/amd64 OCI manifest")
	}
	nydus := index.Manifests[1]
	if nydus.ArtifactType != nydusArtifactType || nydus.Platform == nil ||
		nydus.Platform.OS != "linux" || nydus.Platform.Architecture != "amd64" {
		return nil, fmt.Errorf("second index child is not the linux/amd64 nydus manifest")
	}
	nydusManifest, err := registry.GetManifest(ctx, repoPath, nydus.Digest)
	if err != nil {
		return nil, fmt.Errorf("read nydus manifest: %w", err)
	}
	var nydusDoc struct {
		Layers []struct {
			Annotations map[string]string `json:"annotations,omitempty"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(nydusManifest.Body, &nydusDoc); err != nil {
		return nil, fmt.Errorf("parse nydus manifest: %w", err)
	}
	if len(nydusDoc.Layers) == 0 {
		return nil, fmt.Errorf("nydus manifest has no layers")
	}
	if _, ok := nydusDoc.Layers[len(nydusDoc.Layers)-1].Annotations[nydusBootstrapKey]; !ok {
		return nil, fmt.Errorf("nydus manifest has no bootstrap layer annotation")
	}
	return &index, nil
}

// writeDockerConfig creates a private temporary Docker config for nydusify.
// The robot credentials are only in the file; the command line never contains
// them. The caller removes the directory after the subprocess exits.
func writeDockerConfig(host, username, password string) (string, error) {
	if username == "" && password == "" {
		return "", nil
	}
	if host == "" {
		return "", fmt.Errorf("harbor host is empty for Docker auth")
	}
	dir, err := os.MkdirTemp("", "nydusify-docker-")
	if err != nil {
		return "", err
	}
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	config := map[string]any{"auths": map[string]any{
		host: map[string]string{"auth": auth},
	}}
	body, err := json.Marshal(config)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func harborHost(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://"), "/")
}
