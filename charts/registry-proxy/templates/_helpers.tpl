{{- define "registry-proxy.labels" -}}
app.kubernetes.io/name: registry-proxy
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "registry-proxy.selectorLabels" -}}
app.kubernetes.io/name: registry-proxy
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
