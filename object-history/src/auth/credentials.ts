// Simple credential store for direct ClickHouse access (when MSAL is disabled).
// Credentials are held in memory only — cleared on page refresh.

let username = ''
let password = ''
let ready = false

export function setCredentials(user: string, pass: string) {
  username = user
  password = pass
  ready = true
}

export function getCredentials(): { username: string; password: string } {
  return { username, password }
}

export function hasCredentials(): boolean {
  return ready
}
