import { useState } from 'react'
import { 
  Dialog, 
  DialogTitle, 
  DialogContent, 
  DialogActions, 
  TextField, 
  Button, 
  IconButton,
  Alert
} from '@mui/material'
import { Settings } from '@mui/icons-material'

interface ClickHouseSettingsProps {
  onSave: (username: string, password: string) => void
  currentUsername: string
  currentPassword: string
}

export function ClickHouseSettings({ onSave, currentUsername, currentPassword }: ClickHouseSettingsProps) {
  const [open, setOpen] = useState(false)
  const [username, setUsername] = useState(currentUsername)
  const [password, setPassword] = useState(currentPassword)

  const handleSave = () => {
    onSave(username, password)
    setOpen(false)
  }

  const handleOpen = () => {
    setUsername(currentUsername)
    setPassword(currentPassword)
    setOpen(true)
  }

  return (
    <>
      <IconButton 
        onClick={handleOpen}
        size="small"
        sx={{ color: 'white' }}
        title="ClickHouse Settings"
      >
        <Settings />
      </IconButton>

      <Dialog open={open} onClose={() => setOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>ClickHouse Connection Settings</DialogTitle>
        <DialogContent>
          <Alert severity="info" sx={{ mb: 2 }}>
            Enter your ClickHouse connection details. They will be stored in browser storage.
          </Alert>
          
          <TextField
            autoFocus
            margin="dense"
            label="Username"
            type="text"
            fullWidth
            variant="outlined"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            sx={{ mb: 2 }}
          />
          
          <TextField
            margin="dense"
            label="Password"
            type="password"
            fullWidth
            variant="outlined"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setOpen(false)}>Cancel</Button>
          <Button onClick={handleSave} variant="contained">Save</Button>
        </DialogActions>
      </Dialog>
    </>
  )
}

export default ClickHouseSettings

