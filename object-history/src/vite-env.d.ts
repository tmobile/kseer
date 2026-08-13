/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_CLICKHOUSE_DATABASE: string
  readonly VITE_CLICKHOUSE_TABLE: string
  readonly VITE_AZURE_ENABLED: string
  readonly VITE_AZURE_TENANT_ID: string
  readonly VITE_AZURE_CLIENT_ID: string
  readonly VITE_AZURE_PROXY_CLIENT_ID: string
  readonly VITE_AZURE_USE_ID_TOKEN: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}

interface Window {
  env?: {
    CLICKHOUSE_DATABASE: string
    CLICKHOUSE_TABLE: string
    AZURE_ENABLED?: string
    AZURE_TENANT_ID?: string
    AZURE_CLIENT_ID?: string
    AZURE_PROXY_CLIENT_ID?: string
    AZURE_USE_ID_TOKEN?: string
  }
}
