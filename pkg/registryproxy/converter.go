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
	// sourceReferenceKey is written by acceleration-service (via nydusify) on
	// the nydus child manifest. It records the source image reference used for
	// the conversion; when it points at the upstream registry, nydus resolves
	// an image the proxy cannot serve with its credentials/referrers.
	sourceReferenceKey = "containerd.io/snapshot/nydus-source-reference"
	ociManifestType    = "application/vnd.oci.image.manifest.v1+json"
	ociIndexType       = "application/vnd.oci.image.index.v1+json"
	defaultPlatform    = "linux/amd64"
)

// Converter runs nydusify against the writable mirror copy and verifies the
// resulting OCI index before moving it to the user-visible tag.
type Converter struct {
	HarborURL     string
	MirrorProject string
	// ProxyPublicURL is the public proxy base URL (for example
	// https://proxy.example.test). The nydus source-reference annotation is
	// rewritten to a registry reference under this host so nydus does not
	// resolve the upstream registry directly.
	ProxyPublicURL string
	NydusifyPath   string
	NydusImage     string
	Platform       string
	WorkDir        string
	Timeout        time.Duration
	SourceUser     string
	SourcePass     string
	Registry       Registry
	Command        func(ctx context.Context, name string, args ...string) *exec.Cmd
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
	Variant      string `json:"variant,omitempty"`
}

// Convert copies/tag-moves nydusify output to <tag> in the mirror project.
// The source proxy project is not referenced here and is never written.
//
// A multiarch source is converted through its target-platform child digest:
// nydusify must not receive the multiarch tag because Harbor rejects indexes
// that reference unreferenced children (unknown/unknown attestations). The
// published index keeps every real source platform descriptor and appends the
// nydus artifact descriptor.
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
		platform = defaultPlatform
	}
	platformOS, platformArch := splitPlatform(platform)

	sourceManifest, err := c.Registry.GetManifest(ctx, repoPath, tag)
	if err != nil {
		return ConversionResult{}, fmt.Errorf("read conversion source: %w", err)
	}
	sourceDescriptors, err := publishedSourceDescriptors(sourceManifest, platformOS, platformArch)
	if err != nil {
		return ConversionResult{}, err
	}
	if isIndexMediaType(sourceManifest.MediaType) {
		child, ok := platformDescriptor(sourceManifest.Body, platformOS, platformArch)
		if !ok {
			return ConversionResult{}, fmt.Errorf("source index has no %s image manifest", platform)
		}
		source = host + "/" + repoPath + "@" + child.Digest
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
	verified, err := verifyIndex(runCtx, c.Registry, repoPath, merged, platform)
	if err != nil {
		return ConversionResult{}, err
	}
	proxyRef := func() (string, error) {
		return proxySourceReference(c.ProxyPublicURL, registry, repo, tag)
	}
	nydusDescriptor := verified.Descriptor
	rewritten, nydusChanged, err := rewriteNydusManifestSource(verified.Nydus, proxyRef)
	if err != nil {
		return ConversionResult{}, err
	}
	if nydusChanged {
		// The child digest changed, so push the rewritten manifest before
		// publishing an index that points at it.
		mediaType := rewritten.MediaType
		if mediaType == "" {
			mediaType = ociManifestType
		}
		if err := c.Registry.PutManifest(runCtx, repoPath, rewritten.Digest, mediaType, rewritten.Body); err != nil {
			return ConversionResult{}, fmt.Errorf("push rewritten nydus manifest: %w", err)
		}
		nydusDescriptor.Digest = rewritten.Digest
		nydusDescriptor.Size = int64(len(rewritten.Body))
	}

	annotations := map[string]string{sourceDigestKey: sourceDigest}
	if _, err := rewriteNydusSourceReferences(annotations, proxyRef); err != nil {
		return ConversionResult{}, err
	}
	index := indexDocument{
		SchemaVersion: 2,
		MediaType:     ociIndexType,
		Manifests:     append(sourceDescriptors, nydusDescriptor),
		Annotations:   annotations,
	}
	body, err := json.Marshal(index)
	if err != nil {
		return ConversionResult{}, err
	}
	if err := c.Registry.PutManifest(runCtx, repoPath, targetTag, ociIndexType, body); err != nil {
		return ConversionResult{}, fmt.Errorf("move merged index to tag %s: %w", targetTag, err)
	}
	sum := sha256.Sum256(body)
	return ConversionResult{
		IndexDigest:  "sha256:" + hex.EncodeToString(sum[:]),
		SourceDigest: sourceDigest,
	}, nil
}

// publishedSourceDescriptors returns the real-platform descriptors that make
// up the non-nydus part of the published index. A single-manifest source gets
// one descriptor for the target platform.
func publishedSourceDescriptors(manifest Manifest, platformOS, platformArch string) ([]indexDescriptor, error) {
	if isIndexMediaType(manifest.MediaType) {
		_, descriptors, err := filterImageIndex(manifest.Body)
		return descriptors, err
	}
	mediaType := manifest.MediaType
	if mediaType == "" {
		mediaType = ociManifestType
	}
	return []indexDescriptor{{
		MediaType: mediaType,
		Digest:    manifest.Digest,
		Size:      int64(len(manifest.Body)),
		Platform:  &indexPlatform{OS: platformOS, Architecture: platformArch},
	}}, nil
}

// platformDescriptor finds the runnable image descriptor for the requested
// platform inside a source index.
func platformDescriptor(body []byte, platformOS, platformArch string) (indexDescriptor, bool) {
	_, descriptors, err := filterImageIndex(body)
	if err != nil {
		return indexDescriptor{}, false
	}
	for _, descriptor := range descriptors {
		if descriptor.Platform == nil {
			continue
		}
		if descriptor.Platform.OS == platformOS && descriptor.Platform.Architecture == platformArch {
			return descriptor, true
		}
	}
	return indexDescriptor{}, false
}

func splitPlatform(platform string) (platformOS, platformArch string) {
	parts := strings.SplitN(platform, "/", 3)
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "linux", "amd64"
}

// proxySourceReference builds the public registry reference for a converted
// image. The scheme is dropped because OCI references are host/path; a public
// path prefix is kept as the first repository path segment.
func proxySourceReference(publicURL, registry, repo, ref string) (string, error) {
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("proxy.public_url is required for nydus source rewriting")
	}
	base := parsed.Host
	if path := strings.Trim(parsed.Path, "/"); path != "" {
		base += "/" + path
	}
	target := base + "/" + strings.Trim(registry, "/") + "/" + strings.Trim(repo, "/")
	if strings.HasPrefix(ref, "sha256:") {
		return target + "@" + ref, nil
	}
	return target + ":" + ref, nil
}

// rewriteNydusSourceReferences rewrites the upstream source-reference
// annotation(s) to the proxy reference and always sets the canonical key so a
// fresh conversion is observable on both the index and the nydus child.
func rewriteNydusSourceReferences(annotations map[string]string, proxyRef func() (string, error)) (bool, error) {
	if annotations == nil {
		return false, nil
	}
	target, err := proxyRef()
	if err != nil {
		return false, err
	}
	changed := false
	found := false
	for key := range annotations {
		if key != sourceReferenceKey && key != "nydus-source-reference" &&
			!strings.HasSuffix(key, "/nydus-source-reference") {
			continue
		}
		found = true
		if annotations[key] != target {
			annotations[key] = target
			changed = true
		}
	}
	if !found {
		annotations[sourceReferenceKey] = target
		changed = true
	}
	return changed, nil
}

// rewriteNydusManifestSource rewrites annotations on the nydus child manifest
// and returns a manifest with a new digest and size when anything changed.
func rewriteNydusManifestSource(manifest Manifest, proxyRef func() (string, error)) (Manifest, bool, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(manifest.Body, &doc); err != nil {
		return manifest, false, fmt.Errorf("parse nydus manifest: %w", err)
	}
	annotations := map[string]string{}
	if raw, ok := doc["annotations"]; ok {
		if err := json.Unmarshal(raw, &annotations); err != nil {
			return manifest, false, fmt.Errorf("parse nydus manifest annotations: %w", err)
		}
	}
	changed, err := rewriteNydusSourceReferences(annotations, proxyRef)
	if err != nil || !changed {
		return manifest, false, err
	}
	raw, err := json.Marshal(annotations)
	if err != nil {
		return manifest, false, err
	}
	doc["annotations"] = raw
	body, err := json.Marshal(doc)
	if err != nil {
		return manifest, false, err
	}
	sum := sha256.Sum256(body)
	return Manifest{
		Digest:    "sha256:" + hex.EncodeToString(sum[:]),
		MediaType: manifest.MediaType,
		Body:      body,
	}, true, nil
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

type verifiedIndex struct {
	Descriptor indexDescriptor
	Nydus      Manifest
}

// verifyIndex locates the nydus artifact by artifactType and platform instead
// of assuming a fixed descriptor position, then checks the bootstrap layer.
func verifyIndex(ctx context.Context, registry Registry, repoPath string, manifest Manifest, platform string) (*verifiedIndex, error) {
	if !strings.Contains(manifest.MediaType, "index") {
		return nil, fmt.Errorf("conversion output is not an OCI index (media type %q)", manifest.MediaType)
	}
	var index indexDocument
	if err := json.Unmarshal(manifest.Body, &index); err != nil {
		return nil, fmt.Errorf("parse converted index: %w", err)
	}
	platformOS, platformArch := splitPlatform(platform)
	for _, descriptor := range index.Manifests {
		if descriptor.ArtifactType != nydusArtifactType || descriptor.Platform == nil {
			continue
		}
		if descriptor.Platform.OS != platformOS || descriptor.Platform.Architecture != platformArch {
			continue
		}
		nydusManifest, err := registry.GetManifest(ctx, repoPath, descriptor.Digest)
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
		return &verifiedIndex{Descriptor: descriptor, Nydus: nydusManifest}, nil
	}
	return nil, fmt.Errorf("converted index has no nydus artifact for %s", platform)
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
