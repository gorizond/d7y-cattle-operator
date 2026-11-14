{{/*
Chart name, fully qualified name, and common labels.
*/}}
{{- define "d7y-cattle-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "d7y-cattle-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "d7y-cattle-operator.labels" -}}
helm.sh/chart: {{ include "d7y-cattle-operator.name" . }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "d7y-cattle-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "d7y-cattle-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "d7y-cattle-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "d7y-cattle-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "d7y-cattle-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "d7y-cattle-operator.webhookName" -}}
{{- printf "%s-webhook" (include "d7y-cattle-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
