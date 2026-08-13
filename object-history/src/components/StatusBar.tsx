import { Box, Typography, LinearProgress, Chip } from '@mui/material'
import { Error, CheckCircle } from '@mui/icons-material'

interface StatusBarProps {
  loading: boolean
  error: string | null
  objectCount: number
  totalCount: number
}

function StatusBar({ loading, error, objectCount, totalCount }: StatusBarProps) {
  return (
    <Box
      sx={{
        position: 'fixed',
        bottom: 0,
        left: 0,
        right: 0,
        bgcolor: 'background.paper',
        borderTop: 1,
        borderColor: 'divider',
        p: 1,
        display: 'flex',
        alignItems: 'center',
        gap: 2,
        zIndex: 1300,
      }}
    >
      {loading && <LinearProgress sx={{ position: 'absolute', top: 0, left: 0, right: 0 }} />}
      
      {error ? (
        <>
          <Error color="error" fontSize="small" />
          <Typography variant="body2" color="error">
            {error}
          </Typography>
        </>
      ) : (
        <>
          <CheckCircle color="success" fontSize="small" />
          <Typography variant="body2">Connected</Typography>
        </>
      )}

      <Box sx={{ flexGrow: 1 }} />

      <Typography variant="body2" color="text.secondary">
        Showing {objectCount.toLocaleString()} of {totalCount.toLocaleString()} objects
      </Typography>

      {loading && (
        <Chip
          label="Loading..."
          size="small"
          color="primary"
          variant="outlined"
        />
      )}
    </Box>
  )
}

export default StatusBar
