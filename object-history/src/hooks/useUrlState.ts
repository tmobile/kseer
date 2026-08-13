import { useCallback, useRef, useEffect } from 'react'
import { GroupingConfiguration } from '../types'

export interface UrlState {
  // Filters
  clusters?: string[]
  kinds?: string[]
  namespaces?: string[]
  
  // Time range
  startTime?: Date
  endTime?: Date
  currentTime?: Date
  
  // Playback
  isPlaying?: boolean
  playbackSpeed?: number
  
  // Grouping
  grouping?: GroupingConfiguration
}

/**
 * Custom hook to synchronize application state with URL parameters
 * This allows users to share links with specific views
 */
export function useUrlState() {
  const isInitialMount = useRef(true)

  /**
   * Parse URL search parameters into application state
   */
  const parseUrlState = useCallback((): UrlState => {
    const params = new URLSearchParams(window.location.search)
    const state: UrlState = {}

    // Parse filters
    const clusters = params.get('clusters')
    if (clusters) {
      state.clusters = clusters.split(',').filter(Boolean)
    }

    const kinds = params.get('kinds')
    if (kinds) {
      state.kinds = kinds.split(',').filter(Boolean)
    }

    const namespaces = params.get('namespaces')
    if (namespaces) {
      state.namespaces = namespaces.split(',').filter(Boolean)
    }

    // Parse time range
    const startTime = params.get('startTime')
    if (startTime) {
      const parsed = parseInt(startTime, 10)
      if (!isNaN(parsed)) {
        state.startTime = new Date(parsed)
      }
    }

    const endTime = params.get('endTime')
    if (endTime) {
      const parsed = parseInt(endTime, 10)
      if (!isNaN(parsed)) {
        state.endTime = new Date(parsed)
      }
    }

    const currentTime = params.get('currentTime')
    if (currentTime) {
      const parsed = parseInt(currentTime, 10)
      if (!isNaN(parsed)) {
        state.currentTime = new Date(parsed)
      }
    }

    // Parse playback
    const isPlaying = params.get('isPlaying')
    if (isPlaying) {
      state.isPlaying = isPlaying === 'true'
    }

    const playbackSpeed = params.get('playbackSpeed')
    if (playbackSpeed) {
      const parsed = parseFloat(playbackSpeed)
      if (!isNaN(parsed)) {
        state.playbackSpeed = parsed
      }
    }

    // Parse grouping configuration
    const grouping = params.get('grouping')
    if (grouping) {
      try {
        state.grouping = JSON.parse(decodeURIComponent(grouping))
      } catch (e) {
        console.error('Failed to parse grouping configuration from URL:', e)
      }
    }

    return state
  }, [])

  /**
   * Update URL with current application state (debounced)
   */
  const updateUrlDebounced = useRef<NodeJS.Timeout | null>(null)
  
  const updateUrl = useCallback((state: UrlState) => {
    // Clear any pending updates
    if (updateUrlDebounced.current) {
      clearTimeout(updateUrlDebounced.current)
    }
    
    // Debounce URL updates to avoid excessive updates during playback
    updateUrlDebounced.current = setTimeout(() => {
      const params = new URLSearchParams()

      // Add filters
      if (state.clusters && state.clusters.length > 0) {
        params.set('clusters', state.clusters.join(','))
      }

      if (state.kinds && state.kinds.length > 0) {
        params.set('kinds', state.kinds.join(','))
      }

      if (state.namespaces && state.namespaces.length > 0) {
        params.set('namespaces', state.namespaces.join(','))
      }

      // Add time range
      if (state.startTime) {
        params.set('startTime', state.startTime.getTime().toString())
      }

      if (state.endTime) {
        params.set('endTime', state.endTime.getTime().toString())
      }

      if (state.currentTime) {
        params.set('currentTime', state.currentTime.getTime().toString())
      }

      // Add playback
      if (state.isPlaying !== undefined) {
        params.set('isPlaying', state.isPlaying.toString())
      }

      if (state.playbackSpeed !== undefined) {
        params.set('playbackSpeed', state.playbackSpeed.toString())
      }

      // Add grouping configuration
      if (state.grouping) {
        params.set('grouping', encodeURIComponent(JSON.stringify(state.grouping)))
      }

      // Update URL without reloading the page
      const newUrl = params.toString() 
        ? `${window.location.pathname}?${params.toString()}`
        : window.location.pathname

      window.history.replaceState({}, '', newUrl)
    }, 300) // Debounce by 300ms
  }, [])
  
  // Cleanup debounce timer on unmount
  useEffect(() => {
    return () => {
      if (updateUrlDebounced.current) {
        clearTimeout(updateUrlDebounced.current)
      }
    }
  }, [])

  /**
   * Get initial state from URL on mount
   */
  const getInitialState = useCallback((): UrlState | null => {
    if (isInitialMount.current) {
      isInitialMount.current = false
      const urlState = parseUrlState()
      
      // Only return state if URL has parameters
      if (Object.keys(urlState).length > 0) {
        return urlState
      }
    }
    return null
  }, [parseUrlState])

  return {
    updateUrl,
    getInitialState,
    parseUrlState
  }
}

