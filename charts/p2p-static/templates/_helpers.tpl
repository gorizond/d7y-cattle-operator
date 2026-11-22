{{/* vim: set filetype=mustache: */}}
{{- define "p2p-static.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "p2p-static.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "p2p-static.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "p2p-static.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "p2p-static.selectorLabels" -}}
app.kubernetes.io/name: {{ include "p2p-static.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}


{{- define "p2p-static.populatePlan" -}}
{{- if and $.Values.static.populate.enabled $.Values.static.populate.client.enabled (not $.Values.static.populate.client.versions) }}
{{- fail "p2p-static: static.populate.client.versions is required from the operator" }}
{{- end }}
{{- if and $.Values.static.populate.enabled $.Values.static.populate.nydus.enabled (or (not $.Values.static.populate.nydus.version) (not $.Values.static.populate.nydus.arches)) }}
{{- fail "p2p-static: static.populate.nydus.version/arches are required from the operator" }}
{{- end }}
{{- if and $.Values.static.populate.enabled $.Values.static.populate.snapshotter.enabled (or (not $.Values.static.populate.snapshotter.version) (not $.Values.static.populate.snapshotter.arches)) }}
{{- fail "p2p-static: static.populate.snapshotter.version/arches are required from the operator" }}
{{- end }}
{{- if $.Values.static.populate.client.enabled }}
{{- range $version := $.Values.static.populate.client.versions }}
{{- range $artifact := $version.arches }}
client|{{ $version.version }}|{{ $artifact.arch }}|{{ $artifact.url }}|{{ $artifact.sha256 }}|{{ $artifact.tarballRoot }}
{{- end }}
{{- end }}
{{- if $.Values.static.populate.client.compatVersion }}
compat-client|{{ $.Values.static.populate.client.compatVersion }}
{{- end }}
{{- end }}
{{- if $.Values.static.populate.nydus.enabled }}
{{- range $artifact := $.Values.static.populate.nydus.arches }}
nydus|{{ $.Values.static.populate.nydus.version }}|{{ $artifact.arch }}|{{ $artifact.tarballURL }}|{{ $artifact.tarballSHA256 }}|{{ $.Values.static.populate.nydus.tarballRoot }}
{{- end }}
{{- end }}
{{- if $.Values.static.populate.snapshotter.enabled }}
{{- range $artifact := $.Values.static.populate.snapshotter.arches }}
snapshotter|{{ $.Values.static.populate.snapshotter.version }}|{{ $artifact.arch }}|{{ $artifact.url }}|{{ $artifact.sha256 }}|{{ $artifact.asset }}
{{- end }}
{{- end }}
{{- end -}}
