import { useMemo, useState } from 'react'
import {
  Box,
  Slider,
  IconButton,
  Typography,
  Select,
  MenuItem,
  FormControl,
  InputLabel,
  Stack,
  Tooltip,
  Chip,
  Paper,
} from '@mui/material'
import {
  PlayArrow,
  Pause,
  SkipPrevious,
  SkipNext,
  FastRewind,
  FastForward,
} from '@mui/icons-material'
import { DateTimePicker } from '@mui/x-date-pickers/DateTimePicker'
import { format } from 'date-fns'
import { KubernetesObject, ActivityBin, ACTIVITY_BIN_COUNT } from '../types'

interface BinStats {
  total: number
  added: number
  modified: number
  deleted: number
  startTime: Date
  endTime: Date
}

interface TimeControlsProps {
  currentTime: Date
  setCurrentTime: (time: Date) => void
  timeRange: { start: Date; end: Date }
  setTimeRange: (range: { start: Date; end: Date }) => void
  isPlaying: boolean
  setIsPlaying: (playing: boolean) => void
  playbackSpeed: number
  setPlaybackSpeed: (speed: number) => void
  isAtCurrentTime?: boolean
  onTimeChange?: (time: Date) => void
  filteredObjects?: KubernetesObject[]
  /** Server-side per-bin activity counts — primary histogram source (accurate per-event). */
  activityBins?: ActivityBin[]
}

function TimeControls({
  currentTime,
  setCurrentTime,
  timeRange,
  setTimeRange,
  isPlaying,
  setIsPlaying,
  playbackSpeed,
  setPlaybackSpeed,
  isAtCurrentTime,
  onTimeChange,
  filteredObjects = [],
  activityBins = [],
}: TimeControlsProps) {
  const [hoveredBin, setHoveredBin] = useState<number | null>(null)

  // Compute histogram data with detailed stats for the timeline.
  // Primary source is the server-side per-bin activity (accurate, per-event).
  // Falls back to binning filteredObjects (latest-per-uid) only when activity is empty.
  const histogramData = useMemo(() => {
    const numBins = ACTIVITY_BIN_COUNT
    const start = timeRange.start.getTime()
    const end = timeRange.end.getTime()
    const binSize = (end - start) / numBins

    const makeBins = (): BinStats[] => Array.from({ length: numBins }, (_, index) => ({
      total: 0,
      added: 0,
      modified: 0,
      deleted: 0,
      startTime: new Date(start + index * binSize),
      endTime: new Date(start + (index + 1) * binSize)
    }))

    // Preferred: the pre-binned activity counts (already summed across groups server-side).
    if (activityBins.length > 0) {
      const bins = makeBins()
      activityBins.forEach(a => {
        const idx = Math.min(Math.max(a.bin_index, 0), numBins - 1)
        bins[idx].added += a.added
        bins[idx].modified += a.modified
        bins[idx].deleted += a.deleted
        bins[idx].total += a.added + a.modified + a.deleted
      })
      return bins
    }

    // Fallback: bin the latest-per-uid objects (less accurate, clumps near range end).
    if (filteredObjects.length === 0) return []
    const bins = makeBins()
    filteredObjects.forEach(ev => {
      // ClickHouse returns timestamps as strings in format "YYYY-MM-DD HH:MM:SS.mmm"
      // Append 'Z' to treat as UTC, or it will be parsed as local time
      const timestampStr = ev.timestamp.endsWith('Z') ? ev.timestamp : ev.timestamp + 'Z'
      const timestamp = new Date(timestampStr).getTime()
      if (timestamp >= start && timestamp <= end) {
        const binIndex = Math.floor((timestamp - start) / binSize)
        const idx = binIndex === numBins ? numBins - 1 : binIndex
        if (idx >= 0 && idx < numBins) {
          bins[idx].total++
          if (ev.operation === 'ADDED') {
            bins[idx].added++
          } else if (ev.operation === 'MODIFIED') {
            bins[idx].modified++
          } else if (ev.operation === 'DELETED') {
            bins[idx].deleted++
          }
        }
      }
    })
    return bins
  }, [activityBins, filteredObjects, timeRange])

  const maxBinValue = useMemo(() => 
    Math.max(...histogramData.map(bin => bin.total), 1), // Ensure at least 1 to avoid division by 0
    [histogramData]
  )

  const handleSliderChange = (_: Event, value: number | number[]) => {
    const newTime = new Date(value as number)
    setCurrentTime(newTime)
    onTimeChange?.(newTime)
  }

  const skipBackward = () => {
    const newTime = new Date(currentTime.getTime() - 60000) // 1 minute
    if (newTime >= timeRange.start) {
      setCurrentTime(newTime)
    } else {
      setCurrentTime(timeRange.start)
    }
  }

  const skipForward = () => {
    const newTime = new Date(currentTime.getTime() + 60000) // 1 minute
    if (newTime <= timeRange.end) {
      setCurrentTime(newTime)
    } else {
      setCurrentTime(timeRange.end)
    }
  }

  const jumpToStart = () => {
    setCurrentTime(timeRange.start)
  }

  const jumpToEnd = () => {
    setCurrentTime(timeRange.end)
  }

  return (
    <Box>
      {/* Time Range Selectors */}
      <Box sx={{ mb: 2, display: 'flex', gap: 2, alignItems: 'center' }}>
        <DateTimePicker
          label="Start Time"
          value={timeRange.start}
          onChange={(newValue) => {
            if (newValue) {
              setTimeRange({ ...timeRange, start: newValue })
              if (currentTime < newValue) {
                setCurrentTime(newValue)
              }
            }
          }}
          slotProps={{ textField: { size: 'small' } }}
        />
        
        <Typography variant="body2">to</Typography>
        
        <DateTimePicker
          label="End Time"
          value={timeRange.end}
          onChange={(newValue) => {
            if (newValue) {
              setTimeRange({ ...timeRange, end: newValue })
              if (currentTime > newValue) {
                setCurrentTime(newValue)
              }
            }
          }}
          slotProps={{ textField: { size: 'small' } }}
        />

        {/* Quick time range selectors */}
        <Box sx={{ display: 'flex', gap: 1 }}>
          <Chip
            label="1h"
            size="small"
            onClick={() => {
              const end = new Date()
              const start = new Date(end.getTime() - 3600000)
              setTimeRange({ start, end })
              setCurrentTime(end)
            }}
          />
          {!isAtCurrentTime && (
            <Chip
              label="GO LIVE"
              size="small"
              color="error"
              onClick={() => {
                const now = new Date()
                setCurrentTime(now)
                // Update end time if needed
                if (timeRange.end < now) {
                  setTimeRange({ ...timeRange, end: now })
                }
              }}
            />
          )}
          <Chip
            label="6h"
            size="small"
            onClick={() => {
              const end = new Date()
              const start = new Date(end.getTime() - 6 * 3600000)
              setTimeRange({ start, end })
              setCurrentTime(end)
            }}
          />
          <Chip
            label="24h"
            size="small"
            onClick={() => {
              const end = new Date()
              const start = new Date(end.getTime() - 24 * 3600000)
              setTimeRange({ start, end })
              setCurrentTime(end)
            }}
          />
          <Chip
            label="7d"
            size="small"
            onClick={() => {
              const end = new Date()
              const start = new Date(end.getTime() - 7 * 24 * 3600000)
              setTimeRange({ start, end })
              setCurrentTime(end)
            }}
          />
        </Box>
      </Box>

      {/* Current Time Display */}
      <Box sx={{ mb: 1, textAlign: 'center', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 1 }}>
        <Typography variant="h6">
          {format(currentTime, 'yyyy-MM-dd HH:mm:ss')}
        </Typography>
        {isAtCurrentTime && (
          <Chip 
            label="LIVE" 
            color="error" 
            size="small" 
            sx={{ 
              animation: 'pulse 2s infinite',
              '@keyframes pulse': {
                '0%': { opacity: 1 },
                '50%': { opacity: 0.5 },
                '100%': { opacity: 1 }
              }
            }}
          />
        )}
      </Box>

      {/* Time Slider with Histogram */}
      <Box sx={{ px: 2, mb: 2 }}>
        {/* Activity Histogram */}
        {histogramData.length > 0 && (
          <Box sx={{
            mb: 1,
            position: 'relative',
            height: 60,
            animation: 'histogramFadeIn 0.4s ease-out',
            '@keyframes histogramFadeIn': {
              from: { opacity: 0, transform: 'translateY(4px)' },
              to: { opacity: 1, transform: 'translateY(0)' },
            },
          }}>
            <svg 
              width="100%" 
              height="60" 
              style={{ display: 'block' }}
              onMouseLeave={() => setHoveredBin(null)}
            >
              {histogramData.map((bin, index) => {
                const barWidth = 100 / histogramData.length
                const barHeight = (bin.total / maxBinValue) * 50 // Max height of 50px
                const isHovered = hoveredBin === index
                
                return (
                  <rect
                    key={index}
                    x={`${index * barWidth}%`}
                    y={60 - barHeight - 5}
                    width={`${barWidth}%`}
                    height={barHeight}
                    fill={bin.total > 0 ? '#1976d2' : '#e0e0e0'}
                    opacity={isHovered ? 0.9 : 0.6}
                    style={{ cursor: bin.total > 0 ? 'pointer' : 'default', transition: 'opacity 0.2s' }}
                    onMouseEnter={() => setHoveredBin(index)}
                  />
                )
              })}
            </svg>
            
            {/* Hover Tooltip */}
            {hoveredBin !== null && histogramData[hoveredBin] && (
              <Paper
                elevation={4}
                sx={{
                  position: 'absolute',
                  top: -10,
                  left: `${(hoveredBin / histogramData.length) * 100}%`,
                  transform: 'translateX(-50%) translateY(-100%)',
                  p: 1.5,
                  minWidth: 200,
                  zIndex: 1000,
                  pointerEvents: 'none'
                }}
              >
                <Typography variant="caption" sx={{ fontWeight: 'bold', display: 'block', mb: 0.5 }}>
                  {format(histogramData[hoveredBin].startTime, 'MMM dd, HH:mm:ss')}
                </Typography>
                <Typography variant="caption" sx={{ display: 'block', color: 'text.secondary', mb: 1 }}>
                  to {format(histogramData[hoveredBin].endTime, 'HH:mm:ss')}
                </Typography>
                <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
                  <Typography variant="caption" sx={{ fontWeight: 'bold' }}>
                    Total: {histogramData[hoveredBin].total}
                  </Typography>
                  {histogramData[hoveredBin].added > 0 && (
                    <Typography variant="caption" sx={{ color: '#4caf50' }}>
                      ✓ Added: {histogramData[hoveredBin].added}
                    </Typography>
                  )}
                  {histogramData[hoveredBin].modified > 0 && (
                    <Typography variant="caption" sx={{ color: '#ff9800' }}>
                      ✎ Modified: {histogramData[hoveredBin].modified}
                    </Typography>
                  )}
                  {histogramData[hoveredBin].deleted > 0 && (
                    <Typography variant="caption" sx={{ color: '#f44336' }}>
                      ✗ Deleted: {histogramData[hoveredBin].deleted}
                    </Typography>
                  )}
                </Box>
              </Paper>
            )}
            
            <Typography 
              variant="caption" 
              sx={{ 
                position: 'absolute', 
                top: 0, 
                left: 0, 
                color: 'text.secondary',
                fontSize: '0.7rem'
              }}
            >
              Activity Histogram ({activityBins.length > 0
                ? `${histogramData.reduce((sum, b) => sum + b.total, 0).toLocaleString()} events`
                : `${filteredObjects.length} objects`})
            </Typography>
          </Box>
        )}
        
        <Slider
          value={currentTime.getTime()}
          min={timeRange.start.getTime()}
          max={timeRange.end.getTime()}
          onChange={handleSliderChange}
          valueLabelDisplay="auto"
          valueLabelFormat={(value) => format(new Date(value), 'HH:mm:ss')}
          sx={{
            '& .MuiSlider-thumb': {
              width: 20,
              height: 20,
            },
          }}
        />
      </Box>

      {/* Playback Controls */}
      <Stack direction="row" spacing={1} alignItems="center" justifyContent="center">
        <Tooltip title="Jump to Start">
          <IconButton onClick={jumpToStart} size="small">
            <SkipPrevious />
          </IconButton>
        </Tooltip>

        <Tooltip title="Skip Backward (1 min)">
          <IconButton onClick={skipBackward} size="small">
            <FastRewind />
          </IconButton>
        </Tooltip>

        <Tooltip title={isPlaying ? "Pause" : "Play"}>
          <IconButton 
            onClick={() => setIsPlaying(!isPlaying)} 
            size="large"
            color="primary"
          >
            {isPlaying ? <Pause /> : <PlayArrow />}
          </IconButton>
        </Tooltip>

        <Tooltip title="Skip Forward (1 min)">
          <IconButton onClick={skipForward} size="small">
            <FastForward />
          </IconButton>
        </Tooltip>

        <Tooltip title="Jump to End">
          <IconButton onClick={jumpToEnd} size="small">
            <SkipNext />
          </IconButton>
        </Tooltip>

        {/* Playback Speed */}
        <FormControl size="small" sx={{ minWidth: 100, ml: 2 }}>
          <InputLabel>Speed</InputLabel>
          <Select
            value={playbackSpeed}
            label="Speed"
            onChange={(e) => setPlaybackSpeed(Number(e.target.value))}
          >
            <MenuItem value={0.5}>0.5x</MenuItem>
            <MenuItem value={1}>1x</MenuItem>
            <MenuItem value={2}>2x</MenuItem>
            <MenuItem value={5}>5x</MenuItem>
            <MenuItem value={10}>10x</MenuItem>
            <MenuItem value={60}>60x</MenuItem>
          </Select>
        </FormControl>
      </Stack>
    </Box>
  )
}

export default TimeControls
