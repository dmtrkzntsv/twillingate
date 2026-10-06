import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { _resetForTests, reportUnauthorized } from './lib/auth'

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>
  )
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function dashboardsResponse(dashboards: unknown[]): Response {
  return json({ timezone: 'UTC', dashboards })
}

beforeEach(() => {
  localStorage.clear()
  _resetForTests()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  window.history.pushState({}, '', '/')
})

describe('App', () => {
  it('opens the projects on "/"', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) =>
      Promise.resolve(url.startsWith('/api/dashboards')
        ? dashboardsResponse([{ dashboard_id: 3, title: 'Views', owner: 'system', group_id: 3, widgets: 1, sidebar: true, project_tab: false }])
        : json({ projects: [], keys: [], limits: [] }))))
    window.history.pushState({}, '', '/app/')

    renderApp()

    await waitFor(() => expect(window.location.pathname).toBe('/app/projects'))
  })

  it('opens the dashboards on "/" in a dashboards preview, which has no projects', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      json({ timezone: 'UTC', dev: true, dashboards: [{ dashboard_id: 3, title: 'Views', owner: 'system', group_id: 3, widgets: 1, sidebar: true, project_tab: false }] })))
    window.history.pushState({}, '', '/app/')

    renderApp()

    await waitFor(() => expect(window.location.pathname).toBe('/app/dashboards/3'))
  })

  it('redirects "/dashboards" to the last dashboard opened on this device', async () => {
    localStorage.setItem('twillingate.last_dashboard', '2')
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1, widgets: 1, sidebar: true, project_tab: false },
          { dashboard_id: 2, title: 'Mine', owner: 'user', group_id: 2, widgets: 1, sidebar: true, project_tab: false },
        ])
      )
    )
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    await waitFor(() => expect(window.location.pathname).toBe('/app/dashboards/2'))
  })

  it('redirects "/dashboards" to the first system dashboard with nothing stored', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 9, title: 'Mine', owner: 'user', group_id: 9, widgets: 1, sidebar: true, project_tab: false },
          { dashboard_id: 3, title: 'Views', owner: 'system', group_id: 3, widgets: 1, sidebar: true, project_tab: false },
        ])
      )
    )
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    await waitFor(() => expect(window.location.pathname).toBe('/app/dashboards/3'))
  })

  it('never lands on an archived dashboard', async () => {
    localStorage.setItem('twillingate.last_dashboard', '2')
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 1, title: 'Old views', owner: 'system', group_id: 1, widgets: 1, sidebar: true, project_tab: false, archived_at: '2026-09-01T00:00:00Z' },
          { dashboard_id: 2, title: 'Mine', owner: 'user', group_id: 2, widgets: 1, sidebar: true, project_tab: false, archived_at: '2026-09-01T00:00:00Z' },
          { dashboard_id: 4, title: 'Views', owner: 'system', group_id: 4, widgets: 1, sidebar: true, project_tab: false },
        ])
      )
    )
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    await waitFor(() => expect(window.location.pathname).toBe('/app/dashboards/4'))
  })

  it('says so on "/dashboards" when there are no dashboards', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(dashboardsResponse([])))
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    expect(await screen.findByText('No dashboards yet')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open the archive' })).not.toBeInTheDocument()
  })

  it('points to the archive on "/dashboards" when every dashboard is archived', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1, widgets: 1, sidebar: true, project_tab: false, archived_at: '2026-09-01T00:00:00Z' },
          { dashboard_id: 2, title: 'Mine', owner: 'user', group_id: 2, widgets: 1, sidebar: true, project_tab: false, archived_at: '2026-09-01T00:00:00Z' },
        ])
      )
    )
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    expect(await screen.findByText('Everything is archived')).toBeInTheDocument()
    expect(screen.queryByText('No dashboards yet')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('link', { name: 'Open the archive' }))
    await waitFor(() => expect(window.location.pathname).toBe('/app/archive'))
  })

  it('points to the dashboards gallery on "/dashboards" when every built-in is hidden from the sidebar', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1, widgets: 1, sidebar: false, project_tab: true },
          { dashboard_id: 2, title: 'Product', owner: 'system', group_id: 1, widgets: 1, sidebar: false, project_tab: true },
        ])
      )
    )
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    expect(await screen.findByText('Everything is hidden from the sidebar')).toBeInTheDocument()
    expect(screen.queryByText('Everything is archived')).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open the archive' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('link', { name: 'Open Gallery › Dashboards' }))
    await waitFor(() => expect(window.location.pathname).toBe('/app/gallery/dashboards'))
  })

  it('offers both the gallery and the archive on "/dashboards" when the rest is hidden or archived', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1, widgets: 1, sidebar: false, project_tab: true },
          { dashboard_id: 2, title: 'Mine', owner: 'user', group_id: 2, widgets: 1, sidebar: true, project_tab: false, archived_at: '2026-09-01T00:00:00Z' },
        ])
      )
    )
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    expect(await screen.findByText('Everything is hidden from the sidebar')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open Gallery › Dashboards' })).toHaveAttribute('href', '/app/gallery/dashboards')
    expect(screen.getByRole('link', { name: 'Open the archive' })).toHaveAttribute('href', '/app/archive')
  })

  it('opens a hidden dashboard of your own from "/dashboards" when nothing else is left', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        dashboardsResponse([
          { dashboard_id: 2, title: 'Mine', owner: 'user', group_id: 2, widgets: 1, sidebar: false, project_tab: false },
        ])
      )
    )
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    expect(await screen.findByText('Everything is hidden from the sidebar')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open Gallery › Dashboards' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open Mine' })).toHaveAttribute('href', '/app/dashboards/2')
  })

  it('offers a retry on "/dashboards" when the dashboards fail to load', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(json({ error: { code: 'internal', message: 'database is locked' } }, 500))
      .mockResolvedValue(dashboardsResponse([{ dashboard_id: 3, title: 'Views', owner: 'system', group_id: 3, widgets: 1, sidebar: true, project_tab: false }]))
    vi.stubGlobal('fetch', fetch)
    window.history.pushState({}, '', '/app/dashboards')

    renderApp()

    expect(await screen.findByText("Couldn't load the dashboards")).toBeInTheDocument()
    expect(screen.getByText('database is locked')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(window.location.pathname).toBe('/app/dashboards/3'))
  })

  it('sends a 401 to the login page in-app, coming back to the same page', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    window.history.pushState({}, '', '/app/dashboards/7?range=30d')
    renderApp()

    reportUnauthorized()

    await waitFor(() => expect(window.location.pathname).toBe('/app/login'))
    expect(new URLSearchParams(window.location.search).get('returnTo')).toBe('/dashboards/7?range=30d')
  })

  it('shows nothing but a notice while offline', () => {
    vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false)
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    window.history.pushState({}, '', '/app/dashboards/1')

    renderApp()

    expect(screen.getByText('Offline — showing nothing until the connection is back')).toBeInTheDocument()
    expect(fetch).not.toHaveBeenCalled()
  })
})
