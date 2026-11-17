{{- define "nydus-resolver.labels" -}}
app.kubernetes.io/name: nydus-resolver
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "nydus-resolver.selectorLabels" -}}
app.kubernetes.io/name: nydus-resolver
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
