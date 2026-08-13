import React, { useState, useEffect } from 'react'
import { 
  Box, 
  Paper, 
  Typography, 
  IconButton, 
  Button,
  Select,
  MenuItem,
  TextField,
  FormControl,
  InputLabel,
  Chip,
  Stack,
  Menu,
  Tooltip
} from '@mui/material'
import { Add, Delete, DragIndicator, ArrowDropDown, CheckCircle, Cancel } from '@mui/icons-material'
import { GroupByField, GroupByConfig, GroupingConfiguration } from '../types'

interface GroupingConfiguratorProps {
  configuration: GroupingConfiguration
  onChange: (config: GroupingConfiguration) => void
}

const FIELD_OPTIONS: { value: GroupByField; label: string }[] = [
  { value: 'cluster', label: 'Cluster' },
  { value: 'namespace', label: 'Namespace' },
  { value: 'kind', label: 'Kind (Type)' },
  { value: 'label', label: 'Label' },
  { value: 'annotation', label: 'Annotation' },
]

// Preset configurations. The LAST level is always the leaf (bullet) dimension; every
// level before it is a nested card.
const PRESET_CONFIGS: { name: string; description: string; config: GroupingConfiguration }[] = [
  {
    name: 'Cluster → Namespace → Kind',
    description: 'Cluster & namespace cards, kinds as bullets',
    config: {
      levels: [
        { field: 'cluster' },
        { field: 'namespace' },
        { field: 'kind' }
      ]
    }
  },
  {
    name: 'Namespace → Kind',
    description: 'Namespace cards, kinds as bullets',
    config: {
      levels: [
        { field: 'namespace' },
        { field: 'kind' }
      ]
    }
  },
  {
    name: 'Cluster → Kind',
    description: 'Cluster cards, kinds as bullets',
    config: {
      levels: [
        { field: 'cluster' },
        { field: 'kind' }
      ]
    }
  },
  {
    name: 'Kind → Cluster → Namespace',
    description: 'Kind & cluster cards, namespaces as bullets',
    config: {
      levels: [
        { field: 'kind' },
        { field: 'cluster' },
        { field: 'namespace' }
      ]
    }
  }
]

const GroupingConfigurator = React.memo(function GroupingConfigurator({ configuration, onChange }: GroupingConfiguratorProps) {
  const [presetMenuAnchor, setPresetMenuAnchor] = React.useState<null | HTMLElement>(null)
  const [pendingConfig, setPendingConfig] = useState<GroupingConfiguration>(configuration)
  const [hasChanges, setHasChanges] = useState(false)

  // Update pending config when the parent configuration changes
  useEffect(() => {
    setPendingConfig(configuration)
    setHasChanges(false)
  }, [configuration])

  // The last level is the pinned "leaf" (bullet) dimension; everything before it is a card.
  const leafIndex = pendingConfig.levels.length - 1

  const handleAddLevel = () => {
    // New levels are inserted as cards, just above the pinned leaf level.
    const newLevels = [...pendingConfig.levels]
    newLevels.splice(leafIndex, 0, { field: 'namespace' as GroupByField })
    setPendingConfig({ levels: newLevels })
    setHasChanges(true)
  }

  const handleRemoveLevel = (index: number) => {
    // The leaf is pinned, and at least one card level must remain above it.
    if (index === leafIndex || pendingConfig.levels.length <= 2) return
    const newConfig = {
      levels: pendingConfig.levels.filter((_, i) => i !== index)
    }
    setPendingConfig(newConfig)
    setHasChanges(true)
  }

  const handleUpdateLevel = (index: number, updates: Partial<GroupByConfig>) => {
    const newLevels = [...pendingConfig.levels]
    newLevels[index] = { ...newLevels[index], ...updates }
    setPendingConfig({ levels: newLevels })
    setHasChanges(true)
  }

  const handleMoveLevel = (fromIndex: number, direction: 'up' | 'down') => {
    // Only card levels reorder; the leaf stays pinned last.
    if (fromIndex === leafIndex) return
    const toIndex = direction === 'up' ? fromIndex - 1 : fromIndex + 1
    if (toIndex < 0 || toIndex >= leafIndex) return

    const newLevels = [...pendingConfig.levels]
    const temp = newLevels[fromIndex]
    newLevels[fromIndex] = newLevels[toIndex]
    newLevels[toIndex] = temp
    setPendingConfig({ levels: newLevels })
    setHasChanges(true)
  }

  return (
    <Paper sx={{ p: 2, mb: 2 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 2 }}>
        <Box>
          <Typography variant="h6">
            Grouping Configuration
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Define how objects should be grouped in the visualization
          </Typography>
        </Box>
        <Button
          variant="outlined"
          size="small"
          endIcon={<ArrowDropDown />}
          onClick={(e) => setPresetMenuAnchor(e.currentTarget)}
        >
          Presets
        </Button>
      </Box>

      <Menu
        anchorEl={presetMenuAnchor}
        open={Boolean(presetMenuAnchor)}
        onClose={() => setPresetMenuAnchor(null)}
      >
        {PRESET_CONFIGS.map((preset, index) => (
          <MenuItem
            key={index}
            onClick={() => {
              setPendingConfig(preset.config)
              setHasChanges(true)
              setPresetMenuAnchor(null)
            }}
          >
            <Box>
              <Typography variant="body2">{preset.name}</Typography>
              <Typography variant="caption" color="text.secondary">
                {preset.description}
              </Typography>
            </Box>
          </MenuItem>
        ))}
      </Menu>

      <Stack spacing={2} sx={{ mt: 2 }}>
        {pendingConfig.levels.map((level, index) => {
          const isLeaf = index === leafIndex
          return (
          <Paper key={index} variant="outlined" sx={{ p: 2, ...(isLeaf ? { borderColor: 'primary.main', borderStyle: 'dashed' } : {}) }}>
            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 1 }}>
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                <Chip
                  label={isLeaf ? 'Leaf (bullets)' : `Level ${index + 1}`}
                  size="small"
                  color={isLeaf ? 'secondary' : 'primary'}
                />
                <Typography variant="body2" color="text.secondary">
                  {FIELD_OPTIONS.find(o => o.value === level.field)?.label}
                  {level.value && ` (${level.value})`}
                </Typography>
              </Box>
              {!isLeaf && (
                <Box sx={{ display: 'flex', gap: 0.5 }}>
                  <IconButton
                    size="small"
                    disabled={index === 0}
                    onClick={() => handleMoveLevel(index, 'up')}
                    sx={{ p: 0.5 }}
                  >
                    <DragIndicator fontSize="small" sx={{ transform: 'rotate(180deg)' }} />
                  </IconButton>
                  <IconButton
                    size="small"
                    disabled={index === leafIndex - 1}
                    onClick={() => handleMoveLevel(index, 'down')}
                    sx={{ p: 0.5 }}
                  >
                    <DragIndicator fontSize="small" />
                  </IconButton>
                  <Tooltip title={pendingConfig.levels.length <= 2 ? 'At least one card level is required' : 'Remove this grouping level'}>
                    <span>
                      <IconButton
                        size="small"
                        color="error"
                        onClick={() => handleRemoveLevel(index)}
                        disabled={pendingConfig.levels.length <= 2}
                        sx={{ p: 0.5 }}
                      >
                        <Delete fontSize="small" />
                      </IconButton>
                    </span>
                  </Tooltip>
                </Box>
              )}
            </Box>
            <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>

              <FormControl size="small" sx={{ minWidth: 120 }}>
                <InputLabel>Field</InputLabel>
                <Select
                  value={level.field}
                  label="Field"
                  onChange={(e) => handleUpdateLevel(index, { field: e.target.value as GroupByField })}
                >
                  {FIELD_OPTIONS.map(option => (
                    <MenuItem key={option.value} value={option.value}>
                      {option.label}
                    </MenuItem>
                  ))}
                </Select>
              </FormControl>

              {(level.field === 'label' || level.field === 'annotation') && (
                <TextField
                  size="small"
                  required
                  error={!level.value}
                  label={level.field === 'label' ? 'Label Key' : 'Annotation Key'}
                  value={level.value || ''}
                  onChange={(e) => handleUpdateLevel(index, { value: e.target.value })}
                  placeholder={level.field === 'label' ? 'app.kubernetes.io/name' : 'example.com/role'}
                  helperText={!level.value
                    ? `Enter a ${level.field} key to group by`
                    : `Grouping by ${level.field} "${level.value}"`}
                  sx={{ minWidth: 200, flex: 1 }}
                />
              )}

            </Box>
          </Paper>
          )
        })}

        <Button
          variant="outlined"
          startIcon={<Add />}
          onClick={handleAddLevel}
          sx={{ alignSelf: 'flex-start' }}
        >
          Add Grouping Level
        </Button>
      </Stack>

      <Box sx={{ mt: 3, p: 2, bgcolor: 'background.default', borderRadius: 1 }}>
        <Typography variant="body2" color="text.secondary">
          <strong>Example:</strong> With the current configuration, objects will be grouped by:
        </Typography>
        <Box sx={{ mt: 1, ml: 2 }}>
          {pendingConfig.levels.map((level, index) => (
            <Typography key={index} variant="body2" color="text.secondary">
              {index + 1}. {FIELD_OPTIONS.find(o => o.value === level.field)?.label}
              {level.value && ` (${level.value})`}
              {index === leafIndex ? ' — shown as bullets inside the innermost card' : ' — card'}
            </Typography>
          ))}
        </Box>
      </Box>

      {/* Apply/Cancel buttons */}
      {hasChanges && (
        <Box sx={{ 
          display: 'flex', 
          gap: 1, 
          justifyContent: 'flex-end', 
          mt: 2,
          pt: 2,
          borderTop: '1px solid',
          borderColor: 'divider'
        }}>
          <Button
            variant="outlined"
            size="small"
            startIcon={<Cancel />}
            onClick={() => {
              setPendingConfig(configuration)
              setHasChanges(false)
            }}
          >
            Cancel
          </Button>
          <Button
            variant="contained"
            size="small"
            startIcon={<CheckCircle />}
            onClick={() => {
              onChange(pendingConfig)
              setHasChanges(false)
            }}
          >
            Apply Changes
          </Button>
        </Box>
      )}
    </Paper>
  )
})

export default GroupingConfigurator
