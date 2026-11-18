{{- define "admission-webhook.labels" -}}
app.kubernetes.io/name: admission-webhook
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "admission-webhook.selectorLabels" -}}
app.kubernetes.io/name: admission-webhook
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "admission-webhook.tlsSecretName" -}}
{{- default (printf "%s-tls" .Release.Name) .Values.tls.secretName -}}
{{- end -}}

{{- define "admission-webhook.serviceAccountName" -}}
{{- default .Release.Name .Values.serviceAccount.name -}}
{{- end -}}
