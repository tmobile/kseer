import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 3000,
    proxy: {
      '/clickhouse': {
        target: 'https://clickhouse-authz-proxy.example.com', // authz proxy in front of ClickHouse (go run ./cmd/proxy)
        //target: 'https://clickhouse.example.com:8443', // direct ClickHouse HTTPS endpoint
        changeOrigin: true,
        secure: false,
        rewrite: (path) => path.replace(/^\/clickhouse/, ''),
      },
    },
  },
})
