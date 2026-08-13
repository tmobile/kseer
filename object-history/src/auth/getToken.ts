import { InteractionRequiredAuthError } from '@azure/msal-browser'
import { msalInstance, loginRequest } from './msalConfig'
import { azureEnabled } from './authConfig'

/**
 * Returns a Bearer token for the authz proxy, or null when MSAL is disabled
 * (credential mode — clickhouse.ts sends Basic auth instead).
 */

let cachedToken: string | null = null
let tokenExpiresAt = 0
let inflightRequest: Promise<string> | null = null

export async function getToken(): Promise<string | null> {
  if (!azureEnabled) {
    return null
  }

  if (cachedToken && Date.now() < tokenExpiresAt - 60_000) {
    return cachedToken
  }

  if (inflightRequest) {
    return inflightRequest
  }

  inflightRequest = (async () => {
    const accounts = msalInstance.getAllAccounts()
    if (accounts.length === 0) {
      await msalInstance.loginRedirect(loginRequest)
      throw new Error('Redirecting to login')
    }

    try {
      const result = await msalInstance.acquireTokenSilent({
        ...loginRequest,
        account: accounts[0],
      })
      const useIdToken =
        (window.env?.AZURE_USE_ID_TOKEN ?? import.meta.env.VITE_AZURE_USE_ID_TOKEN) === 'true'
      const token = useIdToken ? result.idToken : result.accessToken
      cachedToken = token
      tokenExpiresAt = result.expiresOn
        ? result.expiresOn.getTime()
        : Date.now() + 3_600_000
      return token
    } catch (err) {
      if (err instanceof InteractionRequiredAuthError) {
        await msalInstance.loginRedirect(loginRequest)
        throw new Error('Redirecting to login')
      }
      throw err
    } finally {
      inflightRequest = null
    }
  })()

  return inflightRequest
}
