import React, { useEffect, useRef, useState } from 'react'
import * as d3 from 'd3'
import { Box, Paper, Typography, Chip, IconButton, Tooltip } from '@mui/material'
import { ZoomIn, ZoomOut, CenterFocusStrong } from '@mui/icons-material'
import { KubernetesObject, GraphNode, GraphLink, GraphData, TimeRange } from '../types'
import ObjectDetailsModal from './ObjectDetailsModal'

interface GraphVisualizationProps {
  objects: KubernetesObject[]
  currentTime: Date
  timeRange: TimeRange
}

const kindColors: Record<string, string> = {
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

function GraphVisualization({ objects, currentTime, timeRange }: GraphVisualizationProps) {
  const svgRef = useRef<SVGSVGElement>(null)
  const [selectedNode, setSelectedNode] = useState<GraphNode | null>(null)
  const [dimensions, setDimensions] = useState({ width: 800, height: 600 })

  // Convert objects to graph data
  const graphData = React.useMemo((): GraphData => {
    const nodeMap = new Map<string, GraphNode>()
    const links: GraphLink[] = []

    // Create nodes - use UID as primary identifier
    objects.forEach(obj => {
      const nodeId = obj.uid
      const existingNode = nodeMap.get(nodeId)
      
      // Update node if this is a newer version (same UID, newer timestamp)
      if (!existingNode || new Date(obj.timestamp) > existingNode.timestamp) {
        nodeMap.set(nodeId, {
          id: nodeId,
          label: obj.name,
          kind: obj.kind,
          namespace: obj.namespace,
          cluster: obj.cluster_name,
          status: obj.operation === 'DELETED' ? 'deleted' : 
                  obj.operation === 'MODIFIED' ? 'modified' : 'active',
          timestamp: new Date(obj.timestamp),
          data: obj,
        })
      }
    })

    // Create links based on owner references - match by UID
    nodeMap.forEach(node => {
      if (node.data.owner_references) {
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

      // Create links for Services to Pods (based on selectors)
      if (node.kind === 'Service' && node.data.object?.spec?.selector) {
        const selector = node.data.object.spec.selector
        nodeMap.forEach(potentialPod => {
          if (potentialPod.kind === 'Pod' && 
              potentialPod.namespace === node.namespace &&
              potentialPod.cluster === node.cluster &&
              matchesSelector(potentialPod.data.labels, selector)) {
            links.push({
              source: node.id,
              target: potentialPod.id,
              type: 'service',
              label: 'selects',
            })
          }
        })
      }
    })

    return {
      nodes: Array.from(nodeMap.values()),
      links,
    }
  }, [objects, currentTime])

  // Update dimensions on resize
  useEffect(() => {
    const handleResize = () => {
      if (svgRef.current) {
        const { width, height } = svgRef.current.getBoundingClientRect()
        setDimensions({ width, height })
      }
    }
    handleResize()
    window.addEventListener('resize', handleResize)
    return () => window.removeEventListener('resize', handleResize)
  }, [])

  // D3 visualization
  useEffect(() => {
    if (!svgRef.current || !graphData.nodes.length) return

    const svg = d3.select(svgRef.current)
    svg.selectAll('*').remove()

    const { width, height } = dimensions

    // Create zoom behavior
    const zoom = d3.zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.1, 10])
      .on('zoom', (event) => {
        container.attr('transform', event.transform.toString())
      })

    svg.call(zoom)

    const container = svg.append('g')

    // Create force simulation
    const simulation = d3.forceSimulation(graphData.nodes as any)
      .force('link', d3.forceLink(graphData.links as any)
        .id((d: any) => d.id)
        .distance(100))
      .force('charge', d3.forceManyBody().strength(-300))
      .force('center', d3.forceCenter(width / 2, height / 2))
      .force('collision', d3.forceCollide().radius(30))

    // Create links
    const link = container.append('g')
      .selectAll('line')
      .data(graphData.links)
      .enter().append('line')
      .attr('stroke', '#999')
      .attr('stroke-opacity', 0.6)
      .attr('stroke-width', 2)
      .attr('stroke-dasharray', (d) => d.type === 'owner' ? '0' : '5,5')

    // Create nodes
    const node = container.append('g')
      .selectAll('g')
      .data(graphData.nodes)
      .enter().append('g')
      .call(d3.drag<any, any>()
        .on('start', dragstarted)
        .on('drag', dragged)
        .on('end', dragended) as any)

    // Add circles for nodes
    node.append('circle')
      .attr('r', (d) => {
        // Larger nodes for certain kinds
        if (['Namespace', 'Node'].includes(d.kind)) return 25
        if (['Deployment', 'StatefulSet', 'DaemonSet'].includes(d.kind)) return 20
        return 15
      })
      .attr('fill', (d) => kindColors[d.kind] || '#666')
      .attr('stroke', (d) => d.status === 'deleted' ? '#f44336' : '#fff')
      .attr('stroke-width', (d) => d.status === 'deleted' ? 3 : 2)
      .attr('opacity', (d) => d.status === 'deleted' ? 0.5 : 1)
      .on('click', (_event, d) => {
        setSelectedNode(d)
      })

    // Add icons or text
    node.append('text')
      .text((d) => d.kind[0])
      .attr('text-anchor', 'middle')
      .attr('dominant-baseline', 'middle')
      .attr('fill', 'white')
      .attr('font-size', '12px')
      .attr('font-weight', 'bold')

    // Add labels
    node.append('text')
      .text((d) => d.label)
      .attr('x', 0)
      .attr('y', 30)
      .attr('text-anchor', 'middle')
      .attr('font-size', '10px')
      .attr('fill', '#ccc')

    // Add tooltips
    node.append('title')
      .text((d) => `${d.kind}: ${d.label}\nNamespace: ${d.namespace}\nCluster: ${d.cluster}`)

    // Update positions on tick
    simulation.on('tick', () => {
      link
        .attr('x1', (d: any) => d.source.x)
        .attr('y1', (d: any) => d.source.y)
        .attr('x2', (d: any) => d.target.x)
        .attr('y2', (d: any) => d.target.y)

      node.attr('transform', (d: any) => `translate(${d.x},${d.y})`)
    })

    // Drag functions
    function dragstarted(event: any, d: any) {
      if (!event.active) simulation.alphaTarget(0.3).restart()
      d.fx = d.x
      d.fy = d.y
    }

    function dragged(event: any, d: any) {
      d.fx = event.x
      d.fy = event.y
    }

    function dragended(event: any, d: any) {
      if (!event.active) simulation.alphaTarget(0)
      d.fx = null
      d.fy = null
    }

    // Zoom controls
    const zoomIn = () => {
      svg.transition().call(zoom.scaleBy as any, 1.3)
    }

    const zoomOut = () => {
      svg.transition().call(zoom.scaleBy as any, 0.7)
    }

    const resetZoom = () => {
      svg.transition().call(zoom.transform as any, d3.zoomIdentity)
    }

    // Store zoom functions for external controls
    ;(window as any).graphZoomIn = zoomIn
    ;(window as any).graphZoomOut = zoomOut
    ;(window as any).graphResetZoom = resetZoom

    return () => {
      simulation.stop()
    }
  }, [graphData, dimensions])

  return (
    <Box sx={{ position: 'relative', width: '100%', height: '100%' }}>
      <svg
        ref={svgRef}
        style={{ width: '100%', height: '100%', cursor: 'grab' }}
      />
      
      {/* Zoom Controls */}
      <Box sx={{ position: 'absolute', top: 10, right: 10 }}>
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
          <Tooltip title="Reset Zoom">
            <IconButton size="small" onClick={() => (window as any).graphResetZoom?.()}>
              <CenterFocusStrong />
            </IconButton>
          </Tooltip>
        </Paper>
      </Box>

      {/* Selected Node Details */}
      {selectedNode && (
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

      {/* Legend */}
      <Paper sx={{ position: 'absolute', top: 10, left: 10, p: 2 }}>
        <Typography variant="subtitle2" gutterBottom>Object Types</Typography>
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, maxWidth: 200 }}>
          {Object.entries(kindColors).slice(0, 8).map(([kind, color]) => (
            <Chip
              key={kind}
              label={kind}
              size="small"
              sx={{ 
                backgroundColor: color, 
                color: 'white',
                fontSize: '0.7rem',
              }}
            />
          ))}
        </Box>
      </Paper>

      {/* Object Details Modal */}
      <ObjectDetailsModal
        object={selectedNode?.data || null}
        timeRange={timeRange}
        onClose={() => setSelectedNode(null)}
      />
    </Box>
  )
}

// Helper function to check if labels match selector
function matchesSelector(labels: Record<string, string>, selector: Record<string, string>): boolean {
  if (!labels || !selector) return false
  return Object.entries(selector).every(([key, value]) => labels[key] === value)
}

export default GraphVisualization
