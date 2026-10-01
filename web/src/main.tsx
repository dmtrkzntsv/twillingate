import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import './index.css'
import App from './App.tsx'
import { ApiError } from './lib/api.ts'
import { followSystemTheme } from './lib/theme.ts'
import { trackWindowFocus } from './lib/window-focus.ts'

followSystemTheme()
trackWindowFocus()

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // A 4xx is the API rejecting the request itself (bad params, not
      // found, unauthorized) — retrying it verbatim just repeats the same
      // failure. 5xx and network errors still get the default backoff.
      retry: (failureCount, error) =>
        error instanceof ApiError && error.status >= 400 && error.status < 500 ? false : failureCount < 3,
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
