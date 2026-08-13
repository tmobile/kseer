import React, { useMemo } from 'react'
import {
  Box,
  Typography,
  Button,
  Divider,
  Stack,
} from '@mui/material'
import { FilterList, Clear } from '@mui/icons-material'
import { KubernetesObject, AggregateCount } from '../types'
import FilterSelect from './FilterSelect'

interface FilterPanelProps {
  objects: KubernetesObject[]
  selectedClusters: string[]
  setSelectedClusters: (clusters: string[]) => void
  selectedKinds: string[]
  setSelectedKinds: (kinds: string[]) => void
  selectedNamespaces: string[]
  setSelectedNamespaces: (namespaces: string[]) => void
  availableClusters?: string[]
  availableKinds?: string[]
  availableNamespaces?: string[]
  overviewData?: AggregateCount[]
}

const FilterPanel = React.memo(function FilterPanel({
  objects,
  selectedClusters,
  setSelectedClusters,
  selectedKinds,
  setSelectedKinds,
  selectedNamespaces,
  setSelectedNamespaces,
  availableClusters: providedClusters,
  availableKinds: providedKinds,
  availableNamespaces: providedNamespaces,
  overviewData,
}: FilterPanelProps) {
  // Use provided filter options or extract from objects (backwards compatible)
  const availableClusters = useMemo(() => {
    if (providedClusters && providedClusters.length > 0) {
      return providedClusters
    }
    const clusters = new Set<string>()
    objects.forEach(obj => clusters.add(obj.cluster_name))
    return Array.from(clusters).sort()
  }, [providedClusters, objects])

  const availableKinds = useMemo(() => {
    if (providedKinds && providedKinds.length > 0) {
      return providedKinds
    }
    const kinds = new Set<string>()
    objects.forEach(obj => kinds.add(obj.kind))
    return Array.from(kinds).sort()
  }, [providedKinds, objects])

  const availableNamespaces = useMemo(() => {
    if (providedNamespaces && providedNamespaces.length > 0) {
      return providedNamespaces
    }
    const namespaces = new Set<string>()
    objects.forEach(obj => {
      if (obj.namespace) {
        namespaces.add(obj.namespace)
      }
    })
    return Array.from(namespaces).sort()
  }, [providedNamespaces, objects])

  const kindCounts = useMemo(() => {
    const map = new Map<string, number>()
    if (overviewData) {
      for (const row of overviewData) {
        map.set(row.kind, (map.get(row.kind) ?? 0) + row.total)
      }
    }
    return map
  }, [overviewData])

  const namespaceCounts = useMemo(() => {
    const map = new Map<string, number>()
    if (overviewData) {
      for (const row of overviewData) {
        if (row.namespace) {
          map.set(row.namespace, (map.get(row.namespace) ?? 0) + row.total)
        }
      }
    }
    return map
  }, [overviewData])

  const totalObjects = useMemo(() => {
    if (overviewData && overviewData.length > 0) {
      return overviewData.reduce((sum, row) => sum + row.total, 0)
    }
    return objects.length
  }, [overviewData, objects.length])

  const clearAllFilters = () => {
    setSelectedClusters([])
    setSelectedKinds([])
    setSelectedNamespaces([])
  }

  const hasActiveFilters = 
    selectedClusters.length > 0 || 
    selectedKinds.length > 0 || 
    selectedNamespaces.length > 0

  return (
    <Box>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2 }}>
        <FilterList sx={{ mr: 1 }} />
        <Typography variant="h6" sx={{ flexGrow: 1 }}>Filters</Typography>
        {hasActiveFilters && (
          <Button
            size="small"
            startIcon={<Clear />}
            onClick={clearAllFilters}
          >
            Clear
          </Button>
        )}
      </Box>

      <Divider sx={{ mb: 2 }} />

      <Stack spacing={1.5}>
        <FilterSelect
          label="Clusters"
          options={availableClusters}
          selected={selectedClusters}
          onChange={setSelectedClusters}
        />
        <FilterSelect
          label="Object Types"
          options={availableKinds}
          selected={selectedKinds}
          onChange={setSelectedKinds}
          counts={kindCounts}
        />
        <FilterSelect
          label="Namespaces"
          options={availableNamespaces}
          selected={selectedNamespaces}
          onChange={setSelectedNamespaces}
          counts={namespaceCounts}
        />
      </Stack>

      {/* Statistics */}
      <Box sx={{ mt: 3, p: 2, bgcolor: 'background.paper', borderRadius: 1 }}>
        <Typography variant="subtitle2" gutterBottom>Statistics</Typography>
        <Typography variant="body2" color="text.secondary">
          Total Objects: {totalObjects.toLocaleString()}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          Clusters: {availableClusters.length}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          Object Types: {availableKinds.length}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          Namespaces: {availableNamespaces.length}
        </Typography>
      </Box>
    </Box>
  )
})

export default FilterPanel
