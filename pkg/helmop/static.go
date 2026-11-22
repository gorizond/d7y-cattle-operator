package helmop

import (
	"fmt"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

const defaultStaticReleaseName = "p2p-static"

// SharedStatic returns the singleton p2p-static HelmOp for the manager cluster.
// It owns the static asset server and renders its populate plan from the
// operator versions/repositories/fallback checksums. The chart prefers the
// release-provided sidecar checksum before falling back to these pins.
func SharedStatic(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	releaseName := c.StaticReleaseName
	if releaseName == "" {
		releaseName = defaultStaticReleaseName
	}
	op := newSharedHelmOp(releaseName, TargetNamespace, c, c.StaticChart, "static",
		staticValues(c), nil, ManagerSelector())
	return op
}

func staticValues(c Cluster) map[string]any {
	clientVersion := c.StaticClientVersion
	nydusVersion := c.StaticNydusVersion
	snapshotterVersion := c.StaticSnapshotterVersion
	managerEnabled := c.StaticManagerHost != ""

	clientArches := []any{
		map[string]any{
			"arch":        "amd64",
			"url":         clientReleaseURL(c.StaticClientRepo, clientVersion, "amd64"),
			"sha256":      c.StaticClientSHA256AMD64,
			"tarballRoot": clientReleaseRoot(clientVersion, "amd64"),
		},
		map[string]any{
			"arch":        "arm64",
			"url":         clientReleaseURL(c.StaticClientRepo, clientVersion, "arm64"),
			"sha256":      c.StaticClientSHA256ARM64,
			"tarballRoot": clientReleaseRoot(clientVersion, "arm64"),
		},
	}
	nydusArches := []any{
		map[string]any{
			"arch":          "amd64",
			"tarballURL":    nydusReleaseURL(c.StaticNydusRepo, nydusVersion, "amd64"),
			"tarballSHA256": c.StaticNydusSHA256AMD64,
		},
		map[string]any{
			"arch":          "arm64",
			"tarballURL":    nydusReleaseURL(c.StaticNydusRepo, nydusVersion, "arm64"),
			"tarballSHA256": c.StaticNydusSHA256ARM64,
		},
	}
	snapshotterArches := []any{
		map[string]any{
			"arch":   "amd64",
			"asset":  snapshotterAsset(snapshotterVersion, "amd64"),
			"url":    snapshotterReleaseURL(c.StaticSnapshotterRepo, snapshotterVersion, "amd64"),
			"sha256": c.StaticSnapshotterSHA256AMD64,
		},
		map[string]any{
			"arch":   "arm64",
			"asset":  snapshotterAsset(snapshotterVersion, "arm64"),
			"url":    snapshotterReleaseURL(c.StaticSnapshotterRepo, snapshotterVersion, "arm64"),
			"sha256": c.StaticSnapshotterSHA256ARM64,
		},
	}

	return map[string]any{
		"manager": map[string]any{
			"enabled":   managerEnabled,
			"host":      c.StaticManagerHost,
			"tlsSecret": c.StaticTLSSecret,
		},
		"ingress": map[string]any{
			"className": c.StaticIngressClassName,
		},
		"static": map[string]any{
			"populate": map[string]any{
				"enabled": true,
				"client": map[string]any{
					"enabled":       true,
					"compatVersion": clientVersion,
					"versions": []any{
						map[string]any{
							"version": clientVersion,
							"arches":  clientArches,
						},
					},
				},
				"nydus": map[string]any{
					"enabled":     true,
					"version":     nydusVersion,
					"tarballRoot": "nydus-static",
					"binaries":    []any{"nydusd", "nydus-image", "nydus-overlayfs"},
					"arches":      nydusArches,
				},
				"snapshotter": map[string]any{
					"enabled": true,
					"version": snapshotterVersion,
					"arches":  snapshotterArches,
				},
				"selector": map[string]any{"enabled": true},
			},
		},
	}
}

func clientReleaseArch(arch string) string {
	if arch == "arm64" {
		return "aarch64"
	}
	return "x86_64"
}

func clientReleaseRoot(version, arch string) string {
	return fmt.Sprintf("dragonfly-client-%s-%s-unknown-linux-gnu", version, clientReleaseArch(arch))
}

func clientReleaseURL(repo, version, arch string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s.tar.gz",
		repo, version, clientReleaseRoot(version, arch))
}

func nydusReleaseURL(repo, version, arch string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/nydus-static-%s-linux-%s.tgz",
		repo, version, version, arch)
}

func snapshotterAsset(version, arch string) string {
	return fmt.Sprintf("nydus-snapshotter-%s-linux-%s.tar.gz", version, arch)
}

func snapshotterReleaseURL(repo, version, arch string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
		repo, version, snapshotterAsset(version, arch))
}
