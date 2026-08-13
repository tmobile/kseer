{{/*
Render-time guards.

Every check here catches a configuration that installs cleanly but does not
work — most often a component pointed at a ClickHouse endpoint, database, or
table that another component is not using, which shows up as an empty UI rather
than an error. Failing at `helm template` time is cheaper than debugging it in
the cluster.
*/}}
{{- define "kseer.validate" -}}
{{- $ch := .Values.clickhouse -}}
{{- $schema := .Values.schema -}}
{{- $ui := .Values.ui -}}
{{- $agent := .Values.agent | default dict -}}
{{- $agentOn := dig "enabled" false $agent -}}
{{- $agentCh := dig "outputs" "clickhouse" (dict) $agent -}}
{{- $agentChOn := and $agentOn (dig "enabled" false $agentCh) -}}

{{- if not (or $ch.enabled $schema.enabled $ui.enabled $agentOn) }}
  {{- fail "kseer: every component is disabled, so this release would create nothing. Enable at least one of clickhouse, schema, ui, or agent." }}
{{- end }}

{{/* ── Bundled ClickHouse ─────────────────────────────────────────────────── */}}
{{- if $ch.enabled }}
  {{- if and (not $ch.auth.password) (not $ch.auth.existingSecret) }}
    {{- fail "kseer: clickhouse.enabled=true requires a password. Set clickhouse.auth.password, or point clickhouse.auth.existingSecret at a Secret with `username` and `password` keys. This chart ships no default password on purpose." }}
  {{- end }}
  {{- if not $ch.auth.username }}
    {{- fail "kseer: clickhouse.auth.username is required when the bundled ClickHouse is enabled." }}
  {{- end }}
  {{- if gt (int $ch.replicaCount) 1 }}
    {{- fail "kseer: the bundled ClickHouse is a single node — clickhouse.replicaCount must be 1. Replication needs ClickHouse Keeper, which this chart does not deploy; run a real cluster (or the ClickHouse operator) and point the components at it with schema.clickhouse.url, ui.clickhouse.url, and agent.outputs.clickhouse.host." }}
  {{- end }}
{{- end }}

{{/* ── Schema Job ─────────────────────────────────────────────────────────── */}}
{{- if $schema.enabled }}
  {{- if not (has $schema.mode (list "single" "distributed")) }}
    {{- fail (printf "kseer: schema.mode must be \"single\" or \"distributed\", got %q." $schema.mode) }}
  {{- end }}
  {{- if and (not $schema.clickhouse.url) (not $ch.enabled) }}
    {{- fail "kseer: schema.enabled=true needs somewhere to apply the schema. Set schema.clickhouse.url to your ClickHouse HTTP endpoint (e.g. https://clickhouse.example.com:8443), or set clickhouse.enabled=true to use the bundled one." }}
  {{- end }}
  {{- if not $schema.database }}
    {{- fail "kseer: schema.database is required." }}
  {{- end }}
  {{- if eq $schema.mode "distributed" }}
    {{- if and $ch.enabled (not $schema.clickhouse.url) }}
      {{- fail "kseer: schema.mode=distributed cannot be applied to the bundled ClickHouse. The distributed schema uses ReplicatedMergeTree and ON CLUSTER DDL, which require ClickHouse Keeper and a configured cluster; the bundled single node has neither. Use schema.mode=single, or point schema.clickhouse.url at a real cluster." }}
    {{- end }}
    {{- if not $schema.cluster }}
      {{- fail "kseer: schema.mode=distributed requires schema.cluster (a cluster name from system.clusters)." }}
    {{- end }}
  {{- end }}
  {{/* The bundled server only creates the one database named in clickhouse.database. */}}
  {{- if and $ch.enabled (not $schema.clickhouse.url) (ne $schema.database $ch.database) }}
    {{- fail (printf "kseer: schema.database (%q) and clickhouse.database (%q) differ, but the bundled ClickHouse only creates the database named in clickhouse.database, so the schema Job would fail on a missing database. Set both to the same value." $schema.database $ch.database) }}
  {{- end }}
{{- end }}

{{/* ── UI ─────────────────────────────────────────────────────────────────── */}}
{{- if $ui.enabled }}
  {{- if and (not $ui.clickhouse.url) (not $ch.enabled) }}
    {{- fail "kseer: ui.enabled=true needs a ClickHouse endpoint to proxy queries to. Set ui.clickhouse.url (the HTTP/HTTPS endpoint the UI's nginx forwards /clickhouse to), or set clickhouse.enabled=true to use the bundled one." }}
  {{- end }}
  {{- if and $ui.ingress.enabled (not $ui.ingress.host) }}
    {{- fail "kseer: ui.ingress.enabled=true requires ui.ingress.host." }}
  {{- end }}
  {{- if and $ui.azure.enabled (or (not $ui.azure.tenantId) (not $ui.azure.clientId)) }}
    {{- fail "kseer: ui.azure.enabled=true requires ui.azure.tenantId and ui.azure.clientId." }}
  {{- end }}
{{- end }}

{{/* ── Agent ──────────────────────────────────────────────────────────────── */}}
{{- if $agentChOn }}
  {{- if not (dig "host" "" $agentCh) }}
    {{- fail (printf "kseer: agent.outputs.clickhouse.host is required — the agent writes over the native protocol and has no default. For the bundled ClickHouse use agent.outputs.clickhouse.host=%s (port %v); otherwise use your own host." (include "kseer.clickhouse.fullname" .) .Values.clickhouse.service.nativePort) }}
  {{- end }}
  {{- if not (dig "database" "" $agentCh) }}
    {{- fail "kseer: agent.outputs.clickhouse.database is required." }}
  {{- end }}
  {{- if not (dig "table" "" $agentCh) }}
    {{- fail "kseer: agent.outputs.clickhouse.table is required." }}
  {{- end }}
{{- end }}

{{/* ── Cross-component contract ───────────────────────────────────────────── */}}
{{/*
All three components must agree on database and table or the pipeline silently
goes nowhere: the agent writes to one table while the UI reads another.
*/}}
{{- if and $schema.enabled $agentChOn }}
  {{- if ne (dig "database" "" $agentCh) $schema.database }}
    {{- fail (printf "kseer: agent.outputs.clickhouse.database (%q) does not match schema.database (%q). The agent would write to a database this release did not provision." (dig "database" "" $agentCh) $schema.database) }}
  {{- end }}
  {{- if ne (dig "table" "" $agentCh) "kubernetes_events" }}
    {{- fail (printf "kseer: agent.outputs.clickhouse.table is %q, but the schema Job creates \"kubernetes_events\". Set agent.outputs.clickhouse.table=kubernetes_events, or disable the schema Job and manage the schema yourself." (dig "table" "" $agentCh)) }}
  {{- end }}
{{- end }}
{{- if and $schema.enabled $ui.enabled }}
  {{- if ne $ui.clickhouse.database $schema.database }}
    {{- fail (printf "kseer: ui.clickhouse.database (%q) does not match schema.database (%q). The UI would query a database this release did not provision." $ui.clickhouse.database $schema.database) }}
  {{- end }}
  {{- if ne $ui.clickhouse.table "kubernetes_events" }}
    {{- fail (printf "kseer: ui.clickhouse.table is %q, but the schema Job creates \"kubernetes_events\"." $ui.clickhouse.table) }}
  {{- end }}
{{- end }}
{{- if and $ui.enabled $agentChOn }}
  {{- if ne $ui.clickhouse.table (dig "table" "" $agentCh) }}
    {{- fail (printf "kseer: ui.clickhouse.table (%q) does not match agent.outputs.clickhouse.table (%q); the UI would read a table the agent never writes to." $ui.clickhouse.table (dig "table" "" $agentCh)) }}
  {{- end }}
  {{- if ne $ui.clickhouse.database (dig "database" "" $agentCh) }}
    {{- fail (printf "kseer: ui.clickhouse.database (%q) does not match agent.outputs.clickhouse.database (%q)." $ui.clickhouse.database (dig "database" "" $agentCh)) }}
  {{- end }}
{{- end }}
{{- end -}}
