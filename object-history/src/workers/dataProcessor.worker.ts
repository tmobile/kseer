/**
 * Web Worker for processing large datasets of Kubernetes objects
 * Offloads heavy computation from main thread to keep UI responsive
 * 
 * Handles:
 * - Converting KubernetesObject[] to GraphData (nodes + links)
 * - Building hierarchical grouping structures
 * - Incremental updates for live polling
 */

import type { 
  KubernetesObject, 
  GraphNode, 
  GraphLink, 
  GraphData,
  GroupingConfiguration 
} from '../types'

interface ProcessObjectsMessage {
  type: 'processObjects'
  objects: KubernetesObject[]
  groupingConfig: GroupingConfiguration
}

interface UpdateWithNewObjectMessage {
  type: 'updateWithNewObject'
  object: KubernetesObject
}

interface RebuildHierarchyMessage {
  type: 'rebuildHierarchy'
  groupingConfig: GroupingConfiguration
}

type WorkerMessage = ProcessObjectsMessage | UpdateWithNewObjectMessage | RebuildHierarchyMessage

interface GroupData {
  id: string
  name: string
  value?: number
  children?: GroupData[]
  nodes?: GraphNode[]
}

// Worker state
let currentGraphData: GraphData = { nodes: [], links: [] }
let currentGroupingConfig: GroupingConfiguration | null = null
let currentHierarchy: GroupData | null = null

/**
 * Convert array of KubernetesObject to GraphData (nodes + links)
 */
function buildGraphData(objects: KubernetesObject[]): GraphData {
  const nodeMap = new Map<string, GraphNode>()
  const links: GraphLink[] = []

  // Create nodes - use UID as primary identifier
  objects.forEach(obj => {
    const nodeId = obj.uid
    const existingNode = nodeMap.get(nodeId)
    
    // ClickHouse returns timestamps as strings in format "YYYY-MM-DD HH:MM:SS.mmm"
    // Append 'Z' to treat as UTC, or it will be parsed as local time
    const timestampStr = obj.timestamp.endsWith('Z') ? obj.timestamp : obj.timestamp + 'Z'
    const objTimestamp = new Date(timestampStr)
    
    // Update node if this is a newer version (same UID, newer timestamp)
    if (!existingNode || objTimestamp > existingNode.timestamp) {
      nodeMap.set(nodeId, {
        id: nodeId,
        label: obj.name,
        kind: obj.kind,
        namespace: obj.namespace || 'cluster-level',
        cluster: obj.cluster_name,
        status: obj.operation === 'DELETED' ? 'deleted' : 
                obj.operation === 'MODIFIED' ? 'modified' : 'active',
        timestamp: objTimestamp,
        data: obj,
      })
    }
  })

  // Create links from owner references
  nodeMap.forEach(node => {
    if (node.data.owner_references && Array.isArray(node.data.owner_references)) {
      node.data.owner_references.forEach(owner => {
        const ownerId = owner.uid
        if (nodeMap.has(ownerId)) {
          links.push({
            source: ownerId,
            target: node.id,
            type: 'owner',
            label: 'owns',
          })
        }
      })
    }
  })

  return {
    nodes: Array.from(nodeMap.values()),
    links,
  }
}

/**
 * Build hierarchical grouping structure from graph data
 */
function buildHierarchy(graphData: GraphData, groupingConfig: GroupingConfiguration): GroupData {
  const root: GroupData = {
    id: 'root',
    name: 'All Resources',
    children: []
  }

  const groups = new Map<string, GroupData>()
  
  graphData.nodes.forEach(node => {
    let currentLevel = root
    let pathKey = ''
    
    // Navigate through grouping levels
    groupingConfig.levels.forEach((level, index) => {
      let groupValue: string = ''
      
      switch (level.field) {
        case 'cluster':
          groupValue = node.cluster
          break
        case 'namespace':
          groupValue = node.namespace
          break
        case 'kind':
          groupValue = node.kind
          break
        case 'label':
          if (level.value && node.data.labels) {
            groupValue = node.data.labels[level.value] || 'unlabeled'
          }
          break
        case 'annotation':
          if (level.value && node.data.annotations) {
            groupValue = node.data.annotations[level.value] || 'unannotated'
          }
          break
      }
      
      pathKey = pathKey ? `${pathKey}/${groupValue}` : groupValue
      
      if (!groups.has(pathKey)) {
        const newGroup: GroupData = {
          id: pathKey,
          name: groupValue,
          value: 0,
          children: index < groupingConfig.levels.length - 1 ? [] : undefined,
          nodes: index === groupingConfig.levels.length - 1 ? [] : undefined
        }
        
        groups.set(pathKey, newGroup)
        
        if (!currentLevel.children) {
          currentLevel.children = []
        }
        currentLevel.children.push(newGroup)
      }
      
      currentLevel = groups.get(pathKey)!
    })
    
    // Add node to the final level
    if (currentLevel.nodes) {
      currentLevel.nodes.push(node)
    }
    
    // Update value for sizing
    let updateLevel: GroupData | null = currentLevel
    while (updateLevel) {
      if (updateLevel.value !== undefined) {
        updateLevel.value += 1
      }
      const parentKey = updateLevel.id.split('/').slice(0, -1).join('/')
      updateLevel = parentKey ? groups.get(parentKey) || null : null
    }
  })

  return root
}

/**
 * Incrementally update graph data with a new object
 */
function updateGraphDataWithObject(graphData: GraphData, newObject: KubernetesObject): GraphData {
  const nodeMap = new Map<string, GraphNode>()
  graphData.nodes.forEach(node => nodeMap.set(node.id, node))

  const nodeId = newObject.uid
  const existingNode = nodeMap.get(nodeId)
  
  // Update or add node
  if (!existingNode || new Date(newObject.timestamp) > existingNode.timestamp) {
    nodeMap.set(nodeId, {
      id: nodeId,
      label: newObject.name,
      kind: newObject.kind,
      namespace: newObject.namespace || 'cluster-level',
      cluster: newObject.cluster_name,
      status: newObject.operation === 'DELETED' ? 'deleted' : 
              newObject.operation === 'MODIFIED' ? 'modified' : 'active',
      timestamp: new Date(newObject.timestamp),
      data: newObject,
    })
  }

  // Rebuild links (simple approach - could be optimized for incremental)
  const links: GraphLink[] = []
  nodeMap.forEach(node => {
    if (node.data.owner_references && Array.isArray(node.data.owner_references)) {
      node.data.owner_references.forEach(owner => {
        const ownerId = owner.uid
        if (nodeMap.has(ownerId)) {
          links.push({
            source: ownerId,
            target: node.id,
            type: 'owner',
            label: 'owns',
          })
        }
      })
    }
  })

  return {
    nodes: Array.from(nodeMap.values()),
    links,
  }
}

/**
 * Handle incoming messages from main thread
 */
self.onmessage = (event: MessageEvent<WorkerMessage>) => {
  const message = event.data

  try {
    switch (message.type) {
      case 'processObjects': {
        // Initial processing of object array
        console.log(`[Worker] Processing ${message.objects.length} objects...`)
        const startTime = performance.now()
        
        currentGraphData = buildGraphData(message.objects)
        currentGroupingConfig = message.groupingConfig
        currentHierarchy = buildHierarchy(currentGraphData, message.groupingConfig)
        
        const duration = performance.now() - startTime
        console.log(`[Worker] Processed in ${duration.toFixed(2)}ms: ${currentGraphData.nodes.length} nodes, ${currentGraphData.links.length} links`)
        
        self.postMessage({
          type: 'processComplete',
          graphData: currentGraphData,
          hierarchy: currentHierarchy,
          processingTime: duration
        })
        break
      }

      case 'updateWithNewObject': {
        // Incremental update for live polling
        if (!currentGroupingConfig) {
          console.warn('[Worker] Cannot update - no grouping config set')
          return
        }

        currentGraphData = updateGraphDataWithObject(currentGraphData, message.object)
        currentHierarchy = buildHierarchy(currentGraphData, currentGroupingConfig)
        
        self.postMessage({
          type: 'updateComplete',
          graphData: currentGraphData,
          hierarchy: currentHierarchy,
          updatedObject: message.object
        })
        break
      }

      case 'rebuildHierarchy': {
        // Rebuild hierarchy when grouping config changes
        console.log('[Worker] Rebuilding hierarchy with new grouping config...')
        const startTime = performance.now()
        
        currentGroupingConfig = message.groupingConfig
        currentHierarchy = buildHierarchy(currentGraphData, message.groupingConfig)
        
        const duration = performance.now() - startTime
        console.log(`[Worker] Rebuilt hierarchy in ${duration.toFixed(2)}ms`)
        
        self.postMessage({
          type: 'rebuildComplete',
          hierarchy: currentHierarchy,
          processingTime: duration
        })
        break
      }

      default:
        console.warn('[Worker] Unknown message type:', (message as any).type)
    }
  } catch (error) {
    console.error('[Worker] Error processing message:', error)
    self.postMessage({
      type: 'error',
      error: error instanceof Error ? error.message : 'Unknown error'
    })
  }
}

// Export empty object for TypeScript module resolution
export {}

