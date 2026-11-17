package registryproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const dockerManifestListType = "application/vnd.docker.distribution.manifest.list.v2+json"

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
	if err := c.copyTree(ctx, sourceRepo, mirrorRepo, ref, manifest); err != nil {
		return "", err
	}
	return manifest.Digest, nil
}

// copyTree copies an OCI index, Docker manifest list, or single manifest.
// Index children are pushed by digest before the top manifest is tagged.
func (c *Copier) copyTree(ctx context.Context, sourceRepo, mirrorRepo, ref string, manifest Manifest) error {
	if isIndexMediaType(manifest.MediaType) {
		var index struct {
			Manifests []struct {
				MediaType string `json:"mediaType"`
				Digest    string `json:"digest"`
			} `json:"manifests"`
		}
		if err := json.Unmarshal(manifest.Body, &index); err != nil {
			return fmt.Errorf("parse source index: %w", err)
		}
		for _, child := range index.Manifests {
			if child.Digest == "" {
				continue
			}
			childManifest, err := c.Source.GetManifest(ctx, sourceRepo, child.Digest)
			if err != nil {
				return fmt.Errorf("read source child manifest %s: %w", child.Digest, err)
			}
			if childManifest.MediaType == "" {
				childManifest.MediaType = child.MediaType
			}
			if err := c.copyTree(ctx, sourceRepo, mirrorRepo, child.Digest, childManifest); err != nil {
				return err
			}
		}
		if err := c.Mirror.PutManifest(ctx, mirrorRepo, ref, manifest.MediaType, manifest.Body); err != nil {
			return fmt.Errorf("write mirror manifest %s: %w", ref, err)
		}
		return nil
	}
	digests, err := manifestBlobs(manifest.Body)
	if err != nil {
		return err
	}
	for _, digest := range digests {
		blob, err := c.Source.GetBlob(ctx, sourceRepo, digest)
		if err != nil {
			return fmt.Errorf("read source blob %s: %w", digest, err)
		}
		if err := c.Mirror.PutBlob(ctx, mirrorRepo, digest, blob); err != nil {
			return fmt.Errorf("write mirror blob %s: %w", digest, err)
		}
	}
	if err := c.Mirror.PutManifest(ctx, mirrorRepo, ref, manifest.MediaType, manifest.Body); err != nil {
		return fmt.Errorf("write mirror manifest %s: %w", ref, err)
	}
	return nil
}

func isIndexMediaType(mediaType string) bool {
	if idx := strings.IndexByte(mediaType, ';'); idx >= 0 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}
	return mediaType == ociIndexType || mediaType == dockerManifestListType
}
