import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

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
    // One main bundle, embedded in the binary and cached by the service
    // worker: Recharts makes it ~1.2 MB (~360 kB gzipped), and every
    // dashboard renders it (the stat sparkline). The map and markdown
    // widgets are their own chunks (widgets/lazy/), fetched with the first
    // one on screen. The limit is for the API docs bundle below.
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
      // A running `twillingate serve` on its default address, signing in as
      // on a real console: discovery and the login server sit outside /api.
      '/api': 'http://127.0.0.1:8080',
      '/.well-known': 'http://127.0.0.1:8080',
      '/oauth': 'http://127.0.0.1:8080',
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    // e2e/ holds Playwright specs, run by `npm run e2e`, not vitest.
    exclude: ['**/node_modules/**', 'e2e/**'],
  },
})
