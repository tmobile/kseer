import axios from 'axios'
import { KubernetesObject, TimeRange, AggregateCount, RawEvent, ActivityBin, ActivityGroupCount, ACTIVITY_BIN_COUNT, GroupByConfig, groupDimKey, AttrFilter } from '../types'
import { getToken } from '../auth/getToken'
import { getCredentials } from '../auth/credentials'

const CLICKHOUSE_ENDPOINT = '/clickhouse'
// Use runtime config (production) or Vite env vars (local dev)
const CLICKHOUSE_DATABASE = window.env?.CLICKHOUSE_DATABASE || import.meta.env.VITE_CLICKHOUSE_DATABASE || 'kseer'
const CLICKHOUSE_TABLE = window.env?.CLICKHOUSE_TABLE || import.meta.env.VITE_CLICKHOUSE_TABLE || 'kubernetes_events'

export async function executeClickHouseQuery(query: string): Promise<any[]> {
  const token = await getToken()
  const queryWithFormat = query.trim().endsWith('FORMAT JSON')
    ? query
    : `${query} FORMAT JSON`

  const headers: Record<string, string> = {
    'Content-Type': 'text/plain; charset=utf-8',
    'X-ClickHouse-Setting-allow_experimental_json_type': '1',
    'X-ClickHouse-Setting-json_type_escape_dots_in_keys': '1',
    // Ask ClickHouse to gzip the response. The browser sends Accept-Encoding
    // automatically and decompresses transparently; the authz proxy + nginx pass
    // the encoded body through verbatim. Large aggregate JSON compresses ~10-20x.
    'X-ClickHouse-Setting-enable_http_compression': '1',
  }

  if (token) {
    // MSAL enabled — send Bearer token to authz proxy
    headers['Authorization'] = `Bearer ${token}`
  } else {
    // MSAL disabled — send ClickHouse credentials from login form
    const { username, password } = getCredentials()
    headers['Authorization'] = `Basic ${btoa(`${username}:${password}`)}`
  }

  try {
    const response = await axios.post(CLICKHOUSE_ENDPOINT, queryWithFormat, {
      headers,
      timeout: 120000,
    })
    return response.data.data || []
  } catch (error) {
    console.error('ClickHouse query error:', error)
    if (axios.isAxiosError(error)) {
      console.error('Response data:', error.response?.data)
      console.error('Response status:', error.response?.status)
    }
    throw error
  }
}

// Helper function to format dates for ClickHouse
const formatDate = (date: Date) => {
  const year = date.getUTCFullYear()
  const month = String(date.getUTCMonth() + 1).padStart(2, '0')
  const day = String(date.getUTCDate()).padStart(2, '0')
  const hours = String(date.getUTCHours()).padStart(2, '0')
  const minutes = String(date.getUTCMinutes()).padStart(2, '0')
  const seconds = String(date.getUTCSeconds()).padStart(2, '0')
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`
}

// ── Label/annotation JSON access ───────────────────────────────────────────────
// The `object` JSON column stores labels at object.metadata.labels and annotations at
// object.metadata.annotations. Per the ClickHouse cookbook (Rule 8): with
// json_type_escape_dots_in_keys = 1, dotted keys are stored with each '.' replaced by
// '%2E', so a key must be percent-encoded and backtick-quoted; slashes stay as-is. The
// value is then cast with ::String OUTSIDE any aggregate (cookbook Rule 1). This is the
// SINGLE place that encodes the JSON path syntax.
const escapeSqlString = (s: string) => s.replace(/'/g, "''")

// Native JSON path to a label/annotation key, WITHOUT a cast. Callers append ::String
// (outside argMax) themselves. field must be 'label' or 'annotation'.
const jsonAttrPath = (field: 'label' | 'annotation', key: string): string => {
  const container = field === 'label' ? 'labels' : 'annotations'
  // Strip backticks defensively (k8s keys never contain them), then percent-encode dots.
  const encodedKey = key.replace(/`/g, '').replace(/\./g, '%2E')
  return `object.metadata.${container}.\`${encodedKey}\``
}

// Build WHERE conditions for label/annotation equality filters (used when drilling into
// a card whose path includes a label/annotation level).
const buildAttrConditions = (labels?: AttrFilter[], annotations?: AttrFilter[]): string[] => {
  const conditions: string[] = []
  labels?.forEach(({ key, value }) => {
    if (!key) return
    conditions.push(`${jsonAttrPath('label', key)}::String = '${escapeSqlString(value)}'`)
  })
  annotations?.forEach(({ key, value }) => {
    if (!key) return
    conditions.push(`${jsonAttrPath('annotation', key)}::String = '${escapeSqlString(value)}'`)
  })
  return conditions
}

// Build a namespace WHERE/PREWHERE condition. Selecting real namespaces is strict —
// cluster-scoped objects (empty namespace) are NOT included. The 'cluster-level'
// sentinel (the overview's cluster-level card) maps to namespace = '' explicitly.
const buildNamespaceCondition = (namespaces: string[]): string | null => {
  const real = namespaces
    .filter(n => n !== 'cluster-level')
    .map(n => `'${n.replace(/'/g, "''")}'`)
  const includeClusterLevel = namespaces.includes('cluster-level')
  const parts: string[] = []
  if (real.length > 0) parts.push(`namespace IN (${real.join(',')})`)
  if (includeClusterLevel) parts.push(`namespace = ''`)
  if (parts.length === 0) return null
  return parts.length > 1 ? `(${parts.join(' OR ')})` : parts[0]
}

export async function fetchObjectsInTimeRange(
  _start: Date,
  end: Date,
  filters?: {
    clusters?: string[]
    kinds?: string[]
    namespaces?: string[]
    nameSearch?: string
    labels?: AttrFilter[]
    annotations?: AttrFilter[]
  }
): Promise<KubernetesObject[]> {
  // Latest state per uid, via LIMIT 1 BY uid (streaming merge-sort, so it stays
  // memory-safe on a distributed cluster).
  //
  // The lookback is bounded because the table TTL is 90 days and scanning all of it
  // OOMs. 7 days is enough in practice: live Kubernetes objects receive heartbeat and
  // status updates regularly, so anything still alive has an event inside the window.
  // The tradeoff is that an object untouched for longer than the window will not appear.
  // 7 days is 168 hourly partitions against 2160 for 90, roughly 13x less data scanned.
  //
  // Measured from `end` rather than now() so historical queries work the same way.
  const LOOKBACK_DAYS = 7
  const lookbackStart = new Date(end.getTime() - LOOKBACK_DAYS * 24 * 60 * 60 * 1000)

  // Build cluster/kind/namespace filter conditions (applied to both CTEs)
  const filterConditions: string[] = []

  if (filters?.clusters && filters.clusters.length > 0) {
    const clusterList = filters.clusters.map(c => `'${c.replace(/'/g, "''")}'`).join(',')
    filterConditions.push(`cluster_name IN (${clusterList})`)
  }

  if (filters?.kinds && filters.kinds.length > 0) {
    const kindList = filters.kinds.map(k => `'${k.replace(/'/g, "''")}'`).join(',')
    filterConditions.push(`kind IN (${kindList})`)
  }

  if (filters?.namespaces && filters.namespaces.length > 0) {
    const cond = buildNamespaceCondition(filters.namespaces)
    if (cond) filterConditions.push(cond)
  }

  if (filters?.nameSearch && filters.nameSearch.length > 0) {
    const escapedName = filters.nameSearch.replace(/\\/g, '\\\\').replace(/'/g, "''").replace(/%/g, '\\%').replace(/_/g, '\\_')
    filterConditions.push(`object.metadata.name::String ILIKE '%${escapedName}%'`)
  }

  filterConditions.push(...buildAttrConditions(filters?.labels, filters?.annotations))

  const filterClause = filterConditions.length > 0
    ? `AND ${filterConditions.join(' AND ')}`
    : ''

  const query = `
    -- Latest state per uid within the lookback window. The LOD system guarantees
    -- filterClause is always populated (detail mode only fires after the user zooms into
    -- a specific cluster/kind/namespace), so the lookback bound plus those filters keeps
    -- the per-shard data small enough to avoid OOM.
    -- SEEDED rows are the agent's per-kind seed markers, not objects: they carry an empty
    -- uid, so without excluding them LIMIT 1 BY uid yields one nameless phantom node.
    SELECT uid, max_timestamp, cluster_name, namespace, name, kind, operation, api_version, generation
    FROM (
      SELECT
        uid,
        timestamp AS max_timestamp,
        cluster_name,
        namespace,
        object.metadata.name::String AS name,
        kind,
        operation,
        object.apiVersion::String AS api_version,
        object.metadata.generation::Int64 AS generation
      FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
      WHERE timestamp >= '${formatDate(lookbackStart)}'
        AND timestamp <= '${formatDate(end)}'
        AND operation != 'SEEDED'
        ${filterClause}
      ORDER BY (cluster_name, kind, namespace, timestamp) DESC
      LIMIT 1 BY uid
    )
    WHERE operation != 'DELETED'
    ORDER BY name
    LIMIT 250001
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  return results.map(row => ({
    timestamp: row.max_timestamp,
    cluster_name: row.cluster_name,
    namespace: row.namespace,
    name: row.name,
    kind: row.kind,
    api_version: row.api_version || '',
    uid: row.uid,
    resource_version: row.resource_version || '',
    generation: row.generation || 0,
    operation: row.operation,
    object: {}, // Not fetched in lightweight query - use fetchObjectDetails() for full spec
    labels: {}, // Not fetched in lightweight query - use fetchObjectDetails() for full data
    annotations: {}, // Not fetched in lightweight query - use fetchObjectDetails() for full data
    owner_references: [] // Not fetched in lightweight query - use fetchObjectDetails() for full data
  }))
}

export const ALL_EVENTS_LIMIT = 2_000_000

export async function fetchAllEventsInTimeRange(
  start: Date,
  end: Date,
  filters?: {
    clusters?: string[]
    kinds?: string[]
    namespaces?: string[]
    nameSearch?: string
  }
): Promise<RawEvent[]> {
  // Raw event stream — no dedup, no operation filter, bounded to user's selected timeframe.
  // Used by the "accurate timeline" toggle to drive the histogram with real event counts
  // instead of one-latest-per-uid (which clumps artificially at the range end).
  const filterConditions: string[] = []

  if (filters?.clusters && filters.clusters.length > 0) {
    const clusterList = filters.clusters.map(c => `'${c.replace(/'/g, "''")}'`).join(',')
    filterConditions.push(`cluster_name IN (${clusterList})`)
  }

  if (filters?.kinds && filters.kinds.length > 0) {
    const kindList = filters.kinds.map(k => `'${k.replace(/'/g, "''")}'`).join(',')
    filterConditions.push(`kind IN (${kindList})`)
  }

  if (filters?.namespaces && filters.namespaces.length > 0) {
    const cond = buildNamespaceCondition(filters.namespaces)
    if (cond) filterConditions.push(cond)
  }

  if (filters?.nameSearch && filters.nameSearch.length > 0) {
    const escapedName = filters.nameSearch.replace(/\\/g, '\\\\').replace(/'/g, "''").replace(/%/g, '\\%').replace(/_/g, '\\_')
    filterConditions.push(`object.metadata.name::String ILIKE '%${escapedName}%'`)
  }

  const filterClause = filterConditions.length > 0
    ? `AND ${filterConditions.join(' AND ')}`
    : ''

  const query = `
    SELECT timestamp, uid, operation
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    WHERE timestamp >= '${formatDate(start)}'
      AND timestamp <= '${formatDate(end)}'
      ${filterClause}
    ORDER BY timestamp ASC
    LIMIT ${ALL_EVENTS_LIMIT}
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  return results.map(row => ({
    timestamp: row.timestamp,
    uid: row.uid,
    operation: row.operation,
  }))
}


export async function fetchAggregateCounts(
  _start: Date,
  end: Date,
  filters?: {
    clusters?: string[]
    kinds?: string[]
    namespaces?: string[]
    nameSearch?: string
  },
  // Label/annotation grouping levels to additionally split counts by. Each becomes an
  // extra GROUP BY dimension pulled from the object JSON. cluster/namespace/kind levels
  // are ignored here (they're already first-class columns).
  groupDims?: GroupByConfig[]
): Promise<AggregateCount[]> {
  // Aggregate query: returns one row per (cluster_name, kind, namespace [, ...label/annotation
  // dims]) with live object counts.
  // Uses argMax(operation, timestamp) — the canonical ClickHouse "latest state per key" pattern:
  //   1. Scan the last 7 days (168 hourly partitions — same tradeoff as fetchObjectsInTimeRange)
  //   2. argMax picks each uid's operation at its maximum timestamp (single-pass hash aggregation)
  //   3. Outer WHERE filters to only live objects (latest op != DELETED)
  // Correctly counts stable objects with no recent events, unlike a flat window scan.
  const LOOKBACK_DAYS = 7
  const lookbackStart = new Date(end.getTime() - LOOKBACK_DAYS * 24 * 60 * 60 * 1000)

  // Resolve label/annotation grouping dimensions to (alias, dimKey, expr). A uid's label
  // value can change over time, so argMax(value, timestamp) takes it at the latest event —
  // consistent with how latest_op is chosen.
  const dims = (groupDims || [])
    .filter((l): l is GroupByConfig & { field: 'label' | 'annotation'; value: string } =>
      (l.field === 'label' || l.field === 'annotation') && !!l.value)
    .map((l, i) => ({
      alias: `dim_${i}`,
      dimKey: groupDimKey(l)!,
      path: jsonAttrPath(l.field, l.value),
    }))

  const filterConditions: string[] = []

  if (filters?.clusters && filters.clusters.length > 0) {
    const clusterList = filters.clusters.map(c => `'${c.replace(/'/g, "''")}'`).join(',')
    filterConditions.push(`cluster_name IN (${clusterList})`)
  }

  if (filters?.kinds && filters.kinds.length > 0) {
    const kindList = filters.kinds.map(k => `'${k.replace(/'/g, "''")}'`).join(',')
    filterConditions.push(`kind IN (${kindList})`)
  }

  if (filters?.namespaces && filters.namespaces.length > 0) {
    const cond = buildNamespaceCondition(filters.namespaces)
    if (cond) filterConditions.push(cond)
  }

  if (filters?.nameSearch && filters.nameSearch.length > 0) {
    const escapedName = filters.nameSearch.replace(/\\/g, '\\\\').replace(/'/g, "''").replace(/%/g, '\\%').replace(/_/g, '\\_')
    filterConditions.push(`object.metadata.name::String ILIKE '%${escapedName}%'`)
  }


  const filterClause = filterConditions.length > 0
    ? `AND ${filterConditions.join(' AND ')}`
    : ''

  // Dynamic SQL fragments for the label/annotation dimensions. Cast ::String outside argMax
  // (cookbook Rule 1) — the value is taken at the uid's latest event, like latest_op.
  const innerDimSelect = dims.map(d => `,\n        argMax(${d.path}, timestamp)::String AS ${d.alias}`).join('')
  const outerDimSelect = dims.map(d => `,\n      ${d.alias}`).join('')
  const dimGroupBy = dims.map(d => `, ${d.alias}`).join('')

  const query = `
    SELECT
      cluster_name,
      kind,
      namespace,
      count() AS total,
      countIf(latest_op = 'MODIFIED') AS modified_count${outerDimSelect}
    FROM (
      SELECT
        cluster_name,
        kind,
        namespace,
        uid,
        argMax(operation, timestamp) AS latest_op${innerDimSelect}
      FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
      WHERE timestamp >= '${formatDate(lookbackStart)}'
        AND timestamp <= '${formatDate(end)}'
        ${filterClause}
      GROUP BY cluster_name, kind, namespace, uid
    )
    WHERE latest_op != 'DELETED'
    GROUP BY cluster_name, kind, namespace${dimGroupBy}
    ORDER BY cluster_name, kind, namespace${dimGroupBy}
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  return results.map(row => {
    const out: AggregateCount = {
      cluster_name: row.cluster_name,
      namespace: row.namespace,
      kind: row.kind,
      total: Number(row.total),
      modified_count: Number(row.modified_count),
    }
    if (dims.length > 0) {
      out.dims = {}
      dims.forEach(d => { out.dims![d.dimKey] = row[d.alias] ?? '' })
    }
    return out
  })
}

// Shared filter-clause builder for the activity queries (cluster/kind/namespace/name).
// Returns a string beginning with "AND ..." or "" when no filters are set.
function buildActivityFilterClause(filters?: {
  clusters?: string[]
  kinds?: string[]
  namespaces?: string[]
  nameSearch?: string
}): string {
  const conditions: string[] = []

  if (filters?.clusters && filters.clusters.length > 0) {
    const clusterList = filters.clusters.map(c => `'${c.replace(/'/g, "''")}'`).join(',')
    conditions.push(`cluster_name IN (${clusterList})`)
  }
  if (filters?.kinds && filters.kinds.length > 0) {
    const kindList = filters.kinds.map(k => `'${k.replace(/'/g, "''")}'`).join(',')
    conditions.push(`kind IN (${kindList})`)
  }
  if (filters?.namespaces && filters.namespaces.length > 0) {
    const cond = buildNamespaceCondition(filters.namespaces)
    if (cond) conditions.push(cond)
  }
  if (filters?.nameSearch && filters.nameSearch.length > 0) {
    const escapedName = filters.nameSearch.replace(/\\/g, '\\\\').replace(/'/g, "''").replace(/%/g, '\\%').replace(/_/g, '\\_')
    conditions.push(`object.metadata.name::String ILIKE '%${escapedName}%'`)
  }

  return conditions.length > 0 ? `AND ${conditions.join(' AND ')}` : ''
}

export async function fetchHistogramBins(
  start: Date,
  end: Date,
  filters?: {
    clusters?: string[]
    kinds?: string[]
    namespaces?: string[]
    nameSearch?: string
  }
): Promise<ActivityBin[]> {
  // Histogram source: one row per time-bin (summed across all groups). Bounded to
  // ACTIVITY_BIN_COUNT rows regardless of cluster/namespace/kind cardinality, so the
  // payload is a few KB even on huge clusters — cheap enough to always load.
  //
  // The bin index mirrors the UI's binning: floor((ts - start) / binSize), anchored to `start`
  // (not the Unix epoch) so the bins line up exactly with the 100-bin timeline layout.
  const startMs = start.getTime()
  // Integer divisor for intDiv. Sub-millisecond drift over the range is negligible for a histogram.
  const binSizeMs = Math.max(1, Math.floor((end.getTime() - startMs) / ACTIVITY_BIN_COUNT))
  const filterClause = buildActivityFilterClause(filters)

  const query = `
    SELECT
      intDiv(toUnixTimestamp64Milli(timestamp) - ${startMs}, ${binSizeMs}) AS bin_index,
      countIf(operation = 'ADDED')    AS added,
      countIf(operation = 'MODIFIED') AS modified,
      countIf(operation = 'DELETED')  AS deleted
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    WHERE timestamp >= '${formatDate(start)}'
      AND timestamp <= '${formatDate(end)}'
      ${filterClause}
    GROUP BY bin_index
    ORDER BY bin_index
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  return results.map(row => ({
    // Clamp the t===end edge (bin_index can equal ACTIVITY_BIN_COUNT) into the last bin.
    bin_index: Math.min(Number(row.bin_index), ACTIVITY_BIN_COUNT - 1),
    added: Number(row.added),
    modified: Number(row.modified),
    deleted: Number(row.deleted),
  }))
}

export async function fetchActivityWindow(
  windowStart: Date,
  windowEnd: Date,
  filters?: {
    clusters?: string[]
    kinds?: string[]
    namespaces?: string[]
    nameSearch?: string
  }
): Promise<ActivityGroupCount[]> {
  // Outline source: per-(cluster, namespace, kind) event counts for a SINGLE time window
  // (the bin under the playhead). No bin dimension and no 100× multiplier, so the result is
  // one row per active group — small even on busy clusters. Fetched on demand as the playhead
  // moves (debounced) rather than shipping every bin × every group up front.
  const filterClause = buildActivityFilterClause(filters)

  const query = `
    SELECT
      cluster_name,
      namespace,
      kind,
      countIf(operation = 'ADDED')    AS added,
      countIf(operation = 'MODIFIED') AS modified,
      countIf(operation = 'DELETED')  AS deleted
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    WHERE timestamp > '${formatDate(windowStart)}'
      AND timestamp <= '${formatDate(windowEnd)}'
      ${filterClause}
    GROUP BY cluster_name, namespace, kind
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  return results.map(row => ({
    cluster_name: row.cluster_name,
    namespace: row.namespace,
    kind: row.kind,
    added: Number(row.added),
    modified: Number(row.modified),
    deleted: Number(row.deleted),
  }))
}

export async function fetchClusters(): Promise<string[]> {
  // Bounded to 7 days so we only surface dimensions that are still live,
  // matching the lookback used by the current-state queries.
  const query = `
    SELECT DISTINCT cluster_name
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    WHERE timestamp >= now() - INTERVAL 7 DAY
    ORDER BY cluster_name
  `
  
  const results = await executeClickHouseQuery(query)
  return results.map(row => row.cluster_name)
}

export async function fetchKinds(): Promise<string[]> {
  // Bounded to 7 days so we only surface dimensions that are still live,
  // matching the lookback used by the current-state queries.
  const query = `
    SELECT DISTINCT kind
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    WHERE kind != ''
      AND timestamp >= now() - INTERVAL 7 DAY
    ORDER BY kind
  `
  
  const results = await executeClickHouseQuery(query)
  return results.map(row => row.kind)
}

export async function fetchNamespaces(): Promise<string[]> {
  // Bounded to 7 days so we only surface dimensions that are still live,
  // matching the lookback used by the current-state queries.
  const query = `
    SELECT DISTINCT namespace
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    WHERE namespace IS NOT NULL
      AND namespace != ''
      AND timestamp >= now() - INTERVAL 7 DAY
    ORDER BY namespace
  `
  
  const results = await executeClickHouseQuery(query)
  return results.map(row => row.namespace)
}

export async function fetchObjectHistory(
  uid: string,
  clusterName: string,
  namespace: string,
  kind: string
): Promise<KubernetesObject[]> {
  // Simple PREWHERE scan — no CTE/GROUP BY needed.
  // cluster_name + kind + namespace hit the sort key prefix → ClickHouse skips irrelevant granules.
  // uid is not in the sort key so it's evaluated after PREWHERE reduces the row set.
  // Raw events are returned (may contain duplicate resource_versions); deduplication happens in JS.
  console.log('📊 fetchObjectHistory called with UID:', uid)

  // LIMIT 1 BY resource_version deduplicates in ClickHouse — one row per unique resource_version.
  // This avoids returning thousands of identical snapshots for high-churn objects like Pods
  // (which generate a new event per status change) while preserving the full history.
  // The outer ORDER BY re-sorts after deduplication so the result is chronological.
  const query = `
    SELECT *
    FROM (
      SELECT
        timestamp,
        cluster_name,
        namespace,
        object.metadata.name::String AS name,
        kind,
        object.apiVersion::String AS api_version,
        uid,
        object.metadata.resourceVersion::String AS resource_version,
        object.metadata.generation::Int64 AS generation,
        operation,
        object
      FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
      PREWHERE cluster_name = '${clusterName}'
        AND kind = '${kind}'
        AND namespace = '${namespace}'
      WHERE uid = '${uid}'
      ORDER BY resource_version, timestamp ASC
      LIMIT 1 BY resource_version
    )
    ORDER BY timestamp ASC
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  console.log('📊 Executing query for object history')
  const results = await executeClickHouseQuery(query)
  console.log('📊 fetchObjectHistory results:', results.length, 'versions')

  return results.map(row => ({
    timestamp: row.timestamp,
    cluster_name: row.cluster_name,
    namespace: row.namespace,
    name: row.name,
    kind: row.kind,
    api_version: row.api_version,
    uid: row.uid,
    resource_version: row.resource_version,
    generation: row.generation,
    operation: row.operation,
    object: row.object || {},
    labels: {},
    annotations: {},
    owner_references: []
  }))
}

/**
 * Fetch version list metadata only — no object column.
 * Returns in ~0.2s regardless of history length because the large object column is not read.
 * Use fetchObjectAtVersion to load the full spec for a specific version on demand.
 */
export async function fetchObjectVersionList(
  uid: string,
  clusterName: string,
  namespace: string,
  kind: string
): Promise<KubernetesObject[]> {
  const query = `
    SELECT *
    FROM (
      SELECT
        timestamp,
        cluster_name,
        namespace,
        object.metadata.name::String AS name,
        kind,
        object.apiVersion::String AS api_version,
        uid,
        object.metadata.resourceVersion::String AS resource_version,
        object.metadata.generation::Int64 AS generation,
        operation
      FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
      PREWHERE cluster_name = '${clusterName}'
        AND kind = '${kind}'
        AND namespace = '${namespace}'
      WHERE uid = '${uid}'
      ORDER BY resource_version, timestamp ASC
      LIMIT 1 BY resource_version
    )
    ORDER BY timestamp ASC
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  return results.map(row => ({
    timestamp: row.timestamp,
    cluster_name: row.cluster_name,
    namespace: row.namespace,
    name: row.name,
    kind: row.kind,
    api_version: row.api_version,
    uid: row.uid,
    resource_version: row.resource_version,
    generation: row.generation,
    operation: row.operation,
    object: {},
    labels: {},
    annotations: {},
    owner_references: []
  }))
}

/**
 * Fetch the full object spec for a single specific version by uid + exact timestamp.
 * The exact timestamp match enables partition pruning (PARTITION BY toStartOfHour(timestamp))
 * → narrows to a single hourly partition before evaluating uid. Typically sub-second.
 * timestamp should be the raw string returned by ClickHouse (e.g. "2024-01-15 10:23:45.123456").
 */
export async function fetchObjectAtVersion(
  uid: string,
  clusterName: string,
  namespace: string,
  kind: string,
  timestamp: string
): Promise<KubernetesObject | null> {
  const query = `
    SELECT
      timestamp,
      cluster_name,
      namespace,
      object.metadata.name::String AS name,
      kind,
      object.apiVersion::String AS api_version,
      uid,
      object.metadata.resourceVersion::String AS resource_version,
      object.metadata.generation::Int64 AS generation,
      operation,
      object
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    PREWHERE cluster_name = '${clusterName}'
      AND kind = '${kind}'
      AND namespace = '${namespace}'
      AND timestamp = '${timestamp}'
    WHERE uid = '${uid}'
    LIMIT 1
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  if (results.length === 0) return null

  const row = results[0]
  return {
    timestamp: row.timestamp,
    cluster_name: row.cluster_name,
    namespace: row.namespace,
    name: row.name,
    kind: row.kind,
    api_version: row.api_version,
    uid: row.uid,
    resource_version: row.resource_version,
    generation: row.generation,
    operation: row.operation,
    object: row.object || {},
    labels: {},
    annotations: {},
    owner_references: []
  }
}

export async function fetchObjectVersions(
  cluster: string,
  namespace: string,
  kind: string,
  name: string,
  timeRange: TimeRange
): Promise<KubernetesObject[]> {
  // NOTE: This query searches by name, which may return multiple distinct objects
  // if the name was reused over time (each with different UIDs)
  // Deduplicates by uid and resource_version to exclude duplicate events with no actual changes
  const query = `
    WITH deduplicated AS (
      SELECT 
        uid,
        object.metadata.resourceVersion::String as resource_version,
        max(timestamp) as max_timestamp,
        any(cluster_name) as r_cluster_name,
        any(namespace) as r_namespace,
        argMax(object.metadata.name::String, timestamp) as r_name,
        any(kind) as r_kind,
        argMax(object.apiVersion::String, timestamp) as r_api_version,
        argMax(object.metadata.generation::Int64, timestamp) as r_generation,
        argMax(operation, timestamp) as r_operation,
        argMax(object, timestamp) as r_object
      FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
      WHERE cluster_name = '${cluster}'
        AND namespace = '${namespace}'
        AND kind = '${kind}'
        AND object.metadata.name::String = '${name}'
        AND timestamp >= '${formatDate(timeRange.start)}'
        AND timestamp <= '${formatDate(timeRange.end)}'
      GROUP BY uid, resource_version
    )
    SELECT
      max_timestamp as timestamp,
      r_cluster_name as cluster_name,
      r_namespace as namespace,
      r_name as name,
      r_kind as kind,
      r_api_version as api_version,
      uid,
      resource_version,
      r_generation as generation,
      r_operation as operation,
      r_object as object
    FROM deduplicated
    ORDER BY max_timestamp ASC
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  return results.map(row => ({
    timestamp: row.timestamp,
    cluster_name: row.cluster_name,
    namespace: row.namespace,
    name: row.name,
    kind: row.kind,
    api_version: row.api_version,
    uid: row.uid,
    resource_version: row.resource_version,
    generation: row.generation,
    operation: row.operation,
    object: row.object || {},
    labels: {},
    annotations: {},
    owner_references: []
  }))
}

// Helper function to fetch the latest UID for a given object name
// Useful when you have a name but need to find the current UID
export async function fetchLatestObjectUID(
  cluster: string,
  namespace: string,
  kind: string,
  name: string
): Promise<string | null> {
  const query = `
    SELECT uid
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    WHERE cluster_name = '${cluster}'
      AND namespace = '${namespace}'
      AND kind = '${kind}'
      AND object.metadata.name::String = '${name}'
      AND operation != 'DELETED'
      AND operation != 'SEEDED'
      AND timestamp >= now() - INTERVAL 7 DAY
    ORDER BY timestamp DESC
    LIMIT 1
  `
  
  const results = await executeClickHouseQuery(query)
  return results.length > 0 ? results[0].uid : null
}

/**
 * Search for objects by name substring across the cluster ecosystem.
 * Uses ILIKE for case-insensitive matching. Requires ≥2 characters.
 * If filters are provided they narrow the scan via PREWHERE (sort-key prefix), keeping it fast.
 * Returns lightweight rows (no object column), capped at 50 results.
 */
export async function fetchSearchObjects(
  term: string,
  filters?: { clusters?: string[]; kinds?: string[]; namespaces?: string[] },
  offset = 0
): Promise<KubernetesObject[]> {
  // Escape to prevent SQL injection; ILIKE wildcards (% _) in the term are treated literally
  const escaped = term.replace(/\\/g, '\\\\').replace(/'/g, "''").replace(/%/g, '\\%').replace(/_/g, '\\_')

  const prewhereConditions: string[] = []

  if (filters?.clusters && filters.clusters.length > 0) {
    const list = filters.clusters.map(c => `'${c.replace(/'/g, "''")}'`).join(',')
    prewhereConditions.push(`cluster_name IN (${list})`)
  }
  if (filters?.kinds && filters.kinds.length > 0) {
    const list = filters.kinds.map(k => `'${k.replace(/'/g, "''")}'`).join(',')
    prewhereConditions.push(`kind IN (${list})`)
  }
  if (filters?.namespaces && filters.namespaces.length > 0) {
    const cond = buildNamespaceCondition(filters.namespaces)
    if (cond) prewhereConditions.push(cond)
  }

  const prewhereClause = prewhereConditions.length > 0
    ? `PREWHERE ${prewhereConditions.join(' AND ')}`
    : ''

  const query = `
    SELECT
      uid,
      timestamp,
      cluster_name,
      namespace,
      kind,
      object.metadata.name::String AS name,
      object.apiVersion::String AS api_version
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    ${prewhereClause}
    WHERE operation != 'DELETED'
      AND operation != 'SEEDED'
      AND timestamp >= now() - INTERVAL 7 DAY
      AND object.metadata.name::String ILIKE '%${escaped}%'
    ORDER BY cluster_name, namespace, name, timestamp DESC
    LIMIT 1 BY uid
    LIMIT 50 OFFSET ${offset}
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  return results.map(row => ({
    timestamp: row.timestamp,
    cluster_name: row.cluster_name,
    namespace: row.namespace,
    name: row.name,
    kind: row.kind,
    api_version: row.api_version || '',
    uid: row.uid,
    resource_version: '',
    generation: 0,
    operation: 'ADDED' as const,
    object: {},
    labels: {},
    annotations: {},
    owner_references: []
  }))
}

/**
 * Fetch full object details for a specific object by UID
 * This includes the complete Kubernetes object spec which is excluded from the lightweight queries
 * Use this when user clicks on an object to view details
 * cluster_name + kind + namespace match the sort key prefix → index-only scan instead of full table scan
 */
export async function fetchObjectDetails(
  uid: string,
  clusterName: string,
  namespace: string,
  kind: string
): Promise<KubernetesObject | null> {
  const query = `
    SELECT
      timestamp,
      cluster_name,
      namespace,
      object.metadata.name::String as name,
      kind,
      object.apiVersion::String as api_version,
      uid,
      object.metadata.resourceVersion::String as resource_version,
      object.metadata.generation::Int64 as generation,
      operation,
      object
    FROM ${CLICKHOUSE_DATABASE}.${CLICKHOUSE_TABLE}
    WHERE cluster_name = '${clusterName}'
      AND kind = '${kind}'
      AND namespace = '${namespace}'
      AND uid = '${uid}'
      AND timestamp >= now() - INTERVAL 7 DAY
    ORDER BY timestamp DESC
    LIMIT 1
    SETTINGS json_type_escape_dots_in_keys = 1
  `

  const results = await executeClickHouseQuery(query)
  if (results.length === 0) return null

  const row = results[0]

  return {
    timestamp: row.timestamp,
    cluster_name: row.cluster_name,
    namespace: row.namespace,
    name: row.name,
    kind: row.kind,
    api_version: row.api_version,
    uid: row.uid,
    resource_version: row.resource_version,
    generation: row.generation,
    operation: row.operation,
    object: row.object || {},
    labels: {},
    annotations: {},
    owner_references: []
  }
}
