{{- define "dragonfly-extras.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Resolve the effective Ingress controller for the target cluster.

"auto" (the default) selects traefik only when the target cluster exposes every
Traefik CRD kind the enabled features need:
  grpc                     -> traefik.io/v1alpha1/IngressRouteTCP
  rest                     -> traefik.io/v1alpha1/IngressRoute
  rest + serversTransport  -> traefik.io/v1alpha1/ServersTransport
Anything missing, or no ingress feature enabled, falls back to nginx (the RKE2
default). Explicit "nginx"/"traefik" always wins over detection.

The templates still guard each Traefik object by its own kind, so an explicit
"traefik" with missing CRDs renders only the objects the cluster can accept and
never an unguarded or dangling reference.
*/}}
{{- define "dragonfly-extras.ingressController" -}}
{{- $controller := .Values.ingress.controller | default "auto" -}}
{{- if eq $controller "auto" -}}
{{- $traefik := or .Values.ingress.grpc.enabled .Values.ingress.rest.enabled -}}
{{- if .Values.ingress.grpc.enabled -}}
{{- $traefik = and $traefik (.Capabilities.APIVersions.Has "traefik.io/v1alpha1/IngressRouteTCP") -}}
{{- end -}}
{{- if .Values.ingress.rest.enabled -}}
{{- $traefik = and $traefik (.Capabilities.APIVersions.Has "traefik.io/v1alpha1/IngressRoute") -}}
{{- if .Values.ingress.traefik.serversTransport.enabled -}}
{{- $traefik = and $traefik (.Capabilities.APIVersions.Has "traefik.io/v1alpha1/ServersTransport") -}}
{{- end -}}
{{- end -}}
{{- if $traefik -}}traefik{{- else -}}nginx{{- end -}}
{{- else -}}
{{- $controller -}}
{{- end -}}
{{- end -}}

{{/*
Ingress class for the resolved controller. An explicit ingress.className always
wins; otherwise Traefik defaults to "traefik" and nginx stays empty so the
cluster's default IngressClass applies.
*/}}
{{- define "dragonfly-extras.ingressClassName" -}}
{{- if .Values.ingress.className -}}
{{- .Values.ingress.className -}}
{{- else if eq (include "dragonfly-extras.ingressController" .) "traefik" -}}
traefik
{{- end -}}
{{- end -}}
