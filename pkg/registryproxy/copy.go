package registryproxy

import (
	"context"
	"fmt"
	"strings"
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
	digests, err := manifestBlobs(manifest.Body)
	if err != nil {
		return "", err
	}
	for _, digest := range digests {
		blob, err := c.Source.GetBlob(ctx, sourceRepo, digest)
		if err != nil {
			return "", fmt.Errorf("read source blob %s: %w", digest, err)
		}
		if err := c.Mirror.PutBlob(ctx, mirrorRepo, digest, blob); err != nil {
			return "", fmt.Errorf("write mirror blob %s: %w", digest, err)
		}
	}
	if err := c.Mirror.PutManifest(ctx, mirrorRepo, ref, manifest.MediaType, manifest.Body); err != nil {
		return "", fmt.Errorf("write mirror manifest: %w", err)
	}
	return manifest.Digest, nil
}
