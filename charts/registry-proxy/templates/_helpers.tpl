{{- define "registry-proxy.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- printf "%s-registry-proxy" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name -}}
{{- end -}}
{{- end -}}

{{- define "registry-proxy.labels" -}}
app.kubernetes.io/name: registry-proxy
app.kubernetes.io/instance: {{ include "registry-proxy.fullname" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "registry-proxy.selectorLabels" -}}
app.kubernetes.io/name: registry-proxy
app.kubernetes.io/instance: {{ include "registry-proxy.fullname" . }}
{{- end -}}
