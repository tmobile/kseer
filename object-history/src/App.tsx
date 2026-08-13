import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { Box, AppBar, Toolbar, Typography, Paper, Divider, Chip, IconButton, Tooltip } from '@mui/material'
import { ChevronLeft, ChevronRight } from '@mui/icons-material'
import TimeControls from './components/TimeControls'
import GraphVisualizationRectangular from './components/GraphVisualizationRectangular'
import FilterPanel from './components/FilterPanel'
import GroupingConfigurator from './components/GroupingConfigurator'
import StatusBar from './components/StatusBar'
import ObjectSearch from './components/ObjectSearch'
import ObjectDetailsModal from './components/ObjectDetailsModal'
import { KubernetesObject, GraphNode, TimeRange, GroupingConfiguration, AggregateCount, RawEvent, ActivityBin, ActivityGroupCount, ACTIVITY_BIN_COUNT, CardClickFilters } from './types'
import { fetchObjectsInTimeRange, fetchAggregateCounts, fetchHistogramBins, fetchActivityWindow, fetchClusters, fetchKinds, fetchNamespaces, fetchAllEventsInTimeRange } from './api/clickhouse'
import { useLiveDruidPolling } from './hooks/useLiveDruidPolling'
import { useObjectAnimations } from './hooks/useObjectAnimations'
import { useUrlState } from './hooks/useUrlState'

const DETAIL_THRESHOLD = 250000

function App() {
  const urlState = useUrlState()
  
  // Get initial state from URL
  const initialUrlState = urlState.getInitialState()
  
  const [objects, setObjects] = useState<KubernetesObject[]>([])
  const [filteredObjects, setFilteredObjects] = useState<KubernetesObject[]>([])
  const [overviewData, setOverviewData] = useState<AggregateCount[]>([])
  const [currentTime, setCurrentTime] = useState<Date>(
    initialUrlState?.currentTime || new Date()
  )
  const [timeRange, setTimeRange] = useState<TimeRange>({
    start: initialUrlState?.startTime || new Date(Date.now() - 1 * 3600000), // 1 hour ago (reduced for large clusters)
    end: initialUrlState?.endTime || new Date(),
  })
  const [isPlaying, setIsPlaying] = useState(initialUrlState?.isPlaying || false)
  const [playbackSpeed, setPlaybackSpeed] = useState(initialUrlState?.playbackSpeed || 1)
  const [selectedClusters, setSelectedClusters] = useState<string[]>(
    initialUrlState?.clusters || []
  )
  const [selectedKinds, setSelectedKinds] = useState<string[]>(
    initialUrlState?.kinds || []
  )
  const [selectedNamespaces, setSelectedNamespaces] = useState<string[]>(
    initialUrlState?.namespaces || []
  )
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expandedCardNodes, setExpandedCardNodes] = useState<Map<string, GraphNode[]>>(new Map())
  const [loadingCardIds, setLoadingCardIds] = useState<Set<string>>(new Set())
  const [groupingConfig, setGroupingConfig] = useState<GroupingConfiguration>(
    initialUrlState?.grouping || {
      levels: [
        { field: 'cluster' },
        { field: 'namespace' },
        { field: 'kind' }
      ]
    }
  )
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [searchSelectedObject, setSearchSelectedObject] = useState<KubernetesObject | null>(null)

  // Label/annotation grouping levels become extra dimensions in the overview aggregate query.
  // groupDimsKey is a stable string used as an effect dependency so the overview only refetches
  // when these specific levels change — not when cluster/namespace/kind levels merely reorder.
  const groupDims = useMemo(
    () => groupingConfig.levels.filter(l => (l.field === 'label' || l.field === 'annotation') && !!l.value),
    [groupingConfig]
  )
  const groupDimsKey = useMemo(() => groupDims.map(l => `${l.field}:${l.value}`).join('|'), [groupDims])
  // Read inside the fetch effect via ref so reordering cluster/namespace/kind levels (which
  // doesn't change the aggregate result) doesn't trigger a costly overview refetch — only a
  // change to groupDimsKey does.
  const groupDimsRef = useRef(groupDims)
  groupDimsRef.current = groupDims
  const [nameSearch, setNameSearch] = useState('')
  const [availableClusters, setAvailableClusters] = useState<string[]>([])
  const [availableKinds, setAvailableKinds] = useState<string[]>([])
  const [availableNamespaces, setAvailableNamespaces] = useState<string[]>([])

  // Activity histogram bins (one row per time-bin, summed across groups). Small + always loaded.
  const [activityBins, setActivityBins] = useState<ActivityBin[]>([])
  // Per-group activity for the window under the playhead. Drives the overview change-outlines;
  // fetched on demand (debounced) as the playhead moves, so it never ships the full bin×group matrix.
  const [activityWindow, setActivityWindow] = useState<ActivityGroupCount[]>([])
  // Raw event stream (timestamp + uid), fetched only in detail mode (name search) to
  // pulse individual nodes as their events pass under the playhead. Detail mode is always
  // filtered, so this stays small — no need for the old whole-range "accurate timeline" fetch.
  const [allEvents, setAllEvents] = useState<RawEvent[]>([])

  // Check if we're at the current time (within 5 seconds)
  const isAtCurrentTime = Math.abs(currentTime.getTime() - Date.now()) < 5000
  
  // Sync state to URL whenever it changes
  useEffect(() => {
    urlState.updateUrl({
      clusters: selectedClusters,
      kinds: selectedKinds,
      namespaces: selectedNamespaces,
      startTime: timeRange.start,
      endTime: timeRange.end,
      currentTime: currentTime,
      isPlaying: isPlaying,
      playbackSpeed: playbackSpeed,
      grouping: groupingConfig
    })
  }, [
    selectedClusters,
    selectedKinds,
    selectedNamespaces,
    timeRange,
    currentTime,
    isPlaying,
    playbackSpeed,
    groupingConfig,
    urlState.updateUrl
  ])
  
  // Handle browser back/forward navigation
  useEffect(() => {
    const handlePopState = () => {
      const state = urlState.parseUrlState()
      
      // Update all state from URL
      if (state.clusters !== undefined) {
        setSelectedClusters(state.clusters)
      }
      if (state.kinds !== undefined) {
        setSelectedKinds(state.kinds)
      }
      if (state.namespaces !== undefined) {
        setSelectedNamespaces(state.namespaces)
      }
      if (state.startTime !== undefined && state.endTime !== undefined) {
        setTimeRange({ start: state.startTime, end: state.endTime })
      }
      if (state.currentTime !== undefined) {
        setCurrentTime(state.currentTime)
      }
      if (state.isPlaying !== undefined) {
        setIsPlaying(state.isPlaying)
      }
      if (state.playbackSpeed !== undefined) {
        setPlaybackSpeed(state.playbackSpeed)
      }
      if (state.grouping !== undefined) {
        setGroupingConfig(state.grouping)
      }
    }
    
    window.addEventListener('popstate', handlePopState)
    return () => window.removeEventListener('popstate', handlePopState)
  }, [urlState.parseUrlState])
  
  // Auto-advance time when at current time
  useEffect(() => {
    if (isAtCurrentTime && !isPlaying) {
      const interval = setInterval(() => {
        setCurrentTime(new Date())
      }, 1000)
      return () => clearInterval(interval)
    }
  }, [isAtCurrentTime, isPlaying])

  // Animation system
  const animations = useObjectAnimations()

  // Handle incoming objects from polling
  const handleNewObject = useCallback((newObject: KubernetesObject) => {
    // Only add to objects if we're at current time
    if (isAtCurrentTime) {
      setObjects(prev => {
        const existingIndex = prev.findIndex(obj => obj.uid === newObject.uid)
        let updatedObjects: KubernetesObject[]
        
        if (existingIndex >= 0) {
          // Update existing object
          updatedObjects = [...prev]
          updatedObjects[existingIndex] = newObject
          animations.triggerAnimation(newObject, 'updated')
        } else {
          // Add new object
          updatedObjects = [...prev, newObject]
          animations.triggerAnimation(newObject, 'added')
        }

        // Handle deletions
        if (newObject.operation === 'DELETED') {
          animations.triggerAnimation(newObject, 'deleted')
          // Keep deleted objects for a short time to show the animation
          setTimeout(() => {
            setObjects(current => current.filter(obj => obj.uid !== newObject.uid))
          }, 5000)
        }

        return updatedObjects
      })
    }
  }, [animations, isAtCurrentTime])

  // Live ClickHouse polling hook — pause while a modal is open to avoid
  // competing with the modal's own queries for proxy connections.
  const livePolling = useLiveDruidPolling({
    enabled: isAtCurrentTime && searchSelectedObject === null,
    filters: {
      clusters: selectedClusters,
      namespaces: selectedNamespaces,
      kinds: selectedKinds
    },
    onObjectReceived: handleNewObject
  })

  // Clear animations when moving away from current time
  useEffect(() => {
    if (!isAtCurrentTime) {
      animations.clearAllAnimations()
    }
  }, [isAtCurrentTime, animations])

  // Load filter options and data on mount
  useEffect(() => {
    const loadFilters = async () => {
      setError(null)
      try {
        // Load filter options first
        const [clusters, kinds, namespaces] = await Promise.all([
          fetchClusters(),
          fetchKinds(),
          fetchNamespaces()
        ])
        setAvailableClusters(clusters)
        setAvailableKinds(kinds)
        setAvailableNamespaces(namespaces)
        console.log(`✅ Filter options loaded: ${clusters.length} clusters, ${kinds.length} kinds, ${namespaces.length} namespaces`)
      } catch (err) {
        setError('Failed to load filter options from ClickHouse')
        console.error(err)
      }
    }
    loadFilters()
  }, [])

  // Fetch data when time range / filters change.
  // The overview counts and the binned activity are fired independently so each paints
  // as soon as it returns — the histogram can fill in before the cards (or vice versa)
  // instead of both blocking on the slower query. A cancellation guard prevents a stale
  // response from a previous range/filter from overwriting newer state.
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    const baseFilters = {
      clusters: selectedClusters.length > 0 ? selectedClusters : undefined,
      kinds: selectedKinds.length > 0 ? selectedKinds : undefined,
      namespaces: selectedNamespaces.length > 0 ? selectedNamespaces : undefined,
      nameSearch: nameSearch || undefined,
    }

    // Overview cards — primary content. Reveal the graph as soon as these arrive.
    fetchAggregateCounts(timeRange.start, timeRange.end, baseFilters, groupDimsRef.current)
      .then(counts => {
        if (cancelled) return
        console.log(`✅ Loaded aggregate counts: ${counts.length} groups`)
        setOverviewData(counts)
        setLoading(false)
        if (counts.length === 0) {
          console.warn('⚠️ No data returned. Try adjusting your filters or check that ClickHouse is reachable.')
        }
      })
      .catch(err => {
        if (cancelled) return
        setError('Failed to fetch data from ClickHouse')
        console.error(err)
        setLoading(false)
      })

    // Histogram bins — bin-only (summed across groups), tiny payload. Paints independently.
    fetchHistogramBins(timeRange.start, timeRange.end, baseFilters)
      .then(bins => {
        if (cancelled) return
        console.log(`✅ Loaded ${bins.length} histogram bins`)
        setActivityBins(bins)
      })
      .catch(err => {
        if (cancelled) return
        console.error('Failed to load histogram bins', err)
      })

    // Detail objects only in name-search mode; loads independently of the overview queries.
    if (nameSearch) {
      fetchObjectsInTimeRange(timeRange.start, timeRange.end, baseFilters)
        .then(data => {
          if (cancelled) return
          if (data.length > DETAIL_THRESHOLD) {
            console.warn(`⚠️ Search returned ${data.length}+ objects — too many for detail view, staying in overview`)
            setObjects([])
            setFilteredObjects([])
          } else {
            console.log(`✅ Loaded ${data.length} detail objects for search filters`)
            setObjects(data)
            setFilteredObjects(data)
          }
        })
        .catch(err => {
          if (cancelled) return
          console.error('Failed to load detail objects', err)
        })
    } else {
      // Not in search mode — clear any stale detail objects
      setObjects([])
      setFilteredObjects([])
    }

    return () => { cancelled = true }
  }, [timeRange, selectedClusters, selectedKinds, selectedNamespaces, nameSearch, groupDimsKey])

  // Which histogram bin the playhead currently sits in. Memoized as a plain number so the
  // window-fetch effect below only re-runs when the playhead actually crosses into a new bin —
  // not on every 1s "live" auto-advance tick. Previously the effect depended on `currentTime`
  // directly, so each tick re-ran it and its cleanup cancelled the in-flight fetch before it
  // could land, which is why outlines only appeared after scrubbing stopped.
  const activeBin = useMemo(() => {
    const start = timeRange.start.getTime()
    const rangeMs = timeRange.end.getTime() - start
    if (rangeMs <= 0) return -1
    const binMs = rangeMs / ACTIVITY_BIN_COUNT
    return Math.min(
      Math.max(Math.floor((currentTime.getTime() - start) / binMs), 0),
      ACTIVITY_BIN_COUNT - 1
    )
  }, [currentTime, timeRange])

  // Per-group activity for the bin under the playhead — drives the overview change-outlines.
  // Refetched only when the playhead crosses into a new bin (debounced), so scrubbing/playback
  // doesn't spam ClickHouse. Each query covers a single ~bin-width window, so it stays small.
  useEffect(() => {
    if (activeBin < 0) { setActivityWindow([]); return }
    const start = timeRange.start.getTime()
    const binMs = (timeRange.end.getTime() - start) / ACTIVITY_BIN_COUNT

    let cancelled = false
    const handle = setTimeout(() => {
      // Align the window to the active bin's boundaries so it matches the histogram bar under the playhead.
      const windowStart = new Date(start + activeBin * binMs)
      const windowEnd = new Date(start + (activeBin + 1) * binMs)
      const baseFilters = {
        clusters: selectedClusters.length > 0 ? selectedClusters : undefined,
        kinds: selectedKinds.length > 0 ? selectedKinds : undefined,
        namespaces: selectedNamespaces.length > 0 ? selectedNamespaces : undefined,
        nameSearch: nameSearch || undefined,
      }
      fetchActivityWindow(windowStart, windowEnd, baseFilters)
        .then(rows => { if (!cancelled) setActivityWindow(rows) })
        .catch(err => { if (!cancelled) console.error('Failed to load activity window', err) })
    }, 150)

    return () => { cancelled = true; clearTimeout(handle) }
  }, [activeBin, timeRange, selectedClusters, selectedKinds, selectedNamespaces, nameSearch])

  // Raw event stream for detail mode only (name search). Used to pulse individual nodes
  // as their events pass under the playhead. Overview uses the aggregated activity queries
  // instead, so we never pull raw events for the unfiltered whole-range view.
  useEffect(() => {
    if (!nameSearch) {
      setAllEvents([])
      return
    }
    let cancelled = false
    const load = async () => {
      try {
        const events = await fetchAllEventsInTimeRange(timeRange.start, timeRange.end, {
          clusters: selectedClusters.length > 0 ? selectedClusters : undefined,
          kinds: selectedKinds.length > 0 ? selectedKinds : undefined,
          namespaces: selectedNamespaces.length > 0 ? selectedNamespaces : undefined,
          nameSearch,
        })
        if (cancelled) return
        setAllEvents(events)
      } catch (err) {
        if (cancelled) return
        console.error('Failed to fetch events for node pulses', err)
        setAllEvents([])
      }
    }
    load()
    return () => { cancelled = true }
  }, [timeRange, selectedClusters, selectedKinds, selectedNamespaces, nameSearch])

  // Uids of events whose timestamp falls within one histogram-bin of currentTime.
  // Drives the node-pulse effect in the graph when the user scrubs or plays (detail mode).
  const eventHighlightUids = useMemo(() => {
    if (allEvents.length === 0) return new Set<string>()
    const rangeMs = timeRange.end.getTime() - timeRange.start.getTime()
    if (rangeMs <= 0) return new Set<string>()
    const binMs = rangeMs / ACTIVITY_BIN_COUNT
    const now = currentTime.getTime()
    const windowStart = now - binMs
    const uids = new Set<string>()
    for (const ev of allEvents) {
      const ts = ev.timestamp.endsWith('Z') ? ev.timestamp : ev.timestamp + 'Z'
      const t = new Date(ts).getTime()
      if (t > now) break // allEvents is ASC-sorted by ClickHouse query
      if (t >= windowStart) uids.add(ev.uid)
    }
    return uids
  }, [allEvents, currentTime, timeRange])

  // Called when user clicks a namespace card — loads objects for that card inline.
  // Toggle: clicking an already-expanded card collapses it.
  const handleCardClick = useCallback(async (
    groupId: string,
    groups: CardClickFilters
  ) => {
    if (expandedCardNodes.has(groupId)) {
      setExpandedCardNodes(prev => { const m = new Map(prev); m.delete(groupId); return m })
      return
    }
    setLoadingCardIds(prev => new Set(prev).add(groupId))
    try {
      const effectiveClusters = groups.clusters.length ? groups.clusters
        : selectedClusters.length > 0 ? selectedClusters : undefined
      const effectiveNamespaces = groups.namespaces.length ? groups.namespaces
        : selectedNamespaces.length > 0 ? selectedNamespaces : undefined
      const effectiveKinds = groups.kinds.length ? groups.kinds
        : selectedKinds.length > 0 ? selectedKinds : undefined
      const data = await fetchObjectsInTimeRange(timeRange.start, timeRange.end, {
        clusters: effectiveClusters,
        kinds: effectiveKinds,
        namespaces: effectiveNamespaces,
        labels: groups.labels.length ? groups.labels : undefined,
        annotations: groups.annotations.length ? groups.annotations : undefined,
      })
      const nodes: GraphNode[] = data
        .filter(obj => {
          if (groups.namespaces.length === 0) return true
          return groups.namespaces.some(ns =>
            ns === obj.namespace ||
            (ns === 'cluster-level' && obj.namespace === '')
          )
        })
        .map(obj => ({
          id: obj.uid,
          label: obj.name,
          kind: obj.kind,
          namespace: obj.namespace,
          cluster: obj.cluster_name,
          status: obj.operation === 'MODIFIED' ? 'modified' : 'active',
          timestamp: new Date(obj.timestamp),
          data: obj,
        }))
      setExpandedCardNodes(prev => new Map(prev).set(groupId, nodes))
    } catch (err) {
      console.error('Failed to load detail objects for card', groupId, err)
    } finally {
      setLoadingCardIds(prev => { const s = new Set(prev); s.delete(groupId); return s })
    }
  }, [expandedCardNodes, timeRange, selectedClusters, selectedKinds, selectedNamespaces])

  // Clear expanded cards whenever the time range or filters change
  useEffect(() => {
    setExpandedCardNodes(new Map())
    setLoadingCardIds(new Set())
  }, [timeRange, selectedClusters, selectedKinds, selectedNamespaces])

  // Filters are now applied in ClickHouse query (not frontend)
  // This ensures better use of 100k limit - we get 100k filtered objects, not 100k total then filtered
  // Keep objects and filteredObjects in sync since filtering happens server-side
  useEffect(() => {
    setFilteredObjects(objects)
  }, [objects])

  // Playback animation
  useEffect(() => {
    if (!isPlaying) return
    
    const interval = setInterval(() => {
      setCurrentTime(prev => {
        const next = new Date(prev.getTime() + 1000 * playbackSpeed)
        if (next >= timeRange.end) {
          setIsPlaying(false)
          return timeRange.end
        }
        return next
      })
    }, 100)
    
    return () => clearInterval(interval)
  }, [isPlaying, playbackSpeed, timeRange.end])

  return (
    <Box sx={{ flexGrow: 1, height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <AppBar position="static">
        <Toolbar>
          <Typography variant="h6" component="div" sx={{ flexGrow: 1 }}>
            Kseer - Kubernetes Object History Visualization
          </Typography>
        </Toolbar>
      </AppBar>

      <Box sx={{ display: 'flex', flex: 1, overflow: 'hidden', position: 'relative' }}>
        {/* Left Panel - Filters and Grouping */}
        <Paper sx={{ 
          width: sidebarCollapsed ? 60 : 450,
          minWidth: sidebarCollapsed ? 60 : 400, 
          maxWidth: sidebarCollapsed ? 60 : 600,
          m: 2,
          ml: 2,
          mr: sidebarCollapsed ? 0 : 2,
          overflowY: 'auto',
          overflowX: 'hidden',
          display: 'flex', 
          flexDirection: 'column',
          transition: 'all 0.3s ease-in-out',
          position: 'relative',
          flexShrink: 0,
          '&::-webkit-scrollbar': {
            width: '8px',
          },
          '&::-webkit-scrollbar-track': {
            background: '#f1f1f1',
          },
          '&::-webkit-scrollbar-thumb': {
            background: '#888',
            borderRadius: '4px',
          },
          '&::-webkit-scrollbar-thumb:hover': {
            background: '#555',
          }
        }}>
          {/* Toggle Button */}
          <Box sx={{ 
            position: 'absolute', 
            top: 8, 
            right: 8, 
            zIndex: 10 
          }}>
            <Tooltip title={sidebarCollapsed ? "Expand Sidebar" : "Collapse Sidebar"} placement="right">
              <IconButton 
                size="small"
                onClick={() => setSidebarCollapsed(!sidebarCollapsed)}
                sx={{
                  bgcolor: 'background.paper',
                  boxShadow: 1,
                  '&:hover': {
                    bgcolor: 'action.hover',
                  }
                }}
              >
                {sidebarCollapsed ? <ChevronRight /> : <ChevronLeft />}
              </IconButton>
            </Tooltip>
          </Box>

          {!sidebarCollapsed && (
            <>
              <Box sx={{ p: 2, pt: 5 }}>
                {/* Object name search */}
                <Box sx={{ mb: 2 }}>
                  <ObjectSearch
                    filters={{
                      clusters: selectedClusters.length > 0 ? selectedClusters : undefined,
                      kinds: selectedKinds.length > 0 ? selectedKinds : undefined,
                      namespaces: selectedNamespaces.length > 0 ? selectedNamespaces : undefined,
                    }}
                    onSelect={setSearchSelectedObject}
                    onShowAll={setNameSearch}
                  />
                  {nameSearch && (
                    <Chip
                      label={`Name: "${nameSearch}"`}
                      onDelete={() => setNameSearch('')}
                      size="small"
                      color="primary"
                      sx={{ mt: 0.75 }}
                    />
                  )}
                </Box>


                {/* Live polling status */}
                {isAtCurrentTime && (
                  <Box sx={{ mb: 2, display: 'flex', alignItems: 'center', gap: 1 }}>
                    <Chip
                      label={livePolling.connected ? "LIVE" : "CONNECTING"}
                      color={livePolling.connected ? "success" : "default"}
                      size="small"
                    />
                    <Typography variant="caption" color="text.secondary">
                      {livePolling.messageCount} events
                    </Typography>
                  </Box>
                )}

                <FilterPanel
                  objects={objects}
                  overviewData={overviewData}
                  selectedClusters={selectedClusters}
                  setSelectedClusters={setSelectedClusters}
                  selectedKinds={selectedKinds}
                  setSelectedKinds={setSelectedKinds}
                  selectedNamespaces={selectedNamespaces}
                  setSelectedNamespaces={setSelectedNamespaces}
                  availableClusters={availableClusters}
                  availableKinds={availableKinds}
                  availableNamespaces={availableNamespaces}
                />
              </Box>
              
              <Divider />
              
              <Box sx={{ p: 2, overflowY: 'auto', overflowX: 'hidden', flexShrink: 0 }}>
                <GroupingConfigurator
                  configuration={groupingConfig}
                  onChange={setGroupingConfig}
                />
              </Box>
            </>
          )}
        </Paper>

        {/* Main Content - Graph */}
        <Box sx={{ 
          flex: 1, 
          display: 'flex', 
          flexDirection: 'column', 
          minWidth: 0, 
          overflow: 'hidden',
          ml: sidebarCollapsed ? 2 : 0,
        }}>
          <Paper sx={{ flex: 1, m: 2, position: 'relative', overflow: 'hidden' }}>
            {filteredObjects.length === 0 && overviewData.length === 0 && !loading ? (
              <Box sx={{ 
                display: 'flex', 
                alignItems: 'center', 
                justifyContent: 'center', 
                height: '100%',
                flexDirection: 'column',
                gap: 2,
                p: 4
              }}>
                <Typography variant="h5" color="text.secondary">
                  No Objects to Display
                </Typography>
                <Typography variant="body1" color="text.secondary" textAlign="center">
                  {objects.length === 0 
                    ? 'No Kubernetes objects found — check your ClickHouse connection or adjust your filters'
                    : 'Try adjusting your filters'}
                </Typography>
              </Box>
            ) : loading ? (
              <Box sx={{ 
                display: 'flex', 
                alignItems: 'center', 
                justifyContent: 'center', 
                height: '100%',
                flexDirection: 'column',
                gap: 2
              }}>
                <Typography variant="h5" color="text.secondary">
                  Loading...
                </Typography>
              </Box>
            ) : (
              <GraphVisualizationRectangular
                objects={filteredObjects}
                currentTime={currentTime}
                groupingConfiguration={groupingConfig}
                timeRange={timeRange}
                liveMode={false}
                animations={animations}
                overviewData={overviewData}
                expandedCardNodes={expandedCardNodes}
                loadingCardIds={loadingCardIds}
                onCardClick={handleCardClick}
                forceDetail={!!nameSearch && filteredObjects.length > 0}
                eventHighlightUids={eventHighlightUids}
                activityWindow={activityWindow}
              />
            )}
          </Paper>

          {/* Bottom - Time Controls */}
          <Paper sx={{ mx: 2, mb: 2, p: 2, flexShrink: 0 }}>
            <TimeControls
              currentTime={currentTime}
              setCurrentTime={setCurrentTime}
              timeRange={timeRange}
              setTimeRange={setTimeRange}
              isPlaying={isPlaying}
              setIsPlaying={setIsPlaying}
              playbackSpeed={playbackSpeed}
              setPlaybackSpeed={setPlaybackSpeed}
              isAtCurrentTime={isAtCurrentTime}
              filteredObjects={filteredObjects}
              activityBins={activityBins}
              onTimeChange={(time) => {
                setCurrentTime(time)
                // Trigger animations for timeline playback
                const eventsAtTime = objects.filter(obj => 
                  new Date(obj.timestamp).getTime() === time.getTime()
                )
                eventsAtTime.forEach(obj => {
                  animations.triggerAnimation(obj, obj.operation === 'ADDED' ? 'added' : 
                                                  obj.operation === 'DELETED' ? 'deleted' : 'updated')
                })
              }}
            />
          </Paper>
        </Box>
      </Box>

      <StatusBar
        loading={loading}
        error={error}
        objectCount={filteredObjects.length}
        totalCount={objects.length}
      />

      {/* Modal opened by search results */}
      <ObjectDetailsModal
        object={searchSelectedObject}
        timeRange={timeRange}
        onClose={() => setSearchSelectedObject(null)}
      />
    </Box>
  )
}

export default App
