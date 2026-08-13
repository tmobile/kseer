import { useState, useEffect, useRef, useCallback } from 'react'
import { KubernetesObject } from '../types'
import { executeClickHouseQuery } from '../api/clickhouse'

// Helper to get the full table reference
function getTableReference(): string {
  // Use runtime config (production) or Vite env vars (local dev)
  const database = window.env?.CLICKHOUSE_DATABASE || import.meta.env.VITE_CLICKHOUSE_DATABASE || 'kseer'
  const table = window.env?.CLICKHOUSE_TABLE || import.meta.env.VITE_CLICKHOUSE_TABLE || 'kubernetes_events'
  return `${database}.${table}`
}

// ClickHouse returns naive timestamp strings in UTC (e.g. "2026-06-18 23:45:56.321")
// with no timezone designator. `new Date(str)` would parse that as LOCAL time, which then
// gets re-rendered as UTC by formatDate — shifting the lower bound by the local offset and
// inverting the WHERE range (lower bound > upper bound → zero rows). Parse it as UTC instead.
function parseClickHouseTimestamp(ts: string): Date {
  return new Date(ts.replace(' ', 'T') + 'Z')
}

interface UseLiveDruidPollingOptions {
  enabled: boolean
  pollInterval?: number // milliseconds
  filters: {
    clusters?: string[]
    namespaces?: string[]
    kinds?: string[]
    customSQL?: string // For advanced queries
  }
  onObjectReceived?: (object: KubernetesObject) => void
  onError?: (error: string) => void
}

interface LivePollingState {
  connected: boolean // For compatibility with old UI
  error: string | null
  lastMessage: Date | null
  messageCount: number
  lastQueryTime: Date
  isPolling: boolean
}

export function useLiveDruidPolling({
  enabled,
  pollInterval = 60000,
  filters,
  onObjectReceived,
  onError
}: UseLiveDruidPollingOptions) {
  const [state, setState] = useState<LivePollingState>({
    connected: false,
    error: null,
    lastMessage: null,
    messageCount: 0,
    lastQueryTime: new Date(Date.now() - 10000), // Start 10 seconds ago
    isPolling: false
  })

  const pollTimeoutRef = useRef<NodeJS.Timeout | null>(null)
  const isPollingRef = useRef(false)
  const seenUidsRef = useRef<Set<string>>(new Set())
  // Use ref for lastQueryTime to avoid recreating executePoll callback
  const lastQueryTimeRef = useRef<Date>(new Date(Date.now() - 10000))

  // Build ClickHouse query based on filters
  const buildQuery = useCallback((lastQueryTime: Date) => {
    const formatDate = (date: Date) => {
      const year = date.getUTCFullYear()
      const month = String(date.getUTCMonth() + 1).padStart(2, '0')
      const day = String(date.getUTCDate()).padStart(2, '0')
      const hours = String(date.getUTCHours()).padStart(2, '0')
      const minutes = String(date.getUTCMinutes()).padStart(2, '0')
      const seconds = String(date.getUTCSeconds()).padStart(2, '0')
      const ms = String(date.getUTCMilliseconds()).padStart(3, '0')
      return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}.${ms}`
    }

    // If user provided custom SQL, use it
    if (filters.customSQL) {
      return `
        WITH user_query AS (
          ${filters.customSQL}
        )
        SELECT * FROM user_query
        WHERE timestamp > '${formatDate(lastQueryTime)}'
        ORDER BY timestamp ASC
        LIMIT 1000
      `
    }

    // Build standard query with filters
    let whereConditions = [
      `timestamp > '${formatDate(lastQueryTime)}'`,
      `timestamp <= '${formatDate(new Date())}'`
    ]

    if (filters.clusters && filters.clusters.length > 0) {
      const clusterList = filters.clusters.map(c => `'${c.replace(/'/g, "''")}'`).join(',')
      whereConditions.push(`cluster_name IN (${clusterList})`)
    }

    if (filters.namespaces && filters.namespaces.length > 0) {
      // Strict namespace match — don't pull in cluster-scoped (empty namespace) objects.
      const real = filters.namespaces.filter(n => n !== 'cluster-level')
      const includeClusterLevel = filters.namespaces.includes('cluster-level')
      const parts: string[] = []
      if (real.length > 0) {
        const namespaceList = real.map(n => `'${n.replace(/'/g, "''")}'`).join(',')
        parts.push(`namespace IN (${namespaceList})`)
      }
      if (includeClusterLevel) parts.push(`namespace = ''`)
      if (parts.length > 0) {
        whereConditions.push(parts.length > 1 ? `(${parts.join(' OR ')})` : parts[0])
      }
    }

    if (filters.kinds && filters.kinds.length > 0) {
      const kindList = filters.kinds.map(k => `'${k.replace(/'/g, "''")}'`).join(',')
      whereConditions.push(`kind IN (${kindList})`)
    }

    // Optimized query: exclude 'object' field and JSON extraction for performance
    // JSONExtractString is very slow (13x slowdown), so skip it for live polling
    return `
      SELECT 
        timestamp,
        cluster_name,
        namespace,
        object.metadata.name::String as name,
        kind,
        uid,
        operation
      FROM ${getTableReference()}
      WHERE ${whereConditions.join(' AND ')}
      ORDER BY timestamp ASC
      LIMIT 1000
    `
  }, [filters])

  // Transform ClickHouse results to KubernetesObject format
  // Note: Only essential fields are fetched for performance (JSONExtractString is very slow)
  // Use fetchObjectDetails() for full object spec including labels/annotations/owner_references
  const transformResults = useCallback((results: any[]): KubernetesObject[] => {
    return results.map(row => ({
      timestamp: row.timestamp,
      cluster_name: row.cluster_name,
      namespace: row.namespace,
      name: row.name,
      kind: row.kind,
      api_version: '', // Not fetched in lightweight query
      uid: row.uid,
      resource_version: '', // Not fetched in lightweight query
      generation: 0, // Not fetched in lightweight query
      operation: row.operation,
      object: {}, // Not fetched in lightweight query
      labels: {}, // Not fetched for performance - use fetchObjectDetails()
      annotations: {}, // Not fetched for performance - use fetchObjectDetails()
      owner_references: [] // Not fetched for performance - use fetchObjectDetails()
    }))
  }, [])

  // Single poll execution
  const executePoll = useCallback(async () => {
    if (!enabled || isPollingRef.current) return

    isPollingRef.current = true
    setState(prev => ({ ...prev, isPolling: true, connected: true, error: null }))

    try {
      const query = buildQuery(lastQueryTimeRef.current)
      const results = await executeClickHouseQuery(query)
      
      if (results.length > 0) {
        const events = transformResults(results)
        
        // Process each event (with deduplication)
        let newEventsCount = 0
        events.forEach(event => {
          if (!seenUidsRef.current.has(event.uid)) {
            seenUidsRef.current.add(event.uid)
            onObjectReceived?.(event)
            newEventsCount++
          }
        })

        // Clean up old UIDs to prevent memory leak (keep last 10k)
        if (seenUidsRef.current.size > 10000) {
          const uidsArray = Array.from(seenUidsRef.current)
          seenUidsRef.current = new Set(uidsArray.slice(-5000))
        }

        // Update lastQueryTime ref and state
        const newQueryTime = parseClickHouseTimestamp(events[events.length - 1].timestamp)
        lastQueryTimeRef.current = newQueryTime
        
        if (newEventsCount > 0) {
          setState(prev => ({
            ...prev,
            messageCount: prev.messageCount + newEventsCount,
            lastMessage: new Date(),
            lastQueryTime: newQueryTime
          }))

          console.log(`📊 Live poll: ${newEventsCount} new events (${events.length} total, ${events.length - newEventsCount} duplicates)`)
        } else {
          // All events were duplicates, still advance query time
          setState(prev => ({
            ...prev,
            lastQueryTime: newQueryTime
          }))
        }
      } else {
        // No new events, advance query time to prevent re-querying same range
        const newQueryTime = new Date(Math.max(lastQueryTimeRef.current.getTime(), Date.now() - 5000))
        lastQueryTimeRef.current = newQueryTime
        setState(prev => ({
          ...prev,
          lastQueryTime: newQueryTime
        }))
      }

    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : 'Unknown error'
      console.error('Live polling error:', errorMessage)
      
      setState(prev => ({ 
        ...prev, 
        error: errorMessage,
        connected: false,
        isPolling: false 
      }))
      onError?.(errorMessage)
    } finally {
      isPollingRef.current = false
      setState(prev => ({ ...prev, isPolling: false }))
    }
  }, [enabled, buildQuery, transformResults, onObjectReceived, onError])

  // Keep the latest executePoll in a ref so the polling loop can call it without
  // listing it as an effect dependency. executePoll changes identity on every render
  // (App passes a fresh `filters` object literal), so depending on it would tear down
  // and restart the loop each render — firing an immediate poll roughly every second
  // and making pollInterval meaningless.
  const executePollRef = useRef(executePoll)
  executePollRef.current = executePoll

  // Start/stop polling based on enabled state. Depends ONLY on enabled + pollInterval,
  // so the interval is what actually paces polling (not re-render churn).
  useEffect(() => {
    if (!enabled) {
      // Stop polling
      if (pollTimeoutRef.current) {
        clearTimeout(pollTimeoutRef.current)
        pollTimeoutRef.current = null
      }
      isPollingRef.current = false
      setState(prev => ({ ...prev, connected: false, isPolling: false }))
      return
    }

    let stopped = false
    const poll = async () => {
      await executePollRef.current()
      if (!stopped) {
        pollTimeoutRef.current = setTimeout(poll, pollInterval)
      }
    }

    // Initial poll
    poll()

    return () => {
      stopped = true
      if (pollTimeoutRef.current) {
        clearTimeout(pollTimeoutRef.current)
        pollTimeoutRef.current = null
      }
    }
  }, [enabled, pollInterval])

  // Reset when filters change
  useEffect(() => {
    if (enabled) {
      console.log('Filters changed, resetting query state')
      const resetTime = new Date(Date.now() - 10000)
      lastQueryTimeRef.current = resetTime
      setState(prev => ({
        ...prev,
        lastQueryTime: resetTime,
        messageCount: 0
      }))
      // Clear seen UIDs when filters change
      seenUidsRef.current.clear()
    }
  }, [filters.clusters, filters.namespaces, filters.kinds, filters.customSQL, enabled])

  // Mimic the old WebSocket API for compatibility
  return {
    connected: state.connected,
    error: state.error,
    lastMessage: state.lastMessage,
    messageCount: state.messageCount,
    connect: () => {}, // No-op for compatibility
    disconnect: () => {} // No-op for compatibility
  }
}