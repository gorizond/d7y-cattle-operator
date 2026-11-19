package registryproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	dockerManifestListType = "application/vnd.docker.distribution.manifest.list.v2+json"
	dockerManifestType     = "application/vnd.docker.distribution.manifest.v2+json"
)

// Copier copies an image from the read-only Harbor proxy project into the
// writable mirror project. The source project is never written to.
type Copier struct {
	Source        Registry
	Mirror        Registry
	MirrorProject string
}

// Copy copies registry/repo:ref into <mirror>/<registry>/<repo>:ref and
// returns the source manifest digest.
func (c *Copier) Copy(ctx context.Context, registry, repo, ref string) (string, error) {
	if c.Source == nil || c.Mirror == nil {
		return "", fmt.Errorf("copier is not configured")
	}
	if c.MirrorProject == "" {
		return "", fmt.Errorf("mirror project is empty")
	}
	sourceRepo := ProjectName(registry) + "/" + strings.TrimPrefix(repo, "/")
	mirrorRepo := c.MirrorProject + "/" + strings.TrimSuffix(registry, "/") + "/" + strings.TrimPrefix(repo, "/")
	manifest, err := c.Source.GetManifest(ctx, sourceRepo, ref)
	if err != nil {
		return "", fmt.Errorf("read source manifest: %w", err)
	}
	if err := c.copyTree(ctx, sourceRepo, mirrorRepo, ref, manifest, true); err != nil {
		return "", err
	}
	return manifest.Digest, nil
}

// copyTree copies an OCI index, Docker manifest list, or single manifest.
// Index children are pushed by digest before the top manifest is tagged.
// Attestation manifests (unknown/unknown platforms and artifact descriptors)
// are dropped from the top-level index: Harbor rejects an index that
// references content the mirror does not carry, and nydus conversion ignores
// them. Nested indexes are copied verbatim so their references stay intact.
func (c *Copier) copyTree(ctx context.Context, sourceRepo, mirrorRepo, ref string, manifest Manifest, filter bool) error {
	if isIndexMediaType(manifest.MediaType) {
		body := manifest.Body
		children, err := indexDescriptors(manifest.Body)
		if err != nil {
			return err
		}
		if filter {
			body, children, err = filterImageIndex(manifest.Body)
			if err != nil {
				return err
			}
		}
		for _, child := range children {
			childManifest, err := c.Source.GetManifest(ctx, sourceRepo, child.Digest)
			if err != nil {
				return fmt.Errorf("read source child manifest %s: %w", child.Digest, err)
			}
			if childManifest.MediaType == "" {
				childManifest.MediaType = child.MediaType
			}
			if err := c.copyTree(ctx, sourceRepo, mirrorRepo, child.Digest, childManifest, false); err != nil {
				return err
			}
		}
		if err := c.Mirror.PutManifest(ctx, mirrorRepo, ref, manifest.MediaType, body); err != nil {
			return fmt.Errorf("write mirror manifest %s: %w", ref, err)
		}
		return nil
	}
	digests, err := manifestBlobs(manifest.Body)
	if err != nil {
		return err
	}
	for _, digest := range digests {
		if err := c.copyBlob(ctx, sourceRepo, mirrorRepo, digest); err != nil {
			return err
		}
	}
	if err := c.Mirror.PutManifest(ctx, mirrorRepo, ref, manifest.MediaType, manifest.Body); err != nil {
		return fmt.Errorf("write mirror manifest %s: %w", ref, err)
	}
	return nil
}

// copyBlob streams one source blob into the mirror without buffering it.
func (c *Copier) copyBlob(ctx context.Context, sourceRepo, mirrorRepo, digest string) error {
	body, size, _, err := c.Source.GetBlob(ctx, sourceRepo, digest, "")
	if err != nil {
		return fmt.Errorf("read source blob %s: %w", digest, err)
	}
	defer body.Close()
	if err := c.Mirror.PutBlob(ctx, mirrorRepo, digest, body, size); err != nil {
		return fmt.Errorf("write mirror blob %s: %w", digest, err)
	}
	return nil
}

// rawIndexDocument keeps descriptors as raw JSON so platform variants,
// annotations, and unknown fields survive the attestation filter.
type rawIndexDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	Manifests     []json.RawMessage `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// indexDescriptors parses every descriptor of an index.
func indexDescriptors(body []byte) ([]indexDescriptor, error) {
	var doc rawIndexDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse source index: %w", err)
	}
	descriptors := make([]indexDescriptor, 0, len(doc.Manifests))
	for _, raw := range doc.Manifests {
		var descriptor indexDescriptor
		if err := json.Unmarshal(raw, &descriptor); err != nil {
			return nil, fmt.Errorf("parse source index descriptor: %w", err)
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, nil
}

// filterImageIndex drops unknown/unknown attestation descriptors and keeps
// every real image-platform descriptor. It returns the filtered index body
// and the kept descriptors.
func filterImageIndex(body []byte) ([]byte, []indexDescriptor, error) {
	var doc rawIndexDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse source index: %w", err)
	}
	kept := make([]json.RawMessage, 0, len(doc.Manifests))
	descriptors := make([]indexDescriptor, 0, len(doc.Manifests))
	for _, raw := range doc.Manifests {
		var descriptor indexDescriptor
		if err := json.Unmarshal(raw, &descriptor); err != nil {
			return nil, nil, fmt.Errorf("parse source index descriptor: %w", err)
		}
		if !isRealImageDescriptor(descriptor) {
			continue
		}
		kept = append(kept, raw)
		descriptors = append(descriptors, descriptor)
	}
	if len(kept) == 0 {
		return nil, nil, fmt.Errorf("source index has no image-platform manifests")
	}
	doc.Manifests = kept
	filtered, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("encode mirrored index: %w", err)
	}
	return filtered, descriptors, nil
}

// isRealImageDescriptor reports whether a descriptor points at a runnable
// image platform rather than an attestation or an unknown/unknown entry.
func isRealImageDescriptor(descriptor indexDescriptor) bool {
	if descriptor.Digest == "" || descriptor.Platform == nil {
		return false
	}
	if descriptor.Platform.OS == "unknown" || descriptor.Platform.Architecture == "unknown" {
		return false
	}
	if descriptor.ArtifactType != "" {
		return false
	}
	return descriptor.MediaType == ociManifestType ||
		descriptor.MediaType == dockerManifestType ||
		isIndexMediaType(descriptor.MediaType)
}

func isIndexMediaType(mediaType string) bool {
	if idx := strings.IndexByte(mediaType, ';'); idx >= 0 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}
	return mediaType == ociIndexType || mediaType == dockerManifestListType
}
