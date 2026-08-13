import { useState, useCallback, useRef } from 'react'
import { KubernetesObject } from '../types'

export interface ObjectAnimation {
  uid: string
  type: 'added' | 'updated' | 'deleted'
  timestamp: Date
  object: KubernetesObject
  duration: number
}

interface AnimationState {
  activeAnimations: Map<string, ObjectAnimation>
  completedAnimations: ObjectAnimation[]
}

export function useObjectAnimations() {
  const [state, setState] = useState<AnimationState>({
    activeAnimations: new Map(),
    completedAnimations: []
  })
  
  const timeoutRefs = useRef<Map<string, NodeJS.Timeout>>(new Map())

  const triggerAnimation = useCallback((object: KubernetesObject, type: ObjectAnimation['type']) => {
    const uid = object.uid
    const duration = getAnimationDuration(type)
    
    const animation: ObjectAnimation = {
      uid,
      type,
      timestamp: new Date(),
      object,
      duration
    }

    // Clear any existing timeout for this object
    const existingTimeout = timeoutRefs.current.get(uid)
    if (existingTimeout) {
      clearTimeout(existingTimeout)
    }

    // Add to active animations
    setState(prev => ({
      ...prev,
      activeAnimations: new Map(prev.activeAnimations.set(uid, animation))
    }))

    // Set timeout to move to completed animations
    const timeout = setTimeout(() => {
      setState(prev => {
        const newActiveAnimations = new Map(prev.activeAnimations)
        newActiveAnimations.delete(uid)
        
        return {
          activeAnimations: newActiveAnimations,
          completedAnimations: [...prev.completedAnimations.slice(-99), animation] // Keep last 100
        }
      })
      timeoutRefs.current.delete(uid)
    }, duration)

    timeoutRefs.current.set(uid, timeout)
  }, [])

  const clearAnimation = useCallback((uid: string) => {
    const timeout = timeoutRefs.current.get(uid)
    if (timeout) {
      clearTimeout(timeout)
      timeoutRefs.current.delete(uid)
    }

    setState(prev => {
      const newActiveAnimations = new Map(prev.activeAnimations)
      newActiveAnimations.delete(uid)
      return {
        ...prev,
        activeAnimations: newActiveAnimations
      }
    })
  }, [])

  const clearAllAnimations = useCallback(() => {
    // Clear all timeouts
    timeoutRefs.current.forEach(timeout => clearTimeout(timeout))
    timeoutRefs.current.clear()

    setState({
      activeAnimations: new Map(),
      completedAnimations: []
    })
  }, [])

  const getAnimationForObject = useCallback((uid: string): ObjectAnimation | null => {
    return state.activeAnimations.get(uid) || null
  }, [state.activeAnimations])

  const isAnimating = useCallback((uid: string): boolean => {
    return state.activeAnimations.has(uid)
  }, [state.activeAnimations])

  return {
    activeAnimations: Array.from(state.activeAnimations.values()),
    completedAnimations: state.completedAnimations,
    triggerAnimation,
    clearAnimation,
    clearAllAnimations,
    getAnimationForObject,
    isAnimating
  }
}

function getAnimationDuration(type: ObjectAnimation['type']): number {
  switch (type) {
    case 'added':
      return 3000 // 3 seconds for new objects
    case 'updated':
      return 2000 // 2 seconds for updates
    case 'deleted':
      return 4000 // 4 seconds for deletions (longer to notice)
    default:
      return 2000
  }
}

export function getAnimationStyles(animation: ObjectAnimation | null, progress?: number): React.CSSProperties {
  if (!animation) return {}

  const elapsed = Date.now() - animation.timestamp.getTime()
  const animationProgress = progress ?? Math.min(elapsed / animation.duration, 1)

  switch (animation.type) {
    case 'added':
      return {
        transform: `scale(${1 + 0.3 * (1 - animationProgress)})`,
        filter: `drop-shadow(0 0 ${10 * (1 - animationProgress)}px #4CAF50)`,
        transition: 'all 0.1s ease-out'
      }

    case 'updated':
      return {
        filter: `drop-shadow(0 0 ${8 * Math.sin(animationProgress * Math.PI * 4)}px #FF9800)`,
        transform: `scale(${1 + 0.1 * Math.sin(animationProgress * Math.PI * 6)})`,
        transition: 'all 0.1s ease-out'
      }

    case 'deleted':
      return {
        opacity: 0.3 + 0.7 * Math.sin(animationProgress * Math.PI * 3),
        filter: `drop-shadow(0 0 ${12 * (1 - animationProgress)}px #f44336)`,
        transform: `scale(${1 - 0.2 * animationProgress})`,
        transition: 'all 0.1s ease-out'
      }

    default:
      return {}
  }
}

export function getAnimationColor(animation: ObjectAnimation | null): string {
  if (!animation) return 'transparent'
  
  switch (animation.type) {
    case 'added':
      return '#4CAF50'
    case 'updated':
      return '#FF9800'
    case 'deleted':
      return '#f44336'
    default:
      return 'transparent'
  }
}
