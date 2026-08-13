import { useState, useMemo, type UIEvent } from 'react'
import {
  Box,
  Button,
  Popover,
  TextField,
  Checkbox,
  Typography,
  InputAdornment,
} from '@mui/material'
import { ArrowDropDown, Search } from '@mui/icons-material'

interface FilterSelectProps {
  label: string
  options: string[]
  selected: string[]
  onChange: (next: string[]) => void
  /** Optional per-option counts, shown as a subtle caption under each option. */
  counts?: Map<string, number>
  fullWidth?: boolean
}

// A compact, searchable multi-select dropdown.
// ColumnFilterSelect: a pill button with a selected-count badge that opens a
// search box + scrollable checkbox list with a "Clear (N)" action.
export default function FilterSelect({
  label,
  options,
  selected,
  onChange,
  counts,
  fullWidth = true,
}: FilterSelectProps) {
  const PAGE_SIZE = 100
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null)
  const [search, setSearch] = useState('')
  const [limit, setLimit] = useState(PAGE_SIZE)
  const open = Boolean(anchorEl)

  const visible = useMemo(() => {
    const q = search.trim().toLowerCase()
    return q ? options.filter(o => o.toLowerCase().includes(q)) : options
  }, [options, search])

  // Only render the first `limit` options; grow by a page as the user scrolls down.
  const shown = visible.slice(0, limit)
  const hasMore = visible.length > limit

  const handleSearchChange = (value: string) => {
    setSearch(value)
    setLimit(PAGE_SIZE) // reset paging whenever the filtered set changes
  }

  const handleScroll = (e: UIEvent<HTMLDivElement>) => {
    if (!hasMore) return
    const el = e.currentTarget
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 24) {
      setLimit(prev => prev + PAGE_SIZE)
    }
  }

  const close = () => {
    setAnchorEl(null)
    setSearch('')
    setLimit(PAGE_SIZE)
  }

  const toggle = (value: string) => {
    onChange(
      selected.includes(value)
        ? selected.filter(v => v !== value)
        : [...selected, value]
    )
  }

  const hasSelected = selected.length > 0

  return (
    <>
      <Button
        onClick={(e) => { setAnchorEl(e.currentTarget); setLimit(PAGE_SIZE) }}
        variant="outlined"
        size="small"
        endIcon={<ArrowDropDown />}
        fullWidth={fullWidth}
        sx={{
          justifyContent: 'space-between',
          textTransform: 'none',
          borderColor: hasSelected ? 'primary.main' : 'divider',
          color: hasSelected ? 'primary.main' : 'text.primary',
          fontWeight: hasSelected ? 600 : 400,
        }}
      >
        <Box component="span" sx={{ display: 'flex', alignItems: 'center', gap: 0.75 }}>
          {label}
          {hasSelected && (
            <Box
              component="span"
              sx={{
                bgcolor: 'primary.main',
                color: 'primary.contrastText',
                borderRadius: '10px',
                fontSize: '0.7rem',
                fontWeight: 700,
                px: 0.75,
                lineHeight: 1.6,
                minWidth: 18,
                textAlign: 'center',
              }}
            >
              {selected.length}
            </Box>
          )}
        </Box>
      </Button>

      <Popover
        open={open}
        anchorEl={anchorEl}
        onClose={close}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'left' }}
        slotProps={{ paper: { sx: { width: 280, maxWidth: '90vw', mt: 0.5 } } }}
      >
        <Box sx={{ p: 1, borderBottom: '1px solid', borderColor: 'divider' }}>
          <TextField
            autoFocus
            fullWidth
            size="small"
            placeholder={`Search ${label.toLowerCase()}…`}
            value={search}
            onChange={(e) => handleSearchChange(e.target.value)}
            InputProps={{
              startAdornment: (
                <InputAdornment position="start">
                  <Search fontSize="small" />
                </InputAdornment>
              ),
            }}
          />
        </Box>

        {hasSelected && (
          <Box sx={{ px: 1.5, py: 0.5, borderBottom: '1px solid', borderColor: 'divider' }}>
            <Button
              size="small"
              onClick={() => onChange([])}
              sx={{ textTransform: 'none', p: 0, minWidth: 0 }}
            >
              Clear ({selected.length})
            </Button>
          </Box>
        )}

        <Box sx={{ maxHeight: 260, overflowY: 'auto', py: 0.5 }} onScroll={handleScroll}>
          {visible.length === 0 ? (
            <Typography variant="body2" color="text.secondary" sx={{ px: 1.5, py: 1 }}>
              No matches
            </Typography>
          ) : (
            <>
              {shown.map(opt => {
                const isSel = selected.includes(opt)
                return (
                  <Box
                    key={opt}
                    onClick={() => toggle(opt)}
                    sx={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 1,
                      px: 1,
                      py: 0.25,
                      cursor: 'pointer',
                      bgcolor: isSel ? 'action.selected' : 'transparent',
                      '&:hover': { bgcolor: 'action.hover' },
                    }}
                  >
                    <Checkbox checked={isSel} size="small" sx={{ p: 0.5 }} />
                    <Box sx={{ minWidth: 0, flex: 1 }}>
                      <Typography variant="body2" noWrap>{opt}</Typography>
                      {counts?.has(opt) && (
                        <Typography variant="caption" color="text.secondary">
                          {counts.get(opt)!.toLocaleString()} objects
                        </Typography>
                      )}
                    </Box>
                  </Box>
                )
              })}
              {hasMore && (
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ display: 'block', textAlign: 'center', py: 0.75 }}
                >
                  Showing {shown.length} of {visible.length} — scroll for more
                </Typography>
              )}
            </>
          )}
        </Box>
      </Popover>
    </>
  )
}
