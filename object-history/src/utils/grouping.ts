import { KubernetesObject, GraphNode, GroupByConfig, GroupingConfiguration } from '../types'

/**
 * Extract the value for a given grouping field from a Kubernetes object or graph node
 */
export function extractGroupValue(
  item: KubernetesObject | GraphNode, 
  config: GroupByConfig
): string {
  const obj = 'data' in item ? item.data : item

  switch (config.field) {
    case 'cluster':
      return obj.cluster_name || 'unknown-cluster'
    
    case 'namespace':
      return obj.namespace || 'cluster-level'
    
    case 'kind':
      return obj.kind || 'unknown-kind'
    
    case 'label':
      if (!config.value) return 'no-label-key'
      return obj.labels?.[config.value] || `no-${config.value}`
    
    case 'annotation':
      if (!config.value) return 'no-annotation-key'
      return obj.annotations?.[config.value] || `no-${config.value}`
    
    default:
      return 'unknown'
  }
}

/**
 * Generate a unique ID for a group based on its path
 */
export function generateGroupId(path: string[]): string {
  return path.join('/')
}

/**
 * Get the display label for a group
 */
export function getGroupLabel(config: GroupByConfig, value: string): string {
  // Use custom label if provided (for backwards compatibility)
  if (config.label) {
    return `${config.label}: ${value}`
  }

  // Default labels based on field type - more concise
  switch (config.field) {
    case 'cluster':
      return value
    case 'namespace':
      return value === 'cluster-level' ? 'Cluster-Level' : value
    case 'kind':
      return value
    case 'label':
      // For labels, show key=value format
      return config.value ? `${config.value}=${value}` : value
    case 'annotation':
      // For annotations, show key=value format
      return config.value ? `${config.value}=${value}` : value
    default:
      return value
  }
}

/**
 * Build a hierarchical grouping structure from nodes and configuration
 */
export interface HierarchicalGroup {
  id: string
  label: string
  value: string
  level: number
  configIndex: number
  children: Map<string, HierarchicalGroup>
  nodes: GraphNode[]
  path: string[]
}

export function buildHierarchicalGroups(
  nodes: GraphNode[], 
  configuration: GroupingConfiguration
): HierarchicalGroup {
  const root: HierarchicalGroup = {
    id: 'root',
    label: 'All Objects',
    value: 'root',
    level: -1,
    configIndex: -1,
    children: new Map(),
    nodes: [],
    path: []
  }

  // Group nodes according to configuration
  nodes.forEach(node => {
    let currentGroup = root
    const path: string[] = []

    configuration.levels.forEach((config, configIndex) => {
      const value = extractGroupValue(node, config)
      path.push(value)

      if (!currentGroup.children.has(value)) {
        currentGroup.children.set(value, {
          id: generateGroupId(path),
          label: getGroupLabel(config, value),
          value,
          level: configIndex,
          configIndex,
          children: new Map(),
          nodes: [],
          path: [...path]
        })
      }

      currentGroup = currentGroup.children.get(value)!
    })

    // Add node to the leaf group
    currentGroup.nodes.push(node)
  })

  return root
}

/**
 * Calculate the radius needed for a group based on its contents
 */
export function calculateGroupRadius(
  group: HierarchicalGroup,
  nodeSize: number,
  nodeSpacing: number,
  groupPadding: number
): number {
  // If this group has nodes, calculate based on node layout
  if (group.nodes.length > 0) {
    const nodeCount = group.nodes.length
    
    if (nodeCount === 1) {
      return nodeSize + groupPadding / 3  // Reduced padding
    } else if (nodeCount <= 6) {
      // Circle layout
      const layoutRadius = nodeSize + nodeSpacing
      return layoutRadius + nodeSize + groupPadding / 3  // Reduced padding
    } else {
      // Grid layout
      const cols = Math.ceil(Math.sqrt(nodeCount))
      const rows = Math.ceil(nodeCount / cols)
      const width = cols * (nodeSize * 2 + nodeSpacing)
      const height = rows * (nodeSize * 2 + nodeSpacing)
      return Math.sqrt(width * width + height * height) / 2 + groupPadding / 3  // Reduced padding
    }
  }

  // If this group has child groups, calculate based on child layout
  if (group.children.size > 0) {
    const childRadii: number[] = []
    group.children.forEach(child => {
      const radius = calculateGroupRadius(child, nodeSize, nodeSpacing, groupPadding)
      childRadii.push(radius)
    })

    const maxRadius = Math.max(...childRadii)
    const childCount = childRadii.length

    if (childCount === 1) {
      return maxRadius + groupPadding / 3  // Reduced padding
    } else {
      // Arrange children in a circle - use tighter formula
      const angleStep = (2 * Math.PI) / childCount
      const requiredRadius = (maxRadius / Math.sin(angleStep / 2)) * 0.7  // Tighter packing
      return requiredRadius + maxRadius + groupPadding / 3  // Reduced padding
    }
  }

  // Empty group
  return groupPadding / 2  // Reduced padding
}

/**
 * Flatten hierarchical groups into a list of circle groups for visualization
 */
export interface FlatCircleGroup {
  id: string
  x: number
  y: number
  r: number
  label: string
  level: number
  parent?: string
  nodes?: GraphNode[]
}

export function flattenGroups(
  group: HierarchicalGroup,
  x: number = 0,
  y: number = 0,
  nodeSize: number = 30,
  nodeSpacing: number = 10,
  levelPaddings: number[] = [60, 40, 30, 20]
): FlatCircleGroup[] {
  const circles: FlatCircleGroup[] = []
  
  if (group.level === -1) {
    // Root group - process children
    group.children.forEach(child => {
      const childCircles = flattenGroups(child, x, y, nodeSize, nodeSpacing, levelPaddings)
      circles.push(...childCircles)
    })
    return circles
  }

  const padding = levelPaddings[Math.min(group.level, levelPaddings.length - 1)]
  const radius = calculateGroupRadius(group, nodeSize, nodeSpacing, padding)

  // Add this group's circle
  circles.push({
    id: group.id,
    x,
    y,
    r: radius,
    label: group.label,
    level: group.level,
    nodes: group.nodes.length > 0 ? group.nodes : undefined
  })

  // Position and add child groups
  if (group.children.size > 0) {
    const childArray = Array.from(group.children.values())
    
    if (childArray.length === 1) {
      // Single child - center it
      const childCircles = flattenGroups(childArray[0], x, y, nodeSize, nodeSpacing, levelPaddings)
      circles.push(...childCircles)
    } else {
      // Multiple children - arrange in circle
      const angleStep = (2 * Math.PI) / childArray.length
      const childRadii = childArray.map(child => 
        calculateGroupRadius(child, nodeSize, nodeSpacing, padding)
      )
      const maxRadius = Math.max(...childRadii)
      // Reduce layout radius for tighter packing (multiply by 0.7 for 30% reduction)
      const layoutRadius = (maxRadius / Math.sin(angleStep / 2)) * 0.7

      childArray.forEach((child, i) => {
        const angle = i * angleStep - Math.PI / 2
        const childX = x + layoutRadius * Math.cos(angle)
        const childY = y + layoutRadius * Math.sin(angle)
        const childCircles = flattenGroups(child, childX, childY, nodeSize, nodeSpacing, levelPaddings)
        circles.push(...childCircles)
      })
    }
  }

  return circles
}
