import React, { useState } from 'react'
import ReactDOM from 'react-dom/client'
import { ThemeProvider, createTheme } from '@mui/material/styles'
import CssBaseline from '@mui/material/CssBaseline'
import { LocalizationProvider } from '@mui/x-date-pickers'
import { AdapterDateFns } from '@mui/x-date-pickers/AdapterDateFns'
import { MsalProvider } from '@azure/msal-react'
import { msalInstance } from './auth/msalConfig'
import { azureEnabled } from './auth/authConfig'
import { hasCredentials } from './auth/credentials'
import ClickHouseLogin from './components/ClickHouseLogin'
import App from './App'
import './index.css'

const darkTheme = createTheme({
  palette: {
    mode: 'dark',
    primary: {
      main: '#1976d2',
    },
    secondary: {
      main: '#dc004e',
    },
    background: {
      default: '#0a0a0a',
      paper: '#121212',
    },
  },
})

function Root() {
  const [loggedIn, setLoggedIn] = useState(hasCredentials())

  if (!azureEnabled && !loggedIn) {
    return <ClickHouseLogin onLogin={() => setLoggedIn(true)} />
  }

  return <App />
}

;(async () => {
  if (azureEnabled) {
    await msalInstance.initialize()
    await msalInstance.handleRedirectPromise()
  }

  const appTree = (
    <ThemeProvider theme={darkTheme}>
      <LocalizationProvider dateAdapter={AdapterDateFns}>
        <CssBaseline />
        <Root />
      </LocalizationProvider>
    </ThemeProvider>
  )

  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      {azureEnabled ? (
        <MsalProvider instance={msalInstance}>{appTree}</MsalProvider>
      ) : (
        appTree
      )}
    </React.StrictMode>,
  )
})()
