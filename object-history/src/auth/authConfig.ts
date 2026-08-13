// Whether Azure Entra (MSAL) authentication is enabled.
// When false, ClickHouse credentials are used directly.
export const azureEnabled =
  (window.env?.AZURE_ENABLED ?? import.meta.env.VITE_AZURE_ENABLED) === 'true'
