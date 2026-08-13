import { useState, useEffect, useRef } from 'react'
import { Autocomplete, TextField, CircularProgress, Chip, Box, Typography } from '@mui/material'
import { Search } from '@mui/icons-material'
import { KubernetesObject } from '../types'
import { fetchSearchObjects } from '../api/clickhouse'

const PAGE_SIZE = 50

// Sentinel UID used to identify the synthetic "Show all" option injected at position 0
const SHOW_ALL_UID = '__show_all__'

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

function getKindColor(kind: string): string {
  return kindColors[kind] || '#78909C'
}

interface ObjectSearchProps {
  filters?: { clusters?: string[]; kinds?: string[]; namespaces?: string[] }
  onSelect: (object: KubernetesObject) => void
  onShowAll?: (term: string) => void
}

function ObjectSearch({ filters, onSelect, onShowAll }: ObjectSearchProps) {
  const [inputValue, setInputValue] = useState('')
  const [options, setOptions] = useState<KubernetesObject[]>([])
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [open, setOpen] = useState(false)

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const offsetRef = useRef(0)
  const termRef = useRef('')
  const filtersRef = useRef(filters)
  filtersRef.current = filters

  // Refs used inside the scroll handler to avoid stale-closure bugs.
  // React state updates are async — if the handler reads `loadingMore` from the render
  // closure, it still sees false on the next scroll event even though a fetch is already
  // in flight. Using refs that are updated synchronously prevents concurrent fetches.
  const loadingMoreRef = useRef(false)
  const hasMoreRef = useRef(false)

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)

    if (inputValue.length < 2) {
      setOptions([])
      hasMoreRef.current = false
      setLoading(false)
      return
    }

    setOptions([])
    hasMoreRef.current = false
    setLoading(true)
    offsetRef.current = 0
    termRef.current = inputValue

    debounceRef.current = setTimeout(async () => {
      try {
        const results = await fetchSearchObjects(inputValue, filtersRef.current, 0)
        setOptions(results)
        const more = results.length === PAGE_SIZE
        hasMoreRef.current = more
        offsetRef.current = results.length
      } catch (err) {
        console.error('Object search failed:', err)
        setOptions([])
        hasMoreRef.current = false
      } finally {
        setLoading(false)
      }
    }, 300)

    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [inputValue])

  const loadMore = async () => {
    // Use the ref, not the state variable — state is stale inside the scroll handler
    if (loadingMoreRef.current || !hasMoreRef.current) return

    loadingMoreRef.current = true   // synchronous guard
    setLoadingMore(true)
    try {
      const results = await fetchSearchObjects(termRef.current, filtersRef.current, offsetRef.current)
      setOptions(prev => [...prev, ...results])
      const more = results.length === PAGE_SIZE
      hasMoreRef.current = more
      offsetRef.current += results.length
    } catch (err) {
      console.error('Load more failed:', err)
    } finally {
      loadingMoreRef.current = false  // synchronous reset
      setLoadingMore(false)
    }
  }

  const handleListboxScroll = (event: React.UIEvent<HTMLElement>) => {
    const el = event.currentTarget
    if (
      !loadingMoreRef.current &&
      hasMoreRef.current &&
      el.scrollTop + el.clientHeight >= el.scrollHeight - 80
    ) {
      loadMore()
    }
  }

  const shouldBeOpen = open && inputValue.length >= 2 && (loading || options.length > 0 || !!onShowAll)

  // Inject "Show all" as the first option so it's always visible inside the dropdown
  const showAllOption = { uid: SHOW_ALL_UID } as KubernetesObject
  const optionsWithShowAll: KubernetesObject[] = onShowAll && inputValue.length >= 2
    ? [showAllOption, ...options]
    : options

  return (
    <Autocomplete
      freeSolo
      open={shouldBeOpen}
      onOpen={() => setOpen(true)}
      onClose={() => setOpen(false)}
      options={optionsWithShowAll}
      groupBy={opt => {
        const obj = opt as KubernetesObject
        if (obj.uid === SHOW_ALL_UID) return ''
        return `${obj.cluster_name} / ${obj.namespace || '(cluster-scoped)'}`
      }}
      getOptionLabel={opt => typeof opt === 'string' ? opt : (opt as KubernetesObject).uid === SHOW_ALL_UID ? '' : (opt as KubernetesObject).name}
      inputValue={inputValue}
      onInputChange={(_, value) => setInputValue(value)}
      onChange={(_, value) => {
        if (value && typeof value !== 'string') {
          const obj = value as KubernetesObject
          if (obj.uid === SHOW_ALL_UID) {
            onShowAll?.(inputValue)
            setOpen(false)
            return
          }
          onSelect(obj)
          setInputValue('')
          setOptions([])
        }
      }}
      loading={loading}
      filterOptions={x => x}
      ListboxProps={{ onScroll: handleListboxScroll } as any}
      renderInput={params => (
        <TextField
          {...params}
          size="small"
          placeholder="Search objects..."
          InputProps={{
            ...params.InputProps,
            startAdornment: (
              <Search sx={{ color: 'text.secondary', mr: 0.5, fontSize: 18, flexShrink: 0 }} />
            ),
            endAdornment: (
              <>
                {(loading || loadingMore) && <CircularProgress color="inherit" size={14} />}
                {params.InputProps.endAdornment}
              </>
            ),
          }}
        />
      )}
      renderGroup={params => {
        if (params.group === '') {
          // The "Show all" CTA group — render without a group header
          return <>{params.children}</>
        }
        return (
          <li key={params.key}>
            <Box
              sx={{
                px: 2,
                py: 0.5,
                fontSize: '0.7rem',
                fontWeight: 700,
                color: 'text.secondary',
                bgcolor: 'action.hover',
                letterSpacing: '0.05em',
              }}
            >
              {params.group}
            </Box>
            <ul style={{ padding: 0 }}>{params.children}</ul>
          </li>
        )
      }}
      renderOption={(props, option) => {
        const obj = option as KubernetesObject
        const { key, ...rest } = props as any

        if (obj.uid === SHOW_ALL_UID) {
          return (
            <li
              key={key}
              {...rest}
              style={{
                ...rest.style,
                position: 'sticky',
                top: 0,
                zIndex: 1,
                borderBottom: '1px solid rgba(0,0,0,0.08)',
              }}
            >
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.75, width: '100%', color: 'primary.main', fontWeight: 600, fontSize: '0.78rem', py: 0.25 }}>
                <Search sx={{ fontSize: 14 }} />
                Show all "{inputValue}" in visualization
              </Box>
            </li>
          )
        }

        return (
          <li key={key ?? obj.uid} {...rest}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, width: '100%', overflow: 'hidden' }}>
              <Chip
                label={obj.kind}
                size="small"
                sx={{
                  bgcolor: getKindColor(obj.kind),
                  color: 'white',
                  fontSize: '0.65rem',
                  height: 18,
                  flexShrink: 0,
                  '& .MuiChip-label': { px: 0.75 },
                }}
              />
              <Typography
                variant="body2"
                sx={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
              >
                {obj.name}
              </Typography>
            </Box>
          </li>
        )
      }}
      noOptionsText={inputValue.length < 2 ? 'Type 2+ characters to search' : 'No objects found'}
      sx={{ width: '100%' }}
    />
  )
}

export default ObjectSearch
