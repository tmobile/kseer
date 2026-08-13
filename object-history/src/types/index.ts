export interface KubernetesObject {
  timestamp: string
  cluster_name: string
  namespace: string
  name: string
  kind: string
  api_version: string
  uid: string
  resource_version: string
  generation: number
  operation: 'ADDED' | 'MODIFIED' | 'DELETED'
  object: any
  labels: Record<string, string>
  annotations: Record<string, string>
  owner_references: OwnerReference[]
  creation_timestamp?: string
  deletion_timestamp?: string
}

export interface OwnerReference {
  apiVersion: string
  kind: string
  name: string
  uid: string
}

export interface TimeRange {
  start: Date
  end: Date
}

export interface RawEvent {
  timestamp: string
  uid: string
  operation: 'ADDED' | 'MODIFIED' | 'DELETED'
}

export interface GraphNode {
  id: string
  label: string
  kind: string
  namespace: string
  cluster: string
  status: 'active' | 'deleted' | 'modified'
  timestamp: Date
  data: KubernetesObject
}

export interface GraphLink {
  source: string
  target: string
  type: 'owner' | 'service' | 'configmap' | 'secret' | 'pvc'
  label?: string
}

export interface GraphData {
  nodes: GraphNode[]
  links: GraphLink[]
}

export interface AggregateCount {
  cluster_name: string
  namespace: string
  kind: string
  total: number
  modified_count: number
  /**
   * Values for any label/annotation grouping dimensions, keyed by groupDimKey()
   * (e.g. "label:app" -> "frontend"). Only populated for the label/annotation
   * levels present in the active grouping config; absent otherwise.
   */
  dims?: Record<string, string>
}

/**
 * Per-time-bin event counts (summed across all groups), split by operation.
 * Drives the activity histogram. Bounded to ACTIVITY_BIN_COUNT rows regardless of
 * cluster/namespace/kind cardinality, so it's tiny and cheap to always load.
 */
export interface ActivityBin {
  bin_index: number
  added: number
  modified: number
  deleted: number
}

/**
 * Per-(cluster, namespace, kind) event counts for a single time window, split by operation.
 * Drives the overview change-outlines for the slice under the playhead. Fetched on demand
 * for one window (not all bins), so it stays small even on busy clusters.
 */
export interface ActivityGroupCount {
  cluster_name: string
  namespace: string
  kind: string
  added: number
  modified: number
  deleted: number
}

/** A single label/annotation key=value filter used when drilling into a card. */
export interface AttrFilter {
  key: string
  value: string
}

/**
 * The set of filters a clicked overview card expands to. Cluster/namespace/kind are
 * first-class columns; labels/annotations carry the key+value pulled from the grouping
 * config and the card's path.
 */
export interface CardClickFilters {
  clusters: string[]
  namespaces: string[]
  kinds: string[]
  labels: AttrFilter[]
  annotations: AttrFilter[]
}

/** Number of histogram/activity bins the selected time range is divided into. */
export const ACTIVITY_BIN_COUNT = 100

export type GroupByField = 'cluster' | 'namespace' | 'kind' | 'label' | 'annotation'

export interface GroupByConfig {
  field: GroupByField
  value?: string // For label/annotation, specifies which label/annotation key
  label?: string // Display label for this grouping level
}

export interface GroupingConfiguration {
  levels: GroupByConfig[]
}

/**
 * Stable key for a label/annotation grouping dimension, used to index AggregateCount.dims.
 * Returns null for cluster/namespace/kind (those are first-class columns, not dims) or for
 * label/annotation levels missing a key.
 */
export function groupDimKey(level: GroupByConfig): string | null {
  if ((level.field === 'label' || level.field === 'annotation') && level.value) {
    return `${level.field}:${level.value}`
  }
  return null
}
