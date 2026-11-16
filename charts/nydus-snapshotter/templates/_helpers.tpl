{{/* Common names and labels for the nydus-snapshotter chart. */}}
{{- define "nydus-snapshotter.labels" -}}
app.kubernetes.io/name: nydus-snapshotter
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "nydus-snapshotter.selectorLabels" -}}
app.kubernetes.io/name: nydus-snapshotter
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "nydus-snapshotter.socketPath" -}}
{{ .Values.snapshotter.socketDir }}/{{ .Values.snapshotter.socket }}
{{- end -}}
