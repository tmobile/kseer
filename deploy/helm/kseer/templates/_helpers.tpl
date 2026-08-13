{{/* vim: set filetype=mustache: */}}

{{/* Chart name, overridable. */}}
{{- define "kseer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Release-scoped base name for every resource this chart owns. When the release is
already named after the chart (the common `helm install kseer ...`), the name is
not doubled up.
*/}}
{{- define "kseer.fullname" -}}
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

{{- define "kseer.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Per-component resource names. */}}
{{- define "kseer.clickhouse.fullname" -}}
{{- printf "%s-clickhouse" (include "kseer.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kseer.ui.fullname" -}}
{{- printf "%s-ui" (include "kseer.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kseer.schema.fullname" -}}
{{- printf "%s-schema" (include "kseer.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Selector labels. Deliberately excludes version and chart so that upgrades never
change an immutable Deployment/StatefulSet selector.

Call with: (dict "root" $ "component" "ui")
*/}}
{{- define "kseer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kseer.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Full label set, plus any user-supplied labels for this resource kind. Labels set
per component win over the chart-wide ones.

Call with: (dict "root" $ "component" "ui" "values" .Values.ui "kind" "deployment")
where kind is one of deployment / pod / service.
*/}}
{{- define "kseer.labels" -}}
helm.sh/chart: {{ include "kseer.chart" .root }}
{{ include "kseer.selectorLabels" (dict "root" .root "component" .component) }}
{{- if .root.Chart.AppVersion }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
{{- $global := dig .kind (dict) (.root.Values.labels | default dict) }}
{{- $local := dig .kind (dict) ((.values | default dict).labels | default dict) }}
{{- range $k, $v := merge (dict) $local $global }}
{{ $k }}: {{ $v | quote }}
{{- end }}
{{- end -}}

{{/*
Annotations for a resource kind, merging chart-wide and per-component maps.
Same call shape as kseer.labels.
*/}}
{{- define "kseer.annotations" -}}
{{- $global := dig .kind (dict) (.root.Values.annotations | default dict) }}
{{- $local := dig .kind (dict) ((.values | default dict).annotations | default dict) }}
{{- range $k, $v := merge (dict) $local $global }}
{{ $k }}: {{ $v | quote }}
{{- end }}
{{- end -}}

{{/*
Pod scheduling for a component, falling back to the chart-wide setting when the
component leaves it unset.

Call with: (dict "root" $ "values" .Values.ui)
*/}}
{{- define "kseer.scheduling" -}}
{{- $v := .values | default dict }}
{{- with ($v.nodeSelector | default .root.Values.nodeSelector) }}
nodeSelector:
{{- toYaml . | nindent 2 }}
{{- end }}
{{- with ($v.tolerations | default .root.Values.tolerations) }}
tolerations:
{{- toYaml . | nindent 2 }}
{{- end }}
{{- with ($v.affinity | default .root.Values.affinity) }}
affinity:
{{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{- define "kseer.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
{{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* ── Bundled ClickHouse endpoints ────────────────────────────────────────── */}}

{{- define "kseer.clickhouse.httpUrl" -}}
{{- printf "http://%s:%v" (include "kseer.clickhouse.fullname" .) .Values.clickhouse.service.httpPort -}}
{{- end -}}

{{/*
HTTP endpoint the schema Job talks to: an explicit URL wins, otherwise the
bundled service. Validation guarantees one of the two exists.
*/}}
{{- define "kseer.schema.clickhouseUrl" -}}
{{- if .Values.schema.clickhouse.url -}}
{{- .Values.schema.clickhouse.url -}}
{{- else -}}
{{- include "kseer.clickhouse.httpUrl" . -}}
{{- end -}}
{{- end -}}

{{/* HTTP endpoint the UI's nginx proxies /clickhouse to. */}}
{{- define "kseer.ui.proxyUrl" -}}
{{- if .Values.ui.clickhouse.url -}}
{{- .Values.ui.clickhouse.url -}}
{{- else -}}
{{- include "kseer.clickhouse.httpUrl" . -}}
{{- end -}}
{{- end -}}

{{/* ── Credential Secret resolution ────────────────────────────────────────── */}}

{{/*
Secret holding the bundled ClickHouse admin user. Created by this chart unless
the user points at their own.
*/}}
{{- define "kseer.clickhouse.secretName" -}}
{{- if .Values.clickhouse.auth.existingSecret -}}
{{- .Values.clickhouse.auth.existingSecret -}}
{{- else -}}
{{- include "kseer.clickhouse.fullname" . -}}
{{- end -}}
{{- end -}}

{{/*
Secret the schema Job authenticates with. Preference order: an explicit
existingSecret, then the bundled ClickHouse Secret (so a bundled install needs
credentials in exactly one place), then a Secret this chart creates from
schema.clickhouse.auth.username/password.
*/}}
{{- define "kseer.schema.secretName" -}}
{{- if .Values.schema.clickhouse.auth.existingSecret -}}
{{- .Values.schema.clickhouse.auth.existingSecret -}}
{{- else if and .Values.clickhouse.enabled (not .Values.schema.clickhouse.url) -}}
{{- include "kseer.clickhouse.secretName" . -}}
{{- else -}}
{{- include "kseer.schema.fullname" . -}}
{{- end -}}
{{- end -}}

{{/* True when this chart must create the schema Job's credential Secret. */}}
{{- define "kseer.schema.createSecret" -}}
{{- if and .Values.schema.enabled (not .Values.schema.clickhouse.auth.existingSecret) -}}
{{- if not (and .Values.clickhouse.enabled (not .Values.schema.clickhouse.url)) -}}
true
{{- end -}}
{{- end -}}
{{- end -}}
