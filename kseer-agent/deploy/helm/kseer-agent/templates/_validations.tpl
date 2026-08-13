{{/*
Validate ClickHouse credential configuration.

When running in-cluster with ClickHouse output enabled, exactly one credential
source must be configured:

  (A) ESO:                externalSecret.enabled=true
  (B) Helm-created Secret: outputs.clickhouse.credentialsSecret.create=true
  (C) Pre-existing Secret: outputs.clickhouse.credentialsSecret.name set
                           (with create=false and externalSecret.enabled=false)

Rules:
  - (A) and (B) are mutually exclusive (both would try to create the Secret)
  - At least one of (A), (B), or (C) must be configured
*/}}
{{- define "kseer-agent.validateClickHouseCredentials" -}}
{{- if and (eq .Values.mode "in-cluster") .Values.outputs.clickhouse.enabled }}
  {{- $eso := .Values.externalSecret.enabled }}
  {{- $create := .Values.outputs.clickhouse.credentialsSecret.create }}
  {{- $name := .Values.outputs.clickhouse.credentialsSecret.name }}
  {{- if and $eso $create }}
    {{- fail "ERROR: Enable EITHER externalSecret.enabled OR outputs.clickhouse.credentialsSecret.create, not both. Both paths would attempt to create the same Secret." }}
  {{- end }}
  {{- if not (or $eso $create $name) }}
    {{- fail "ERROR: ClickHouse credentials required. Choose one:\n  (A) Set externalSecret.enabled=true to have ESO sync credentials from Vault\n  (B) Set outputs.clickhouse.credentialsSecret.create=true with username/password to have Helm create the Secret\n  (C) Set outputs.clickhouse.credentialsSecret.name to reference an existing Secret" }}
  {{- end }}
{{- end }}
{{- end }}
