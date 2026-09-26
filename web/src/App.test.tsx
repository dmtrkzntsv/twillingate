import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'

function renderApp() {
  const client = new QueryClient()
  return render(
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>
  )
}

function dashboardsResponse(dashboards: unknown[]): Response {
  return new Response(JSON.stringify({ timezone: 'UTC', dashboards }), {
    headers: { 'Content-Type': 'application/json' },
  })
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
  window.history.pushState({}, '', '/')
})

describe('App', () => {
  it('renders the dashboard route', () => {
    window.history.pushState({}, '', '/app/dashboards/1')
    renderApp()
    expect(screen.getByText('twillingate')).toBeInTheDocument()
  })

  it('redirects "/" to the last dashboard opened on this device', async () => {
    localStorage.setItem('twillingate.last_dashboard', '2')
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 1, title: 'Views', owner: 'system', widgets: 1 },
          { dashboard_id: 2, title: 'Mine', owner: 'user', widgets: 1 },
        ])
      )
    )
    window.history.pushState({}, '', '/app/')

    renderApp()

    await waitFor(() => expect(window.location.pathname).toBe('/app/dashboards/2'))
  })

  it('redirects "/" to the first system dashboard with nothing stored', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 9, title: 'Mine', owner: 'user', widgets: 1 },
          { dashboard_id: 3, title: 'Views', owner: 'system', widgets: 1 },
        ])
      )
    )
    window.history.pushState({}, '', '/app/')

    renderApp()

    await waitFor(() => expect(window.location.pathname).toBe('/app/dashboards/3'))
  })
})
