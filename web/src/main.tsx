import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import './index.css'
import App from './App.tsx'
import { shouldRetry } from './lib/retry.ts'
import { followSystemTheme } from './lib/theme.ts'
import { trackWindowFocus } from './lib/window-focus.ts'

followSystemTheme()
trackWindowFocus()

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // No retry for a refusal, a busy engine or a timeout; other 5xx and
      // network errors get the default backoff (lib/retry.ts).
      retry: shouldRetry,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
)

// The shell is only worth caching offline once it's a real build; in dev
// it would just serve stale assets over Vite's own dev server.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/app/sw.js')
  })
}
