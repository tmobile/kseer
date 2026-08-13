import { useEffect, useRef, useState, useMemo, useCallback } from 'react'
import * as d3 from 'd3'
import { Box, Paper, Typography, Chip, IconButton, Tooltip, CircularProgress } from '@mui/material'
import { ZoomIn, ZoomOut, CenterFocusStrong, FitScreen } from '@mui/icons-material'
import { KubernetesObject, GraphNode, GraphData, GroupingConfiguration, GroupByConfig, GroupByField, TimeRange, AggregateCount, ActivityGroupCount, CardClickFilters, groupDimKey } from '../types'
import ObjectDetailsModal from './ObjectDetailsModal'
import { getAnimationStyles, getAnimationColor } from '../hooks/useObjectAnimations'
import type { ObjectAnimation } from '../hooks/useObjectAnimations'

interface GraphVisualizationProps {
  objects: KubernetesObject[]
  currentTime: Date
  groupingConfiguration?: GroupingConfiguration
  timeRange: TimeRange
  liveMode?: boolean
  animations?: {
    activeAnimations: ObjectAnimation[]
    getAnimationForObject: (uid: string) => ObjectAnimation | null
    isAnimating: (uid: string) => boolean
  }
  /** Pre-aggregated counts for overview mode — when provided, overview cards use this data */
  overviewData?: AggregateCount[]
  /** Nodes for each expanded card (groupId → GraphNode[]); managed by App.tsx */
  expandedCardNodes?: Map<string, GraphNode[]>
  /** Set of card groupIds currently being loaded */
  loadingCardIds?: Set<string>
  /** Called when user clicks a card — App fetches and expands it; called again to collapse */
  onCardClick?: (groupId: string, groups: CardClickFilters) => void
  /** When true, render individual object nodes regardless of zoom level (e.g. after nameSearch "Show all") */
  forceDetail?: boolean
  /** Uids of objects whose events fired within one histogram-bin of currentTime — pulsed to show activity */
  eventHighlightUids?: Set<string>
  /** Per-group activity for the window under the playhead — drives the change-outlines on overview cards/clusters */
  activityWindow?: ActivityGroupCount[]
}

// Predefined colors for common Kubernetes object types
const predefinedKindColors: Record<string, string> = {
  Pod: '#4CAF50',
  Service: '#2196F3',
  Deployment: '#FF9800',
  ReplicaSet: '#FF5722',
  ConfigMap: '#9C27B0',
  Secret: '#E91E63',
  Namespace: '#00BCD4',
  Node: '#795548',
  PersistentVolumeClaim: '#607D8B',
  PersistentVolume: '#455A64',
  StatefulSet: '#FFC107',
  DaemonSet: '#8BC34A',
  Job: '#03A9F4',
  CronJob: '#00ACC1',
  Ingress: '#7E57C2',
  NetworkPolicy: '#5E35B1',
}

// Generate a large palette of maximally distinct colors
// Using systematic HSL generation with careful spacing
function generateDistinctColorPalette(count: number): string[] {
  const colors: string[] = []
  const saturationLevels = [0.7, 0.85, 0.6] // High, very high, medium
  const lightnessLevels = [0.5, 0.6, 0.45, 0.65] // Different brightness levels
  
  // Calculate hue step to distribute colors evenly across spectrum
  const hueStep = 360 / Math.ceil(count / (saturationLevels.length * lightnessLevels.length))
  
  let colorIndex = 0
  for (let hue = 0; hue < 360 && colorIndex < count; hue += hueStep) {
    for (const saturation of saturationLevels) {
      for (const lightness of lightnessLevels) {
        if (colorIndex >= count) break
        colors.push(hslToHex(hue, saturation * 100, lightness * 100))
        colorIndex++
      }
      if (colorIndex >= count) break
    }
  }
  
  return colors
}

// Convert HSL to HEX color
function hslToHex(h: number, s: number, l: number): string {
  s /= 100
  l /= 100
  
  const c = (1 - Math.abs(2 * l - 1)) * s
  const x = c * (1 - Math.abs((h / 60) % 2 - 1))
  const m = l - c / 2
  
  let r = 0, g = 0, b = 0
  
  if (h >= 0 && h < 60) {
    r = c; g = x; b = 0
  } else if (h >= 60 && h < 120) {
    r = x; g = c; b = 0
  } else if (h >= 120 && h < 180) {
    r = 0; g = c; b = x
  } else if (h >= 180 && h < 240) {
    r = 0; g = x; b = c
  } else if (h >= 240 && h < 300) {
    r = x; g = 0; b = c
  } else if (h >= 300 && h < 360) {
    r = c; g = 0; b = x
  }
  
  const toHex = (n: number) => {
    const hex = Math.round((n + m) * 255).toString(16)
    return hex.length === 1 ? '0' + hex : hex
  }
  
  return `#${toHex(r)}${toHex(g)}${toHex(b)}`
}

// Generate 300 distinct colors for extensive coverage
// This provides excellent collision resistance even with 150+ object types
const distinctColorPalette = generateDistinctColorPalette(300)

// FNV-1a hash function for better string distribution
// This hash is more sensitive to character position and value changes
function hashString(str: string): number {
  let hash = 2166136261 // FNV offset basis
  for (let i = 0; i < str.length; i++) {
    hash ^= str.charCodeAt(i)
    // FNV prime: multiply by 16777619
    hash += (hash << 1) + (hash << 4) + (hash << 7) + (hash << 8) + (hash << 24)
  }
  return hash >>> 0 // Convert to unsigned 32-bit integer
}

// Secondary hash for additional mixing
function secondaryHash(str: string): number {
  let hash = 5381 // djb2 hash
  for (let i = 0; i < str.length; i++) {
    hash = ((hash << 5) + hash) + str.charCodeAt(i)
  }
  return hash >>> 0
}

// Generate a visually distinct color for any object kind
function getKindColor(kind: string): string {
  // Check if we have a predefined color
  if (predefinedKindColors[kind]) {
    return predefinedKindColors[kind]
  }
  
  // Use dual hash functions for maximum distribution
  const hash1 = hashString(kind)
  const hash2 = secondaryHash(kind)
  
  // Combine both hashes with different prime multipliers
  // This ensures even very similar strings get different indices
  const combined = (hash1 * 37 + hash2 * 97) >>> 0
  const colorIndex = combined % distinctColorPalette.length
  
  return distinctColorPalette[colorIndex]
}

interface AggregateItem {
  // The value of the leaf (last) grouping level — e.g. a kind, or a namespace, etc.
  // Rendered as a bullet row inside the innermost card.
  label: string
  total: number
  modified_count: number
}

// Map an AggregateCount row to the value for a given grouping level. Mirrors the
// columns returned by fetchAggregateCounts. label/annotation values come from the row's
// `dims` map (populated when those levels are present in the grouping config); a missing
// value renders as a readable "(no <key>)" placeholder.
function aggFieldValue(level: GroupByConfig, item: AggregateCount): string {
  switch (level.field) {
    case 'cluster': return item.cluster_name || 'unknown'
    case 'namespace': return item.namespace || 'cluster-level'
    case 'kind': return item.kind || 'unknown'
    case 'label':
    case 'annotation': {
      const key = groupDimKey(level)
      const raw = key ? item.dims?.[key] : undefined
      return raw && raw.length > 0 ? raw : `(no ${level.value || level.field})`
    }
    default: return 'other'
  }
}

const emptyCardFilters = (): CardClickFilters => ({
  clusters: [], namespaces: [], kinds: [], labels: [], annotations: [],
})

// Derive the detail-query filters from a card's group id. The id is a '/'-joined path of
// the card (non-leaf) levels, so segment i maps to level i. label/annotation segments are
// emitted as {key, value} pairs (the key from the config, the value from the path); the
// "(no <key>)" placeholder is skipped since it represents "attribute absent".
function groupFiltersFromId(
  groupId: string,
  grouping: GroupingConfiguration
): CardClickFilters {
  const segments = groupId.split('/')
  const out = emptyCardFilters()
  grouping.levels.forEach((level, i) => {
    const val = segments[i]
    if (val === undefined) return
    if (level.field === 'cluster') out.clusters.push(val)
    else if (level.field === 'namespace') out.namespaces.push(val)
    else if (level.field === 'kind') out.kinds.push(val)
    else if (level.field === 'label' && level.value && val !== `(no ${level.value})`) {
      out.labels.push({ key: level.value, value: val })
    } else if (level.field === 'annotation' && level.value && val !== `(no ${level.value})`) {
      out.annotations.push({ key: level.value, value: val })
    }
  })
  return out
}

interface GroupData {
  id: string
  name: string
  value?: number
  children?: GroupData[]
  nodes?: GraphNode[]
  /** Populated in overview mode from fetchAggregateCounts — replaces nodes */
  aggregateItems?: AggregateItem[]
}

interface LayoutNode extends Omit<GroupData, 'children'> {
  x?: number
  y?: number
  width?: number
  height?: number
  children?: LayoutNode[]
}

interface NodeBounds {
  x: number
  y: number
  width: number
  height: number
  node: GraphNode
}

interface CardBounds {
  x: number
  y: number
  width: number
  height: number
  groupId: string
  groups: CardClickFilters
  isKindToggle?: boolean
}

/**
 * Build a GroupData hierarchy from AggregateCount[] for the overview mode.
 *
 * The LAST grouping level is the "leaf" dimension: its values become the bullet rows
 * (aggregateItems) inside the innermost card. Every level before it becomes a nested
 * card. So [cluster, namespace, kind] => cluster cards contain namespace cards, and each
 * namespace card lists its kinds as bullets. Rows that collapse to the same
 * (card-path, leaf-value) are summed.
 */
function buildOverviewHierarchy(
  overviewData: AggregateCount[],
  groupingConfig: GroupingConfiguration
): GroupData {
  const root: GroupData = { id: 'root', name: 'All Resources', children: [] }
  const groups = new Map<string, GroupData>()

  const levels = groupingConfig.levels
  // All-but-last levels are cards; the last level is the bullet/leaf dimension.
  // Fallback for a single-level config: treat that level as the only card and use kind as bullets.
  const cardLevels = levels.length > 1 ? levels.slice(0, -1) : levels
  const leafLevel: GroupByConfig = levels.length > 1 ? levels[levels.length - 1] : { field: 'kind' }

  // innermost-card id -> (leaf value -> merged AggregateItem)
  const leafAcc = new Map<string, Map<string, AggregateItem>>()

  overviewData.forEach(item => {
    let currentLevel = root
    let pathKey = ''

    cardLevels.forEach((level, index) => {
      const groupValue = aggFieldValue(level, item)
      pathKey = pathKey ? `${pathKey}/${groupValue}` : groupValue
      const isInnermostCard = index === cardLevels.length - 1

      if (!groups.has(pathKey)) {
        const newGroup: GroupData = {
          id: pathKey,
          name: groupValue,
          value: 0,
          children: !isInnermostCard ? [] : undefined,
          aggregateItems: isInnermostCard ? [] : undefined,
        }
        groups.set(pathKey, newGroup)
        if (!currentLevel.children) currentLevel.children = []
        currentLevel.children.push(newGroup)
      }

      currentLevel = groups.get(pathKey)!
    })

    // Accumulate the leaf-dimension value into the innermost card, summing duplicates.
    const leafValue = aggFieldValue(leafLevel, item)
    let perCard = leafAcc.get(currentLevel.id)
    if (!perCard) { perCard = new Map(); leafAcc.set(currentLevel.id, perCard) }
    const existing = perCard.get(leafValue)
    if (existing) {
      existing.total += item.total
      existing.modified_count += item.modified_count
    } else {
      perCard.set(leafValue, { label: leafValue, total: item.total, modified_count: item.modified_count })
    }
    currentLevel.value = (currentLevel.value || 0) + item.total
  })

  // Materialize the accumulated bullet rows onto each innermost card.
  leafAcc.forEach((perCard, cardId) => {
    const card = groups.get(cardId)
    if (card) card.aggregateItems = Array.from(perCard.values())
  })

  return root
}

function calcKindCardHeight(kindCount: number): number {
  return Math.max(140, 60 + kindCount * 16)
}

interface GroupActivity {
  added: number
  modified: number
  deleted: number
  total: number
}

// Grouping fields that change activity can be attributed to. label/annotation grouping
// has no equivalent in the (cluster, namespace, kind)-keyed activity data, so those views
// simply get no outlines.
const ACTIVITY_SUPPORTED_FIELDS = new Set<GroupByField>(['cluster', 'namespace', 'kind'])

// Mirror buildOverviewHierarchy's value mapping so the activity group-ids match card ids exactly.
function activityFieldValue(field: GroupByField, a: ActivityGroupCount): string {
  switch (field) {
    case 'cluster': return a.cluster_name || 'unknown'
    case 'namespace': return a.namespace || 'cluster-level'
    case 'kind': return a.kind || 'unknown'
    default: return ''
  }
}

function calcDetailWidth(nodeCount: number): number {
  const nodeSize = 30, nodePadding = 3, containerPadding = 8
  let cols: number
  if (nodeCount <= 3) cols = nodeCount
  else if (nodeCount <= 8) cols = Math.min(4, nodeCount)
  else if (nodeCount <= 16) cols = Math.min(6, Math.ceil(nodeCount / 3))
  else cols = Math.min(10, Math.ceil(Math.sqrt(nodeCount)))
  return Math.max(210, cols * (nodeSize + nodePadding) - nodePadding + 2 * containerPadding)
}

function calcDetailHeight(nodeCount: number): number {
  const nodeSize = 30, nodePadding = 3, containerPadding = 8, labelHeight = 22
  let cols: number
  if (nodeCount <= 3) cols = nodeCount
  else if (nodeCount <= 8) cols = Math.min(4, nodeCount)
  else if (nodeCount <= 16) cols = Math.min(6, Math.ceil(nodeCount / 3))
  else cols = Math.min(10, Math.ceil(Math.sqrt(nodeCount)))
  const rows = Math.ceil(nodeCount / cols)
  return rows * (nodeSize + nodePadding) - nodePadding + 2 * containerPadding + labelHeight
}

interface CardAnimation { startTime: number; fromHeight: number; toHeight: number }

function GraphVisualizationRectangular({ objects, currentTime, groupingConfiguration, timeRange, liveMode, animations, overviewData, expandedCardNodes, loadingCardIds, onCardClick, forceDetail, eventHighlightUids, activityWindow }: GraphVisualizationProps) {
  const defaultGrouping: GroupingConfiguration = {
    levels: [
      { field: 'cluster' },
      { field: 'namespace' }
    ]
  }
  const activeGrouping = groupingConfiguration || defaultGrouping
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [selectedNode, setSelectedNode] = useState<GraphNode | null>(null)
  const [hoveredNode, setHoveredNode] = useState<GraphNode | null>(null)
  const [mousePos, setMousePos] = useState({ x: 0, y: 0 })
  const [dimensions, setDimensions] = useState({ width: 800, height: 600 })
  const [transform, setTransform] = useState({ x: 0, y: 0, scale: 1 })
  const currentTransformRef = useRef({ x: 0, y: 0, scale: 1 })
  currentTransformRef.current = transform
  const [contentBounds, setContentBounds] = useState({ width: 0, height: 0 })
  const [hoveredCard, setHoveredCard] = useState<CardBounds | null>(null)
  const [expandedKindCardIds, setExpandedKindCardIds] = useState<Set<string>>(new Set())
  const isOverview = !forceDetail
  const nodeBoundsRef = useRef<NodeBounds[]>([])
  const cardBoundsRef = useRef<CardBounds[]>([])
  const expandAnimationsRef = useRef<Map<string, CardAnimation>>(new Map())
  const animFrameRef = useRef<number | null>(null)
  
  // Use ref for animations to prevent render callback recreation
  const animationsRef = useRef(animations)
  animationsRef.current = animations

  // Uids to pulse in the canvas draw loop (accurate-timeline mode).
  // Mutated via ref so the render callback doesn't need to be recreated.
  const eventHighlightUidsRef = useRef<Set<string> | undefined>(eventHighlightUids)
  eventHighlightUidsRef.current = eventHighlightUids

  // Per-group change activity for the window under the playhead. The activityWindow rows are
  // already scoped server-side to that single window, so we just roll each (cluster, namespace,
  // kind) row up the grouping path. Keyed by the same group-id path that
  // buildOverviewHierarchy/drawGroup use, so cluster boxes and leaf cards both light up.
  const activityByGroup = useMemo(() => {
    const result = { map: new Map<string, GroupActivity>(), max: 0 }
    if (!activityWindow || activityWindow.length === 0) return result
    if (activeGrouping.levels.some(l => !ACTIVITY_SUPPORTED_FIELDS.has(l.field))) return result

    for (const a of activityWindow) {
      if (a.added + a.modified + a.deleted === 0) continue
      let pathKey = ''
      for (const level of activeGrouping.levels) {
        pathKey = pathKey ? `${pathKey}/${activityFieldValue(level.field, a)}` : activityFieldValue(level.field, a)
        const cur = result.map.get(pathKey) || { added: 0, modified: 0, deleted: 0, total: 0 }
        cur.added += a.added
        cur.modified += a.modified
        cur.deleted += a.deleted
        cur.total += a.added + a.modified + a.deleted
        result.map.set(pathKey, cur)
      }
    }
    for (const v of result.map.values()) if (v.total > result.max) result.max = v.total
    return result
  }, [activityWindow, activeGrouping])

  // Read from a ref inside the render callback so it doesn't need to be recreated each tick.
  const activityByGroupRef = useRef(activityByGroup)
  activityByGroupRef.current = activityByGroup

  // Web Worker state - offload heavy computation to background thread
  const workerRef = useRef<Worker | null>(null)
  const [fullGraphData, setFullGraphData] = useState<GraphData>({ nodes: [], links: [] })
  const [hierarchicalData, setHierarchicalData] = useState<GroupData>({
    id: 'root',
    name: 'All Resources',
    children: []
  })
  const [isProcessing, setIsProcessing] = useState(false)

  // Initialize Web Worker
  useEffect(() => {
    // Create worker with Vite's worker import syntax
    workerRef.current = new Worker(
      new URL('../workers/dataProcessor.worker.ts', import.meta.url),
      { type: 'module' }
    )

    // Handle messages from worker
    workerRef.current.onmessage = (event) => {
      const message = event.data

      switch (message.type) {
        case 'processComplete':
          setFullGraphData(message.graphData)
          setHierarchicalData(message.hierarchy)
          setIsProcessing(false)
          console.log(`📊 Processed ${message.graphData.nodes.length} nodes, ${message.graphData.links.length} links in ${message.processingTime.toFixed(2)}ms`)
          break

        case 'updateComplete':
          setFullGraphData(message.graphData)
          setHierarchicalData(message.hierarchy)
          break

        case 'rebuildComplete':
          setHierarchicalData(message.hierarchy)
          setIsProcessing(false)
          console.log(`Rebuilt hierarchy in ${message.processingTime.toFixed(2)}ms`)
          break

        case 'error':
          console.error('Worker error:', message.error)
          setIsProcessing(false)
          break
      }
    }

    workerRef.current.onerror = (error) => {
      console.error('Worker error:', error)
      setIsProcessing(false)
    }

    return () => {
      workerRef.current?.terminate()
      workerRef.current = null
    }
  }, [])

  // Track previous objects and grouping to detect what changed
  const prevObjectsLengthRef = useRef(0)
  const prevGroupingRef = useRef<string>('')
  const isInitialMount = useRef(true)
  
  // Send objects to worker for processing when they change
  useEffect(() => {
    if (!workerRef.current || objects.length === 0) return
    
    // In live mode with incremental updates, only send the new object
    if (liveMode && objects.length === prevObjectsLengthRef.current + 1 && prevObjectsLengthRef.current > 0) {
      // Incremental update - just send the new object
      const newObject = objects[objects.length - 1]
      workerRef.current.postMessage({
        type: 'updateWithNewObject',
        object: newObject
      })
    } else {
      // Full reprocess - initial load or bulk update
      setIsProcessing(true)
      workerRef.current.postMessage({
        type: 'processObjects',
        objects: objects,
        groupingConfig: activeGrouping
      })
    }
    prevObjectsLengthRef.current = objects.length
  }, [objects, liveMode]) // Only depend on objects and liveMode, NOT activeGrouping

  // Rebuild hierarchy when grouping config changes (but objects stay the same)
  useEffect(() => {
    const currentGrouping = JSON.stringify(activeGrouping)
    
    // Skip on initial mount or if no data yet
    if (isInitialMount.current || !workerRef.current || fullGraphData.nodes.length === 0) {
      isInitialMount.current = false
      prevGroupingRef.current = currentGrouping
      return
    }
    
    // Only rebuild if grouping actually changed
    if (currentGrouping !== prevGroupingRef.current) {
      setIsProcessing(true)
      workerRef.current.postMessage({
        type: 'rebuildHierarchy',
        groupingConfig: activeGrouping
      })
      prevGroupingRef.current = currentGrouping
    }
  }, [activeGrouping, fullGraphData.nodes.length])

  // Start per-card expand animations when expandedCardNodes changes
  useEffect(() => {
    if (!expandedCardNodes) return
    const SUMMARY_H = 140
    expandedCardNodes.forEach((nodes, groupId) => {
      if (!expandAnimationsRef.current.has(groupId)) {
        expandAnimationsRef.current.set(groupId, {
          startTime: performance.now(),
          fromHeight: SUMMARY_H,
          toHeight: calcDetailHeight(nodes.length),
        })
      }
    })
    // Clean up animations for collapsed cards
    expandAnimationsRef.current.forEach((_, id) => {
      if (!expandedCardNodes.has(id)) expandAnimationsRef.current.delete(id)
    })
  }, [expandedCardNodes])

  // Filter nodes based on current time and deletion status
  // Show only nodes that existed at or before the current time
  const visibleNodeIds = useMemo(() => {
    const visible = new Set<string>()
    const currentTimestamp = currentTime.getTime()
    
    fullGraphData.nodes.forEach(node => {
      // Show nodes that:
      // 1. Are not deleted
      // 2. Have a timestamp at or before the current time (have been created by this point)
      const nodeTimestamp = node.timestamp.getTime()
      if (node.status !== 'deleted' && nodeTimestamp <= currentTimestamp) {
        visible.add(node.id)
      }
    })
    
    console.log(`🔍 Time filter: Showing ${visible.size} of ${fullGraphData.nodes.length} nodes at time ${currentTime.toISOString()}`)
    
    return visible
  }, [fullGraphData, currentTime])

  // Layout configuration constants
  const nodeSize = 30
  const nodePadding = 3
  const containerPadding = 8
  const labelHeight = 22
  const groupSpacing = 10
  const maxNodesPerRow = 10

  // Calculate content-sized dimensions for each group
  const layoutRoot = useMemo(() => {
    // In overview mode with aggregate data, use the pre-built overview hierarchy
    const sourceHierarchy = (isOverview && overviewData && overviewData.length > 0)
      ? buildOverviewHierarchy(overviewData, activeGrouping)
      : hierarchicalData

    const calculateDimensions = (node: GroupData): LayoutNode => {
      const layoutNode: LayoutNode = {
        ...node,
        children: node.children ? node.children.map(c => ({ ...c } as LayoutNode)) : undefined
      }

      // Any expanded card (innermost leaf OR an intermediate container) renders its
      // detailed object grid in place of its normal content. Checked first so it applies
      // uniformly regardless of whether the card had aggregateItems or child cards.
      if (isOverview) {
        const expandedNodes = expandedCardNodes?.get(layoutNode.id)
        if (expandedNodes) {
          layoutNode.width = calcDetailWidth(expandedNodes.length)
          layoutNode.height = calcDetailHeight(expandedNodes.length)
          layoutNode.nodes = expandedNodes
          layoutNode.aggregateItems = undefined
          layoutNode.children = undefined
          return layoutNode
        }
      }

      // Overview aggregate leaf — summary card
      if (layoutNode.aggregateItems && layoutNode.aggregateItems.length > 0) {
        const hasContent = layoutNode.aggregateItems.some(i => i.total > 0)
        if (!hasContent) { layoutNode.width = 0; layoutNode.height = 0; return layoutNode }
        // Kind-list expanded: taller card to fit all kind rows
        if (expandedKindCardIds.has(layoutNode.id) && layoutNode.aggregateItems.length > 4) {
          layoutNode.width = 210
          layoutNode.height = calcKindCardHeight(layoutNode.aggregateItems.length)
          return layoutNode
        }
        layoutNode.width = 210
        layoutNode.height = 140
        return layoutNode
      }

      if (layoutNode.nodes && layoutNode.nodes.length > 0) {
        // Leaf node with objects
        const visibleNodes = layoutNode.nodes.filter(n => visibleNodeIds.has(n.id))
        if (visibleNodes.length === 0) {
          layoutNode.width = 0
          layoutNode.height = 0
          return layoutNode
        }

        // Overview mode: fixed-size summary card — no grid layout needed
        if (isOverview) {
          layoutNode.width = 210
          layoutNode.height = 140
          layoutNode.nodes = visibleNodes
          return layoutNode
        }

        // Detail mode: grid layout
        // Calculate grid dimensions
        let cols: number
        if (visibleNodes.length <= 3) {
          cols = visibleNodes.length
        } else if (visibleNodes.length <= 8) {
          cols = Math.min(4, visibleNodes.length)
        } else if (visibleNodes.length <= 16) {
          cols = Math.min(6, Math.ceil(visibleNodes.length / 3))
        } else {
          cols = Math.min(maxNodesPerRow, Math.ceil(Math.sqrt(visibleNodes.length)))
        }
        const rows = Math.ceil(visibleNodes.length / cols)
        
        layoutNode.width = cols * (nodeSize + nodePadding) - nodePadding + 2 * containerPadding
        layoutNode.height = rows * (nodeSize + nodePadding) - nodePadding + 2 * containerPadding + labelHeight
        layoutNode.nodes = visibleNodes
      } else if (layoutNode.children) {
        // Container node with children
        layoutNode.children = layoutNode.children
          .map(child => calculateDimensions(child))
          .filter(child => child.width! > 0)
        
        if (layoutNode.children.length === 0) {
          layoutNode.width = 0
          layoutNode.height = 0
          return layoutNode
        }
        
        // Arrange children in columns
        let currentX = containerPadding
        let currentY = labelHeight + containerPadding
        let columnHeight = 0
        let maxColumnWidth = 0
        let totalWidth = containerPadding
        let maxHeight = labelHeight + containerPadding
        
        layoutNode.children.forEach((child) => {
          const maxColumnHeight = 400
          if (columnHeight > 0 && columnHeight + child.height! + groupSpacing > maxColumnHeight) {
            currentX += maxColumnWidth + groupSpacing
            currentY = labelHeight + containerPadding
            columnHeight = 0
            maxColumnWidth = 0
          }
          
          child.x = currentX
          child.y = currentY
          
          currentY += child.height! + groupSpacing
          columnHeight += child.height! + groupSpacing
          maxColumnWidth = Math.max(maxColumnWidth, child.width!)
          totalWidth = Math.max(totalWidth, currentX + child.width! + containerPadding)
          maxHeight = Math.max(maxHeight, currentY)
        })
        
        layoutNode.width = totalWidth
        layoutNode.height = maxHeight
      }
      
      return layoutNode
    }

    const root = calculateDimensions(sourceHierarchy) as LayoutNode

    // Arrange top-level groups
    if (root.children) {
      const availableWidth = dimensions.width - 60
      const sortedChildren = [...root.children].sort((a, b) => a.id.localeCompare(b.id))
      const columns: { x: number, height: number, width: number }[] = []
      
      sortedChildren.forEach((child) => {
        if (child.width! > 0) {
          let targetColumn = 0
          if (columns.length > 0) {
            targetColumn = columns.reduce((minIdx, col, idx, arr) => 
              col.height < arr[minIdx].height ? idx : minIdx, 0)
          }
          
          const wouldFit = columns.length === 0 || 
            (columns.reduce((sum, col) => sum + col.width, 0) + child.width! + groupSpacing * columns.length < availableWidth)
          
          if (wouldFit || columns.length === 0) {
            if (columns.length === 0 || columns[targetColumn].height === 0) {
              const x = columns.reduce((sum, col) => sum + col.width + groupSpacing, 0)
              child.x = x
              child.y = 0
              columns.push({ x, height: child.height!, width: child.width! })
            } else {
              child.x = columns[targetColumn].x
              child.y = columns[targetColumn].height + groupSpacing
              columns[targetColumn].height += child.height! + groupSpacing
              columns[targetColumn].width = Math.max(columns[targetColumn].width, child.width!)
            }
          } else {
            child.x = columns[targetColumn].x
            child.y = columns[targetColumn].height + groupSpacing
            columns[targetColumn].height += child.height! + groupSpacing
            columns[targetColumn].width = Math.max(columns[targetColumn].width, child.width!)
          }
        }
      })
    }

    // Calculate total content bounds
    let maxX = 0
    let maxY = 0
    if (root.children) {
      root.children.forEach(child => {
        const childRight = (child.x || 0) + (child.width || 0)
        const childBottom = (child.y || 0) + (child.height || 0)
        maxX = Math.max(maxX, childRight)
        maxY = Math.max(maxY, childBottom)
      })
    }
    
    // Add some padding to content bounds
    setContentBounds({ width: maxX + 20, height: maxY + 20 })

    return root
  }, [hierarchicalData, visibleNodeIds, dimensions.width, isOverview, overviewData, activeGrouping, expandedCardNodes, expandedKindCardIds])

  // Color scale for groups
  const groupColors = useMemo(() => d3.scaleOrdinal(d3.schemeCategory10), [])

  // Ref to store render function and break dependency cycles
  const renderRef = useRef<() => void>()

  // Canvas rendering function
  const render = useCallback(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const ctx = canvas.getContext('2d', {
      alpha: false,
      desynchronized: true
    })
    if (!ctx) return

    // Reset transform to clear entire canvas properly
    ctx.save()
    ctx.setTransform(1, 0, 0, 1, 0, 0)
    
    // Clear entire canvas (accounting for device pixel ratio)
    ctx.fillStyle = '#0a0a0a'
    ctx.fillRect(0, 0, canvas.width, canvas.height)
    
    ctx.restore()

    // Apply device pixel ratio scale, then user transform
    // Note: DPR scaling makes drawing operations work in CSS pixels
    const dpr = window.devicePixelRatio || 1
    ctx.save()
    ctx.scale(dpr, dpr)
    
    // Apply user transform (translate and scale)
    ctx.translate(transform.x, transform.y)
    ctx.scale(transform.scale, transform.scale)

    // Calculate viewport bounds for culling
    const viewportBounds = {
      left: -transform.x / transform.scale,
      top: -transform.y / transform.scale,
      right: (canvas.width - transform.x) / transform.scale,
      bottom: (canvas.height - transform.y) / transform.scale
    }

    // Clear bounds for hit detection
    nodeBoundsRef.current = []
    cardBoundsRef.current = []

    // Recursive function to draw groups
    const drawGroup = (group: LayoutNode, parentX = 0, parentY = 0, depth = 0) => {
      if (!group.width || group.width === 0) return
      
      const x = parentX + (group.x || 0)
      const y = parentY + (group.y || 0)
      const width = group.width || 0
      const height = group.height || 0
      
      // Viewport culling
      if (x + width < viewportBounds.left - 50 || x > viewportBounds.right + 50 ||
          y + height < viewportBounds.top - 50 || y > viewportBounds.bottom + 50) {
        return
      }
      
      // Draw group rectangle
      
      ctx.fillStyle = depth === 1 ? groupColors(group.name) : '#1a1a1a'
      ctx.globalAlpha = depth === 1 ? 0.15 : 0.8
      ctx.fillRect(x, y, width, height)
      
      // Draw border
      ctx.globalAlpha = 1
      ctx.strokeStyle = depth === 1 ? '#555' : '#333'
      ctx.lineWidth = Math.max(1, 3 - depth) / transform.scale
      ctx.strokeRect(x, y, width, height)

      // Change-activity outline: a colored glow around any group/card seeing events in the
      // activity bin under the playhead. green=ADDED, orange=MODIFIED, red=DELETED (dominant op).
      // Intensity (width/alpha/blur) scales with event volume; a sine term makes it breathe.
      const activity = activityByGroupRef.current.map.get(group.id)
      const activityMax = activityByGroupRef.current.max
      if (activity && activity.total > 0 && activityMax > 0) {
        const intensity = Math.log1p(activity.total) / Math.log1p(activityMax)
        const maxOp = Math.max(activity.added, activity.modified, activity.deleted)
        const color = maxOp === activity.deleted ? '#f44336'
          : maxOp === activity.modified ? '#ff9800'
          : '#4caf50'
        const breathe = 0.6 + 0.4 * Math.sin(performance.now() / 500)
        ctx.save()
        ctx.globalAlpha = Math.min(1, (0.4 + 0.6 * intensity) * breathe)
        ctx.strokeStyle = color
        ctx.shadowColor = color
        ctx.shadowBlur = 6 + 14 * intensity
        ctx.lineWidth = (1.5 + 3.5 * intensity) / transform.scale
        ctx.strokeRect(x, y, width, height)
        ctx.restore()
      }
      
      // Draw group label (only if large enough)
      if (transform.scale > 0.3 && (group.width || 0) > 30) {
        ctx.fillStyle = depth === 1 ? '#fff' : '#ddd'
        const fontSize = depth === 1 ? 14 : 12
        ctx.font = `bold ${fontSize}px sans-serif`
        ctx.textAlign = 'left'
        ctx.textBaseline = 'top'
        
        // Measure text and truncate if necessary
        const maxWidth = width - 16 // Leave padding on both sides
        const textMetrics = ctx.measureText(group.name)
        
        if (textMetrics.width <= maxWidth) {
          // Text fits, draw it normally
          ctx.fillText(group.name, x + 8, y + 6)
        } else if (maxWidth > 20) {
          // Text doesn't fit, truncate with ellipsis
          let truncated = group.name
          let truncatedWidth = textMetrics.width
          
          // Binary search for the right length
          while (truncatedWidth > maxWidth - ctx.measureText('...').width && truncated.length > 1) {
            truncated = truncated.slice(0, -1)
            truncatedWidth = ctx.measureText(truncated).width
          }
          
          if (truncated.length > 0) {
            ctx.fillText(truncated + '...', x + 8, y + 6)
          }
        }
        // If maxWidth is too small, don't draw the label at all
      }
      
      // Register intermediate container cards (any level except the outermost) as
      // expandable, so clicking e.g. a namespace that contains further card levels
      // expands it to the detailed object view. Bounds fully contain children, but the
      // hit-test picks the smallest matching card so the deepest one wins.
      if (isOverview && group.children && group.children.length > 0 && depth >= 2 &&
          !expandedCardNodes?.has(group.id)) {
        cardBoundsRef.current.push({
          x, y, width, height,
          groupId: group.id,
          groups: groupFiltersFromId(group.id, activeGrouping)
        })
      }

      // Draw aggregate summary card for overview mode with pre-fetched aggregate data
      if (group.aggregateItems && group.aggregateItems.length > 0) {
        const groupFilters = groupFiltersFromId(group.id, activeGrouping)
        const groupBoundsEntry = { x, y, width, height, groupId: group.id, groups: groupFilters }

        const isKindExpanded = expandedKindCardIds.has(group.id)
        const isHovered = hoveredCard?.groupId === group.id && !hoveredCard?.isKindToggle

        // Hover highlight (only for main card area, not toggle zone)
        if (isHovered) {
          ctx.fillStyle = 'rgba(255,255,255,0.08)'
          ctx.fillRect(x, y, width, height)
        }

        let total = 0, modifiedCount = 0
        const sortedItems = [...group.aggregateItems].sort((a, b) => b.total - a.total)
        sortedItems.forEach(item => { total += item.total; modifiedCount += item.modified_count })
        const activeCount = total - modifiedCount
        const dominantLabel = sortedItems[0]?.label || ''
        const accentColor = getKindColor(dominantLabel)

        // Left accent bar
        ctx.fillStyle = accentColor
        ctx.globalAlpha = 0.9
        ctx.fillRect(x, y, 4, height)
        ctx.globalAlpha = 1

        // Total label — top-right, subtle
        ctx.fillStyle = '#777777'
        ctx.font = '10px sans-serif'
        ctx.textAlign = 'right'
        ctx.textBaseline = 'top'
        ctx.fillText(`${total.toLocaleString()} objects`, x + width - 8, y + 12)

        // Kind rows — all if expanded, otherwise up to 4
        const visibleItems = isKindExpanded ? sortedItems : sortedItems.slice(0, 4)
        const moreCount = sortedItems.length - 4
        visibleItems.forEach((item, idx) => {
          const rowY = y + 30 + idx * 16
          const dotColor = getKindColor(item.label)
          ctx.fillStyle = dotColor
          ctx.beginPath()
          ctx.arc(x + 14, rowY + 5, 3, 0, Math.PI * 2)
          ctx.fill()
          ctx.fillStyle = '#cccccc'
          ctx.font = '11px sans-serif'
          ctx.textAlign = 'left'
          ctx.textBaseline = 'top'
          ctx.fillText(item.label, x + 22, rowY)
          ctx.fillStyle = '#ffffff'
          ctx.textAlign = 'right'
          ctx.fillText(item.total.toLocaleString(), x + width - 8, rowY)
        })

        // "+N more" / "▲ show less" toggle — only when >4 kinds
        if (sortedItems.length > 4) {
          const toggleY = y + 30 + visibleItems.length * 16
          // Push toggle zone FIRST so it wins hit detection over the full card bounds
          cardBoundsRef.current.push({
            x, y: toggleY - 2, width, height: 16,
            groupId: group.id, groups: groupFilters,
            isKindToggle: true
          })
          const isToggleHovered = hoveredCard?.groupId === group.id && hoveredCard?.isKindToggle
          ctx.fillStyle = isToggleHovered ? '#aaaaaa' : '#666666'
          ctx.font = '10px sans-serif'
          ctx.textAlign = 'left'
          ctx.textBaseline = 'top'
          ctx.fillText(isKindExpanded ? '▲ show less' : `+${moreCount} more…`, x + 14, toggleY)
        }

        // Full card bounds pushed AFTER toggle so toggle zone takes priority.
        // Only register as expandable when this card is nested below the outermost level
        // (depth 1 = first level = non-expandable container).
        if (depth >= 2) {
          cardBoundsRef.current.push(groupBoundsEntry)
        }

        // Operation bar — bottom (position is relative to dynamic height)
        const barMargin = 10
        const barX = x + barMargin, barY = y + height - 18
        const barW = width - barMargin * 2, barH = 7
        if (total > 0) {
          ctx.fillStyle = '#333333'
          ctx.fillRect(barX, barY, barW, barH)
          const activeW = (activeCount / total) * barW
          ctx.fillStyle = '#4caf50'
          ctx.fillRect(barX, barY, activeW, barH)
          if (modifiedCount > 0) {
            ctx.fillStyle = '#ff9800'
            ctx.fillRect(barX + activeW, barY, (modifiedCount / total) * barW, barH)
          }
        }

        // Loading indicator
        if (loadingCardIds?.has(group.id)) {
          ctx.fillStyle = 'rgba(10,10,10,0.55)'
          ctx.fillRect(x, y, width, height)
          ctx.fillStyle = '#aaaaaa'
          ctx.font = '11px sans-serif'
          ctx.textAlign = 'center'
          ctx.textBaseline = 'middle'
          ctx.fillText('Loading…', x + width / 2, y + height / 2 + 16)
        }
        return
      }

      // Draw nodes if this is a leaf group
      if (group.nodes && group.nodes.length > 0) {
        // Overview mode: draw a summary card for non-expanded groups
        if (isOverview && !expandedCardNodes?.has(group.id)) {
          // Capture card bounds for click detection (not the outermost level).
          if (depth >= 2) {
            cardBoundsRef.current.push({
              x, y, width, height,
              groupId: group.id,
              groups: groupFiltersFromId(group.id, activeGrouping)
            })
          }

          const isHovered = hoveredCard?.groupId === group.id
          if (isHovered) {
            ctx.fillStyle = 'rgba(255,255,255,0.08)'
            ctx.fillRect(x, y, width, height)
          }

          // Build kind frequency map
          let modifiedCount = 0
          const kindFreq = new Map<string, number>()
          group.nodes.forEach(node => {
            if (node.status === 'modified') modifiedCount++
            kindFreq.set(node.kind, (kindFreq.get(node.kind) || 0) + 1)
          })
          const total = group.nodes.length
          const activeCount = total - modifiedCount
          const sortedKinds = [...kindFreq.entries()].sort((a, b) => b[1] - a[1])
          const dominantKind = sortedKinds[0]?.[0] || ''
          const accentColor = getKindColor(dominantKind)

          // Left accent bar
          ctx.fillStyle = accentColor
          ctx.globalAlpha = 0.9
          ctx.fillRect(x, y, 4, height)
          ctx.globalAlpha = 1

          // Total label — top-right, subtle
          ctx.fillStyle = '#777777'
          ctx.font = '10px sans-serif'
          ctx.textAlign = 'right'
          ctx.textBaseline = 'top'
          ctx.fillText(`${total.toLocaleString()} objects`, x + width - 8, y + 12)

          // Kind rows — up to 4
          const visibleKinds = sortedKinds.slice(0, 4)
          const moreCount = sortedKinds.length - 4
          visibleKinds.forEach(([kind, count], idx) => {
            const rowY = y + 30 + idx * 16
            ctx.fillStyle = getKindColor(kind)
            ctx.beginPath()
            ctx.arc(x + 14, rowY + 5, 3, 0, Math.PI * 2)
            ctx.fill()
            ctx.fillStyle = '#cccccc'
            ctx.font = '11px sans-serif'
            ctx.textAlign = 'left'
            ctx.textBaseline = 'top'
            ctx.fillText(kind, x + 22, rowY)
            ctx.fillStyle = '#ffffff'
            ctx.textAlign = 'right'
            ctx.fillText(count.toLocaleString(), x + width - 8, rowY)
          })
          if (moreCount > 0) {
            const moreY = y + 30 + 4 * 16
            ctx.fillStyle = '#666666'
            ctx.font = '10px sans-serif'
            ctx.textAlign = 'left'
            ctx.textBaseline = 'top'
            ctx.fillText(`+${moreCount} more…`, x + 14, moreY)
          }

          // Operation bar — bottom
          const barMargin = 10
          const barX = x + barMargin
          const barY = y + height - 18
          const barW = width - barMargin * 2
          const barH = 7
          if (total > 0) {
            ctx.fillStyle = '#333333'
            ctx.fillRect(barX, barY, barW, barH)
            const activeW = (activeCount / total) * barW
            ctx.fillStyle = '#4caf50'
            ctx.fillRect(barX, barY, activeW, barH)
            if (modifiedCount > 0) {
              ctx.fillStyle = '#ff9800'
              ctx.fillRect(barX + activeW, barY, (modifiedCount / total) * barW, barH)
            }
          }

          return
        }

        // Detail node drawing (forceDetail mode or expanded card in overview).
        // Always clip to card bounds to prevent overflow into adjacent cards.
        // During animation, clip height grows from 0 to full.
        ctx.save()
        const anim = expandAnimationsRef.current.get(group.id)
        let clipH = height
        if (anim) {
          const DURATION = 350
          const elapsed = performance.now() - anim.startTime
          const t = Math.min(elapsed / DURATION, 1)
          const ease = 1 - Math.pow(1 - t, 3) // cubic ease-out
          clipH = anim.fromHeight + (anim.toHeight - anim.fromHeight) * ease
          if (t < 1) {
            if (animFrameRef.current) cancelAnimationFrame(animFrameRef.current)
            animFrameRef.current = requestAnimationFrame(() => renderRef.current?.())
          } else {
            expandAnimationsRef.current.delete(group.id)
          }
        }
        ctx.beginPath()
        ctx.rect(x, y, width, clipH)
        ctx.clip()

        // Register click bounds for expanded card so user can click to collapse
        if (isOverview && expandedCardNodes?.has(group.id)) {
          cardBoundsRef.current.push({
            x, y, width, height,
            groupId: group.id,
            groups: groupFiltersFromId(group.id, activeGrouping)
          })
        }

        // Calculate grid dimensions
        let cols: number
        if (group.nodes.length <= 3) {
          cols = group.nodes.length
        } else if (group.nodes.length <= 8) {
          cols = Math.min(4, group.nodes.length)
        } else if (group.nodes.length <= 16) {
          cols = Math.min(6, Math.ceil(group.nodes.length / 3))
        } else {
          cols = Math.min(maxNodesPerRow, Math.ceil(Math.sqrt(group.nodes.length)))
        }

        group.nodes.forEach((node, i) => {
          const col = i % cols
          const row = Math.floor(i / cols)
          const nodeX = x + containerPadding + col * (nodeSize + nodePadding)
          const nodeY = y + labelHeight + containerPadding + row * (nodeSize + nodePadding)
          
          // Store bounds for hit detection
          nodeBoundsRef.current.push({
            x: nodeX,
            y: nodeY,
            width: nodeSize,
            height: nodeSize,
            node
          })
          
          // Draw node background
          ctx.fillStyle = getKindColor(node.kind)
          ctx.fillRect(nodeX, nodeY, nodeSize, nodeSize)
          
          // Apply animation effects if in live mode
          if (liveMode && animationsRef.current) {
            const animation = animationsRef.current.getAnimationForObject(node.id)
            if (animation) {
              const animationColor = getAnimationColor(animation)
              if (animationColor !== 'transparent') {
                ctx.strokeStyle = animationColor
                ctx.lineWidth = 3 / transform.scale
                ctx.strokeRect(nodeX, nodeY, nodeSize, nodeSize)
              }
              
              // Apply pulse effect for added/updated
              const styles = getAnimationStyles(animation)
              const opacity = typeof styles.opacity === 'number' ? styles.opacity : 1
              if (opacity < 1) {
                ctx.globalAlpha = opacity
                ctx.fillStyle = getKindColor(node.kind)
                ctx.fillRect(nodeX, nodeY, nodeSize, nodeSize)
                ctx.globalAlpha = 1
              }
            }
          }
          
          // Highlight hovered node with glow effect
          if (hoveredNode?.id === node.id) {
            // Add shadow/glow effect
            ctx.shadowColor = '#ffeb3b'
            ctx.shadowBlur = 8
            ctx.strokeStyle = '#ffeb3b'
            ctx.lineWidth = 3 / transform.scale
            ctx.strokeRect(nodeX, nodeY, nodeSize, nodeSize)
            // Reset shadow
            ctx.shadowColor = 'transparent'
            ctx.shadowBlur = 0
          } else if (eventHighlightUidsRef.current?.has(node.id)) {
            // Accurate-timeline mode: pulse nodes whose events fire at currentTime.
            ctx.shadowColor = '#00e5ff'
            ctx.shadowBlur = 10
            ctx.strokeStyle = '#00e5ff'
            ctx.lineWidth = 2.5 / transform.scale
            ctx.strokeRect(nodeX, nodeY, nodeSize, nodeSize)
            ctx.shadowColor = 'transparent'
            ctx.shadowBlur = 0
          } else {
            // Normal border
            ctx.strokeStyle = '#fff'
            ctx.lineWidth = 1 / transform.scale
            ctx.strokeRect(nodeX, nodeY, nodeSize, nodeSize)
          }
          
          // Draw node icon (only when zoomed in enough)
          if (transform.scale > 0.4) {
            ctx.fillStyle = '#fff'
            ctx.font = 'bold 12px sans-serif'
            ctx.textAlign = 'center'
            ctx.textBaseline = 'middle'
            ctx.fillText(node.kind[0], nodeX + nodeSize / 2, nodeY + nodeSize / 2)
          }
        })

        ctx.restore()
      }

      // Draw children
      if (group.children) {
        group.children.forEach(child => {
          drawGroup(child, x, y, depth + 1)
        })
      }
    }
    
    // Draw all groups
    if (layoutRoot.children) {
      layoutRoot.children.forEach(child => {
        drawGroup(child, 0, 0, 1)
      })
    }

    ctx.restore()
  }, [layoutRoot, transform, visibleNodeIds, hoveredNode, hoveredCard, isOverview, activeGrouping, liveMode, groupColors, expandedCardNodes, loadingCardIds, expandedKindCardIds])
  
  // Update ref after render is defined
  renderRef.current = render

  // Calculate dynamic minimum zoom scale based on content size
  const minZoomScale = useMemo(() => {
    if (contentBounds.width === 0 || contentBounds.height === 0) {
      return 0.01 // Very small default
    }
    
    // Calculate scale needed to fit all content in viewport with 10% padding
    const scaleX = (dimensions.width * 0.9) / contentBounds.width
    const scaleY = (dimensions.height * 0.9) / contentBounds.height
    const minScale = Math.min(scaleX, scaleY)
    
    // Allow zooming out to at least fit all content, with a lower bound
    return Math.min(0.01, minScale * 0.8) // 80% of fit scale to give some margin
  }, [contentBounds, dimensions])

  // Set up D3 zoom behavior
  useEffect(() => {
    if (!canvasRef.current) return

    const canvas = d3.select(canvasRef.current)

    const zoom = d3.zoom<HTMLCanvasElement, unknown>()
      .scaleExtent([minZoomScale, 4])
      .on('zoom', (event) => {
        setTransform({
          x: event.transform.x,
          y: event.transform.y,
          scale: event.transform.k
        })
      })

    canvas.call(zoom)

    // Restore current transform so D3 internal state doesn't reset to identity
    // when this effect re-runs due to contentBounds/minZoomScale changes.
    const t = currentTransformRef.current
    canvas.call(zoom.transform, d3.zoomIdentity.translate(t.x, t.y).scale(t.scale))

    // Zoom controls for UI buttons
    ;(window as any).graphZoomIn = () => {
      canvas.transition().duration(300).call(zoom.scaleBy, 1.5)
    }
    ;(window as any).graphZoomOut = () => {
      canvas.transition().duration(300).call(zoom.scaleBy, 0.67)
    }
    ;(window as any).graphResetZoom = () => {
      canvas.transition().duration(300).call(zoom.transform, d3.zoomIdentity)
    }
    ;(window as any).graphFitAll = () => {
      if (contentBounds.width === 0 || contentBounds.height === 0) return
      
      // Calculate scale to fit all content with 10% padding
      const scaleX = (dimensions.width * 0.9) / contentBounds.width
      const scaleY = (dimensions.height * 0.9) / contentBounds.height
      const scale = Math.min(scaleX, scaleY)
      
      // Center the content
      const x = (dimensions.width - contentBounds.width * scale) / 2
      const y = (dimensions.height - contentBounds.height * scale) / 2
      
      const transform = d3.zoomIdentity.translate(x, y).scale(scale)
      canvas.transition().duration(500).call(zoom.transform, transform)
    }

    return () => {
      canvas.on('.zoom', null)
    }
  }, [minZoomScale, contentBounds, dimensions])

  // Handle mouse interactions for hover and click
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const handleMouseMove = (e: MouseEvent) => {
      const rect = canvas.getBoundingClientRect()

      const mouseX = e.clientX - rect.left
      const mouseY = e.clientY - rect.top

      const worldX = (mouseX - transform.x) / transform.scale
      const worldY = (mouseY - transform.y) / transform.scale

      setMousePos({ x: e.clientX, y: e.clientY })

      if (isOverview) {
        // Check node bounds first — only populated for expanded cards
        let foundNode: GraphNode | null = null
        for (const bounds of nodeBoundsRef.current) {
          if (worldX >= bounds.x && worldX <= bounds.x + bounds.width &&
              worldY >= bounds.y && worldY <= bounds.y + bounds.height) {
            foundNode = bounds.node
            break
          }
        }
        if (foundNode) {
          setHoveredNode(foundNode)
          setHoveredCard(null)
          canvas.style.cursor = 'pointer'
          return
        }
        // Fall through to card bounds. Cards can nest (container fully contains its
        // children), so pick the SMALLEST matching bounds — that's the deepest card,
        // or a kind-toggle zone — instead of the first hit.
        let foundCard: CardBounds | null = null
        let foundArea = Infinity
        for (const bounds of cardBoundsRef.current) {
          if (worldX >= bounds.x && worldX <= bounds.x + bounds.width &&
              worldY >= bounds.y && worldY <= bounds.y + bounds.height) {
            const area = bounds.width * bounds.height
            if (area < foundArea) { foundArea = area; foundCard = bounds }
          }
        }
        setHoveredCard(foundCard)
        setHoveredNode(null)
        canvas.style.cursor = foundCard ? 'pointer' : 'grab'
      } else {
        // In detail mode: check node bounds
        let found: GraphNode | null = null
        const tolerance = 2 / transform.scale
        for (const bounds of nodeBoundsRef.current) {
          if (worldX >= bounds.x - tolerance && worldX <= bounds.x + bounds.width + tolerance &&
              worldY >= bounds.y - tolerance && worldY <= bounds.y + bounds.height + tolerance) {
            found = bounds.node
            break
          }
        }
        setHoveredNode(found)
        setHoveredCard(null)
        canvas.style.cursor = found ? 'pointer' : 'grab'
      }
    }

    const handleClick = () => {
      if (isOverview) {
        if (hoveredNode) {
          setSelectedNode(hoveredNode)
        } else if (hoveredCard?.isKindToggle) {
          setExpandedKindCardIds(prev => {
            const s = new Set(prev)
            if (s.has(hoveredCard.groupId)) s.delete(hoveredCard.groupId)
            else s.add(hoveredCard.groupId)
            return s
          })
        } else if (hoveredCard) {
          onCardClick?.(hoveredCard.groupId, hoveredCard.groups)
        }
      } else {
        if (hoveredNode) {
          setSelectedNode(hoveredNode)
        }
      }
    }

    canvas.addEventListener('mousemove', handleMouseMove)
    canvas.addEventListener('click', handleClick)

    return () => {
      canvas.removeEventListener('mousemove', handleMouseMove)
      canvas.removeEventListener('click', handleClick)
    }
  }, [transform, hoveredNode, hoveredCard, isOverview, onCardClick])

  // Throttle renders to avoid blocking main thread
  const renderTimeoutRef = useRef<NodeJS.Timeout | null>(null)
  const lastRenderTimeRef = useRef<number>(0)
  
  // Throttled render function - max 30 FPS to keep UI responsive
  const scheduleRender = useCallback(() => {
    const now = Date.now()
    const timeSinceLastRender = now - lastRenderTimeRef.current
    const minInterval = 33 // 30 FPS max
    
    if (timeSinceLastRender < minInterval) {
      // Too soon, schedule for later
      if (renderTimeoutRef.current) {
        clearTimeout(renderTimeoutRef.current)
      }
      renderTimeoutRef.current = setTimeout(() => {
        renderRef.current?.()
        lastRenderTimeRef.current = Date.now()
      }, minInterval - timeSinceLastRender)
    } else {
      // Enough time has passed, render now
      renderRef.current?.()
      lastRenderTimeRef.current = now
    }
  }, [])

  // Render when anything changes (throttled)
  useEffect(() => {
    scheduleRender()
    return () => {
      if (renderTimeoutRef.current) {
        clearTimeout(renderTimeoutRef.current)
      }
    }
  }, [layoutRoot, transform, visibleNodeIds, hoveredNode, hoveredCard, expandedCardNodes, loadingCardIds, expandedKindCardIds, eventHighlightUids, activityWindow, currentTime, scheduleRender])

  // Animation loop for live mode (reduced to 10 FPS to keep UI responsive)
  useEffect(() => {
    if (!liveMode) return
    const interval = setInterval(() => renderRef.current?.(), 100) // 10 FPS for animations
    return () => clearInterval(interval)
  }, [liveMode])

  // Breathing redraw loop for the overview change-activity outlines (10 FPS).
  // Only runs in overview when the bin under the playhead actually has activity to animate,
  // so quiet periods don't trigger idle canvas redraws.
  useEffect(() => {
    if (forceDetail) return
    if (activityByGroup.map.size === 0) return
    const interval = setInterval(() => renderRef.current?.(), 100)
    return () => clearInterval(interval)
  }, [forceDetail, activityByGroup])

  // Handle resize
  useEffect(() => {
    const handleResize = () => {
      if (canvasRef.current) {
        const container = canvasRef.current.parentElement
        if (container) {
          const { width, height } = container.getBoundingClientRect()
          setDimensions({ width, height })
          
          // Set canvas size with device pixel ratio for sharp rendering
          const dpr = window.devicePixelRatio || 1
          canvasRef.current.width = width * dpr
          canvasRef.current.height = height * dpr
          canvasRef.current.style.width = `${width}px`
          canvasRef.current.style.height = `${height}px`
          
          // Note: DPR scaling is applied in the render function, not here
        }
      }
    }
    
    handleResize()
    window.addEventListener('resize', handleResize)
    return () => window.removeEventListener('resize', handleResize)
  }, [])

  return (
    <Box sx={{ position: 'relative', width: '100%', height: '100%' }}>
      <canvas
        ref={canvasRef}
        style={{
          width: '100%',
          height: '100%',
          cursor: 'grab',
          display: 'block'
        }}
      />
      
      {/* Processing indicator */}
      {isProcessing && (
        <Box sx={{ 
          position: 'absolute', 
          top: '50%', 
          left: '50%', 
          transform: 'translate(-50%, -50%)',
          bgcolor: 'rgba(0, 0, 0, 0.7)',
          p: 3,
          borderRadius: 2,
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          gap: 2
        }}>
          <CircularProgress />
          <Typography variant="body2" color="white">
            Processing {objects.length.toLocaleString()} objects...
          </Typography>
        </Box>
      )}
      
      {/* Zoom Controls */}
      <Box sx={{ position: 'absolute', top: 10, right: 10, display: 'flex', flexDirection: 'column', gap: 1 }}>
        <Paper sx={{ p: 1 }}>
          <Tooltip title="Zoom In">
            <IconButton size="small" onClick={() => (window as any).graphZoomIn?.()}>
              <ZoomIn />
            </IconButton>
          </Tooltip>
          <Tooltip title="Zoom Out">
            <IconButton size="small" onClick={() => (window as any).graphZoomOut?.()}>
              <ZoomOut />
            </IconButton>
          </Tooltip>
          <Tooltip title="Fit All">
            <IconButton size="small" onClick={() => (window as any).graphFitAll?.()}>
              <FitScreen />
            </IconButton>
          </Tooltip>
          <Tooltip title="Reset Zoom">
            <IconButton size="small" onClick={() => (window as any).graphResetZoom?.()}>
              <CenterFocusStrong />
            </IconButton>
          </Tooltip>
        </Paper>
        <Chip
          label={isOverview ? 'Overview' : 'Detail'}
          size="small"
          color={isOverview ? 'default' : 'primary'}
          sx={{ cursor: 'default' }}
        />
      </Box>

      {/* Card hover tooltip (overview mode) */}
      {hoveredCard && (
        <Paper
          sx={{
            position: 'fixed',
            left: mousePos.x + 10,
            top: mousePos.y + 10,
            p: 1,
            pointerEvents: 'none',
            zIndex: 1000,
          }}
        >
          <Typography variant="caption" component="div" sx={{ fontWeight: 'bold' }}>
            {hoveredCard.isKindToggle
              ? (expandedKindCardIds.has(hoveredCard.groupId) ? 'Show fewer kinds' : 'Show all kinds')
              : (expandedCardNodes?.has(hoveredCard.groupId) ? 'Click to collapse' : 'Click to expand')}
          </Typography>
          <Typography variant="caption" component="div" color="text.secondary">
            {hoveredCard.groupId}
          </Typography>
        </Paper>
      )}

      {/* Hover Tooltip */}
      {hoveredNode && (
        <Paper
          sx={{
            position: 'fixed',
            left: mousePos.x + 10,
            top: mousePos.y + 10,
            p: 1,
            pointerEvents: 'none',
            zIndex: 1000,
            maxWidth: 250
          }}
        >
          <Typography variant="caption" component="div" sx={{ fontWeight: 'bold' }}>
            {hoveredNode.kind}: {hoveredNode.label}
          </Typography>
          <Typography variant="caption" component="div" color="text.secondary">
            Namespace: {hoveredNode.namespace}
          </Typography>
          <Typography variant="caption" component="div" color="text.secondary">
            Cluster: {hoveredNode.cluster}
          </Typography>
          <Typography variant="caption" component="div" color="text.secondary">
            Status: {hoveredNode.status}
          </Typography>
        </Paper>
      )}

      {/* Selected Node Details */}
      {selectedNode && visibleNodeIds.has(selectedNode.id) && (
        <Paper sx={{ position: 'absolute', bottom: 10, left: 10, p: 2, maxWidth: 300 }}>
          <Typography variant="h6">{selectedNode.kind}: {selectedNode.label}</Typography>
          <Typography variant="body2" color="text.secondary">
            Cluster: {selectedNode.cluster}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Namespace: {selectedNode.namespace}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Last Update: {selectedNode.timestamp.toLocaleString()}
          </Typography>
          <Chip 
            label={selectedNode.status} 
            size="small" 
            color={selectedNode.status === 'active' ? 'success' : 
                   selectedNode.status === 'deleted' ? 'error' : 'warning'}
            sx={{ mt: 1 }}
          />
        </Paper>
      )}

      {/* Legend - Show object types sorted by frequency (most common first) — only in detail mode */}
      {!isOverview && <Paper sx={{ position: 'absolute', top: 10, left: 10, p: 2, maxHeight: '200px', overflowY: 'auto' }}>
        <Typography variant="subtitle2" gutterBottom>Object Types</Typography>
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, maxWidth: 200 }}>
          {(() => {
            // Count frequency of each kind
            const kindCounts = new Map<string, number>()
            fullGraphData.nodes.forEach(node => {
              kindCounts.set(node.kind, (kindCounts.get(node.kind) || 0) + 1)
            })
            
            // Sort by frequency (descending), then alphabetically for ties
            return Array.from(kindCounts.entries())
              .sort((a, b) => {
                const countDiff = b[1] - a[1]
                return countDiff !== 0 ? countDiff : a[0].localeCompare(b[0])
              })
              .map(([kind, count]) => (
                <Chip
                  key={kind}
                  label={`${kind} (${count})`}
                  size="small"
                  sx={{ 
                    backgroundColor: getKindColor(kind), 
                    color: 'white',
                    fontSize: '0.7rem',
                  }}
                />
              ))
          })()}
        </Box>
      </Paper>}

      {/* Object Details Modal */}
      <ObjectDetailsModal
        object={selectedNode?.data || null}
        timeRange={timeRange}
        onClose={() => setSelectedNode(null)}
      />
    </Box>
  )
}

export default GraphVisualizationRectangular
