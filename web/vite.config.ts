import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  base: '/app/',
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, './src'),
    },
  },
  build: {
    outDir: '../internal/reporting/ui',
    emptyOutDir: true,
    assetsDir: 'assets',
    // One bundle, embedded in the binary and cached by the service worker:
    // Recharts, the map and Markdown make it ~1.3 MB (~400 kB gzipped), all
    // of which the dashboard page renders anyway.
    chunkSizeWarningLimit: 1600,
    // Two pages: the dashboards, and the API docs Go serves at /api/docs
    // (Swagger UI, its own ~1.4 MB bundle the dashboards never load).
    rolldownOptions: {
      input: {
        index: path.resolve(import.meta.dirname, 'index.html'),
        'api-docs': path.resolve(import.meta.dirname, 'api-docs.html'),
      },
    },
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:3100',
    },
  },
})
