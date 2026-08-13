{{/*
Expand the name of the chart.
*/}}
{{- define "kseer-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kseer-agent.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kseer-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kseer-agent.labels" -}}
helm.sh/chart: {{ include "kseer-agent.chart" . }}
{{ include "kseer-agent.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kseer-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kseer-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kseer-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kseer-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Resolve the namespace for kseer-agent namespaced resources.
- in-cluster + namespace.create=true → use namespace.name or the fullname
- otherwise → .Release.Namespace
*/}}
{{- define "kseer-agent.namespace" -}}
{{- if and (eq .Values.mode "in-cluster") .Values.namespace.create -}}
{{- .Values.namespace.name | default (include "kseer-agent.fullname" .) -}}
{{- else -}}
{{- .Release.Namespace -}}
{{- end -}}
{{- end }}

