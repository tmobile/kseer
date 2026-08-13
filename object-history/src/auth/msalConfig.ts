import { PublicClientApplication, Configuration } from '@azure/msal-browser'

const tenantId =
  window.env?.AZURE_TENANT_ID || import.meta.env.VITE_AZURE_TENANT_ID || ''
const clientId =
  window.env?.AZURE_CLIENT_ID || import.meta.env.VITE_AZURE_CLIENT_ID || ''

const msalConfig: Configuration = {
  auth: {
    clientId,
    authority: `https://login.microsoftonline.com/${tenantId}`,
    redirectUri: window.location.origin,
  },
  cache: {
    cacheLocation: 'sessionStorage',
    storeAuthStateInCookie: false,
  },
}

export const msalInstance = new PublicClientApplication(msalConfig)

export const loginRequest = {
  scopes: [
    'email',
    'Group.Read.All',
    'openid',
    'profile',
    'User.Read',
    'User.Read.All',
    'User.ReadBasic.All',
  ],
}
