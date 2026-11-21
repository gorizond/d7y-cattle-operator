{{- define "p2p-host-sync.versionsEnv" -}}
STATIC_URL={{ .Values.staticURL }}
CLIENT_VERSION={{ .Values.versions.client }}
CLIENT_RELEASE_REPO={{ .Values.repos.client }}
CLIENT_SHA256_AMD64={{ .Values.checksums.client.amd64 }}
CLIENT_SHA256_ARM64={{ .Values.checksums.client.arm64 }}
NYDUS_VERSION={{ .Values.versions.nydus }}
NYDUS_RELEASE_REPO={{ .Values.repos.nydus }}
NYDUS_GITHUB_REPO={{ .Values.repos.nydus }}
NYDUS_SHA256_AMD64={{ .Values.checksums.nydus.amd64 }}
NYDUS_SHA256_ARM64={{ .Values.checksums.nydus.arm64 }}
NYDUS_GITHUB_SHA256_AMD64={{ .Values.checksums.nydus.amd64 }}
NYDUS_GITHUB_SHA256_ARM64={{ .Values.checksums.nydus.arm64 }}
NYDUS_SNAPSHOTTER_VERSION={{ .Values.versions.snapshotter }}
NYDUS_SNAPSHOTTER_REPO={{ .Values.repos.snapshotter }}
NYDUS_SNAPSHOTTER_SHA256_AMD64={{ .Values.checksums.snapshotter.amd64 }}
NYDUS_SNAPSHOTTER_SHA256_ARM64={{ .Values.checksums.snapshotter.arm64 }}
NYDUS_SNAPSHOTTER_BINARY_SHA256_AMD64={{ .Values.checksums.snapshotterBinary.amd64 }}
NYDUS_SNAPSHOTTER_BINARY_SHA256_ARM64={{ .Values.checksums.snapshotterBinary.arm64 }}
NYDUS_ALLOW_GITHUB=1
{{- end -}}
