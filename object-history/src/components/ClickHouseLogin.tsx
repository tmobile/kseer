import { useState } from 'react'
import { Box, TextField, Button, Typography, Paper } from '@mui/material'
import { setCredentials } from '../auth/credentials'

interface ClickHouseLoginProps {
  onLogin: () => void
}

export default function ClickHouseLogin({ onLogin }: ClickHouseLoginProps) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    setCredentials(username, password)
    onLogin()
  }

  return (
    <Box
      sx={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        minHeight: '100vh',
      }}
    >
      <Paper sx={{ p: 4, maxWidth: 400, width: '100%' }}>
        <Typography variant="h5" sx={{ mb: 3, textAlign: 'center' }}>
          ClickHouse Login
        </Typography>
        <form onSubmit={handleSubmit}>
          <TextField
            label="Username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            fullWidth
            required
            autoFocus
            sx={{ mb: 2 }}
          />
          <TextField
            label="Password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            fullWidth
            required
            sx={{ mb: 3 }}
          />
          <Button type="submit" variant="contained" fullWidth size="large">
            Connect
          </Button>
        </form>
      </Paper>
    </Box>
  )
}
