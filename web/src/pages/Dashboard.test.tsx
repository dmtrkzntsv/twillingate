import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, endpoints, type Project, type Widget } from '@/lib/api'
import { span } from '@/lib/grid'
import { answerFor, dashboardsList, details, launchWeek, product, projects, views, widgetsById } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Dashboard from './Dashboard'
import Home from './Home'

function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}

function mockApi(opts: { projects?: Project[]; dev?: boolean } = {}) {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(
    dashboardsList(opts.dev ? { dev: true, errors: [{ dir: 'dashboards/sales', message: 'widget "x": unknown component' }] } : {})
  )
  vi.spyOn(endpoints, 'dashboard').mockImplementation(async (id) => {
    const d = details[id]
    if (!d) throw new ApiError(404, 'no such dashboard', 'not_found')
    return d
  })
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: opts.projects ?? projects })
  vi.spyOn(endpoints, 'devVersion').mockResolvedValue({ version: 'v1' })
  vi.spyOn(endpoints, 'saveView').mockResolvedValue({ status: 'saved' })
  return vi.spyOn(endpoints, 'widgetData').mockImplementation(async (id) => answerFor(widgetsById.get(id)!))
}

function renderAt(url: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route
          path="/dashboards/:id"
          element={
            <>
              <Dashboard />
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>
  )
}

/** Like renderAt, with "/dashboards" routed to Home, where an archive with no next tab lands. */
function renderAppAt(url: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/dashboards" element={<Home />} />
        <Route
          path="/dashboards/:id"
          element={
            <>
              <Dashboard />
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>
  )
}

const location = () => screen.getByTestId('location').textContent

beforeEach(() => {
  localStorage.clear()
  window.innerWidth = 1280
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('Dashboard', () => {
  it('renders a system dashboard as report tabs with both switchers', async () => {
    mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    const tabs = await screen.findAllByRole('tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['Views', 'Product', 'Users', 'Groups', 'Retention'])
    expect(screen.getByRole('tab', { name: 'Product' })).toHaveAttribute('aria-selected', 'true')
    expect(await screen.findByRole('button', { name: 'Project: shop' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Range: Last week' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh all' })).toBeInTheDocument()
  })

  it('puts the group menu in the top bar and the tab menu beside the title, on a system tab too', async () => {
    mockApi()
    const copy = vi.spyOn(endpoints, 'duplicate').mockResolvedValue({ ...product, dashboard_id: 30 })
    renderAt('/dashboards/2')

    const group = await screen.findByRole('button', { name: 'Dashboard actions' })
    expect(group.closest('header')).toContainElement(screen.getByRole('tablist'))
    await userEvent.click(group)
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Refresh', 'Duplicate dashboard', 'Hide dashboard'])
    await userEvent.keyboard('{Escape}')

    await userEvent.click(screen.getByRole('button', { name: 'Tab actions' }))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Copy to new dashboard'])

    await userEvent.click(screen.getByRole('menuitem', { name: 'Copy to new dashboard' }))
    expect(copy).toHaveBeenCalledWith(2, { whole_group: undefined, group_id: undefined })
    await waitFor(() => expect(location()).toBe('/dashboards/30'))
  })

  it('lands on a live dashboard after archiving the lone one shown, however slow the list refetch', async () => {
    mockApi()
    const archived = dashboardsList()
    archived.dashboards = archived.dashboards.map((d) =>
      d.dashboard_id === 10 ? { ...d, archived_at: '2026-09-30T00:00:00Z' } : d
    )
    vi.spyOn(endpoints, 'archive').mockImplementation(async () => {
      vi.mocked(endpoints.dashboards).mockImplementation(
        () => new Promise((resolve) => setTimeout(() => resolve(archived), 300))
      )
      return { status: 'archived' }
    })
    renderAppAt('/dashboards/10')

    await userEvent.click(await screen.findByRole('button', { name: 'Dashboard actions' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Archive dashboard' }))

    // "/" picks from the refetched list: Launch week, the last dashboard
    // opened here, is archived there, so the first system one wins.
    await waitFor(() => expect(location()).toBe('/dashboards/1'), { timeout: 2000 })
    expect(vi.mocked(endpoints.archive).mock.calls[0][0]).toBe(10)
    expect(screen.queryByText('Archived: not in the sidebar')).not.toBeInTheDocument()
  })

  it('lists the system group and the live user dashboards in the sidebar, one entry each', async () => {
    mockApi()
    renderAt('/dashboards/1')

    // The sidebar names the system group by its group_title, not its first tab.
    const reports = await screen.findByRole('link', { name: 'Reports' })
    expect(reports).toHaveAttribute('href', '/dashboards/1')
    expect(screen.queryByRole('link', { name: 'Views' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Launch week' })).toHaveAttribute('href', '/dashboards/10')
    expect(screen.getByRole('link', { name: 'Marketing' })).toHaveAttribute('href', '/dashboards/13')
    expect(screen.queryByRole('link', { name: 'Funnel' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Old experiment' })).not.toBeInTheDocument()
  })

  it('renders a lone user dashboard as one tab, the same layout as a group, without switchers', async () => {
    const widgetData = mockApi()
    renderAt('/dashboards/10')

    expect(await screen.findByRole('heading', { level: 1, name: 'Launch week' })).toBeInTheDocument()
    expect(await screen.findByText('Signups during launch')).toBeInTheDocument()
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Launch week'])
    expect(screen.queryByRole('button', { name: /^Project/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Range/ })).not.toBeInTheDocument()
    await waitFor(() => expect(widgetData).toHaveBeenCalled())
    for (const call of widgetData.mock.calls) expect(call[1]).toEqual({})
    expect(screen.getByRole('button', { name: 'Dashboard actions' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Tab actions' })).toBeInTheDocument()
  })

  it('still offers Share… and Download PNG on a dashboard with a project but no range switcher', async () => {
    const user = userEvent.setup()
    const widgetData = mockApi()
    const noRange = { ...product, follows_range: false, widgets: product.widgets.map((w) => ({ ...w, follows_range: false })) }
    vi.spyOn(endpoints, 'dashboard').mockResolvedValue(noRange)
    renderAt('/dashboards/2')

    expect(await screen.findByRole('button', { name: /^Project/ })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Range/ })).not.toBeInTheDocument()
    await waitFor(() => expect(widgetData).toHaveBeenCalled())
    // The widgets are not asked for a range they do not follow.
    for (const call of widgetData.mock.calls) expect(call[1]).not.toHaveProperty('from')
    await user.click((await screen.findAllByRole('button', { name: 'Widget actions' }))[0])
    expect(await screen.findByRole('menuitem', { name: 'Share…' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Download PNG' })).toBeInTheDocument()
  })

  it('offers only Download PNG on a dashboard with no project switcher', async () => {
    const user = userEvent.setup()
    const widgetData = mockApi()
    const noProject = { ...product, follows_project: false, widgets: product.widgets.map((w) => ({ ...w, follows_project: false })) }
    vi.spyOn(endpoints, 'dashboard').mockResolvedValue(noProject)
    renderAt('/dashboards/2')

    await waitFor(() => expect(widgetData).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: /^Project/ })).not.toBeInTheDocument()
    await user.click((await screen.findAllByRole('button', { name: 'Widget actions' }))[0])
    expect(await screen.findByRole('menuitem', { name: 'Download PNG' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: 'Share…' })).not.toBeInTheDocument()
  })

  it('shows a tablist for a two-tab user group, its tabs sortable', async () => {
    mockApi()
    renderAt('/dashboards/13')

    const tabs = await screen.findAllByRole('tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['Marketing', 'Funnel'])
    expect(screen.getByRole('tab', { name: 'Marketing' })).toHaveAttribute('aria-selected', 'true')
    for (const tab of tabs) expect(tab).toHaveAttribute('aria-roledescription', 'sortable')
  })

  it('does not make system tabs sortable', async () => {
    mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    for (const tab of await screen.findAllByRole('tab')) expect(tab).not.toHaveAttribute('aria-roledescription')
  })

  it('keeps focus on the user tab chosen with Enter while its dashboard loads', async () => {
    mockApi()
    vi.mocked(endpoints.dashboard).mockImplementation(async (id) => (id === 14 ? new Promise(() => {}) : details[id]))
    renderAt('/dashboards/13')

    const funnel = await screen.findByRole('tab', { name: 'Funnel' })
    funnel.focus()
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(location()).toMatch(/^\/dashboards\/14\b/))
    await act(async () => {})

    // Marketing is frozen on screen; the tab list was not rebuilt under the focus.
    expect(screen.getByRole('heading', { level: 1, name: 'Marketing' })).toBeInTheDocument()
    expect(document.activeElement).toHaveAttribute('role', 'tab')
    expect(document.activeElement).toHaveTextContent('Funnel')
  })

  it('writes nothing on the dashboard being left: no header menu, no sortable tabs', async () => {
    mockApi()
    vi.mocked(endpoints.dashboard).mockImplementation(async (id) =>
      id === launchWeek.dashboard_id ? new Promise(() => {}) : details[id]
    )
    renderAt('/dashboards/13')
    expect(await screen.findByRole('button', { name: 'Dashboard actions' })).toBeInTheDocument()

    await userEvent.click(screen.getByRole('link', { name: 'Launch week' }))
    await waitFor(() => expect(location()).toBe('/dashboards/10'))

    // Marketing stays on screen, frozen, until Launch week loads.
    expect(screen.getByRole('heading', { level: 1, name: 'Marketing' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Dashboard actions' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Tab actions' })).not.toBeInTheDocument()
    for (const tab of screen.getAllByRole('tab')) expect(tab).not.toHaveAttribute('aria-roledescription')
  })

  // The switcher tests use the small Product report: the Views one renders
  // every chart kind, which is slow to re-render on each interaction.
  it('saves a new range and puts it in the URL', async () => {
    const widgetData = mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    await userEvent.click(await screen.findByRole('button', { name: 'Range: Last week' }))
    await userEvent.click(await screen.findByRole('menuitemradio', { name: 'Last month' }))

    await waitFor(() => expect(endpoints.saveView).toHaveBeenCalledWith(2, { project_id: 7, range: '30d' }))
    expect(location()).toBe('/dashboards/2?project=7&range=30d')
    expect(screen.getByRole('button', { name: 'Range: Last month' })).toBeInTheDocument()
    const days = (call: unknown[]) => {
      const q = call[1] as { from: string; to: string }
      return (Date.parse(q.to) - Date.parse(q.from)) / 86_400_000
    }
    await waitFor(() => expect(widgetData.mock.calls.map(days)).toContain(29))
  })

  it('saves a new project and puts it in the URL', async () => {
    mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    await userEvent.click(await screen.findByRole('button', { name: 'Project: shop' }))
    await userEvent.click(await screen.findByRole('menuitemradio', { name: 'blog' }))

    await waitFor(() => expect(endpoints.saveView).toHaveBeenCalledWith(2, { project_id: 1, range: '7d' }))
    expect(location()).toBe('/dashboards/2?project=1&range=7d')
  })

  it('saves a custom range picked on the calendar', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-26T12:00:00Z'))
    mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    await userEvent.click(await screen.findByRole('button', { name: 'Range: Last week' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Custom…' }))
    const picker = await screen.findByRole('dialog')
    await userEvent.click(within(picker).getByRole('button', { name: /September 10(th)?, 2026/ }))
    await userEvent.click(within(picker).getByRole('button', { name: /September 20(th)?, 2026/ }))
    await userEvent.click(within(picker).getByRole('button', { name: 'Apply' }))

    await waitFor(() =>
      expect(endpoints.saveView).toHaveBeenCalledWith(2, {
        project_id: 7,
        range: 'custom',
        from: '2026-09-10',
        to: '2026-09-20',
      })
    )
    expect(location()).toBe('/dashboards/2?project=7&range=custom&from=2026-09-10&to=2026-09-20')
    vi.useRealTimers()
  })

  it('carries the selection to the next report tab and saves it there', async () => {
    mockApi()
    renderAt('/dashboards/2?project=1&range=30d')

    await userEvent.click(await screen.findByRole('tab', { name: 'Users' }))

    await waitFor(() => expect(location()).toBe('/dashboards/3?project=1&range=30d'))
    await waitFor(() => expect(endpoints.saveView).toHaveBeenCalledWith(3, { project_id: 1, range: '30d' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Users' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Users' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('button', { name: 'Range: Last month' })).toBeInTheDocument()
    // One GET for the tab it lands on: the saved view goes into the cache.
    expect(vi.mocked(endpoints.dashboard).mock.calls.filter(([id]) => id === 3)).toHaveLength(1)
  })

  it('moves focus, not the page, when arrowing across the report tabs', async () => {
    mockApi()
    renderAt('/dashboards/2?project=1&range=30d')

    const tab = await screen.findByRole('tab', { name: 'Product' })
    tab.focus()
    await userEvent.keyboard('{ArrowRight}{ArrowRight}')

    expect(screen.getByRole('tab', { name: 'Groups' })).toHaveFocus()
    expect(location()).toBe('/dashboards/2?project=1&range=30d')
    expect(endpoints.saveView).not.toHaveBeenCalled()
  })

  it('refreshes, from the header, only the widgets past their refresh_after', async () => {
    const [events, perDay] = product.widgets
    const widgetData = mockApi()
    widgetData.mockImplementation(async (id) => {
      const answer = answerFor(widgetsById.get(id)!)
      if (id === perDay.widget_id) answer.refresh_after = new Date(Date.now() + 3_600_000).toISOString()
      return answer
    })
    renderAt('/dashboards/2?project=7&range=7d')

    const button = await screen.findByRole('button', { name: 'Refresh all' })
    await waitFor(() => expect(button).not.toHaveAttribute('aria-disabled', 'true'))
    await userEvent.click(button)

    await waitFor(() => expect(widgetData.mock.calls.filter((c) => c[1].fresh)).toHaveLength(1))
    expect(widgetData.mock.calls.filter((c) => c[1].fresh)[0][0]).toBe(events.widget_id)
  })

  it('counts a remote table by the page on screen and refreshes that page', async () => {
    const attrs: Widget = {
      ...product.widgets[0],
      widget_id: 900,
      name: 'attributes',
      component: 'table',
      title: 'Attributes',
      width: 6,
      height: 10,
      props: { mode: 'remote' },
    }
    const filters = [{ column: 'Attribute', op: 'in', value: ['plan'] }]
    localStorage.setItem('twillingate.widget.2.900.view', JSON.stringify({ filters, sort: { column: 'Count', dir: 'desc' } }))
    const later = () => new Date(Date.now() + 3_600_000).toISOString()
    const widgetData = mockApi()
    vi.mocked(endpoints.dashboard).mockImplementation(async (id) =>
      id === 2 ? { ...product, widgets: [...product.widgets, attrs] } : details[id]
    )
    // Only the remote table is past its refresh_after, until it is refreshed.
    widgetData.mockImplementation(async (id, q) => {
      if (id !== attrs.widget_id) return { ...answerFor(widgetsById.get(id)!), refresh_after: later() }
      return {
        widget_id: id,
        source_type: 'sql',
        removed: false,
        cached_at: new Date(Date.now() - 20 * 60_000).toISOString(),
        refresh_after: q.fresh ? later() : new Date(Date.now() - 60_000).toISOString(),
        data: { columns: ['Attribute', 'Count'], rows: [['plan', '7']], truncated: false },
        page: { offset: q.offset ?? 0, limit: 1000, matched: 2500, total: 2500, filters: [] },
      }
    })
    renderAt('/dashboards/2?project=7&range=7d')

    const button = await screen.findByRole('button', { name: 'Refresh all' })
    await waitFor(() => expect(button).not.toHaveAttribute('aria-disabled', 'true'))
    await userEvent.click(await screen.findByRole('button', { name: 'Next page' }))
    await screen.findByText('1,001–2,000 of 2,500')
    await waitFor(() => expect(button).not.toHaveAttribute('aria-disabled', 'true'))
    await userEvent.click(button)

    await waitFor(() => expect(button).toHaveAttribute('aria-disabled', 'true'))
    const fresh = widgetData.mock.calls.filter((c) => c[1].fresh)
    expect(fresh).toHaveLength(1)
    expect(fresh[0]).toEqual([
      900,
      expect.objectContaining({ filters: JSON.stringify(filters), sort: 'Count:desc', offset: 1000, fresh: true }),
    ])
  })

  it('keeps the old dashboard idle while the next one loads', async () => {
    const widgetData = mockApi()
    let open: (d: typeof launchWeek) => void = () => {}
    vi.mocked(endpoints.dashboard).mockImplementation(async (id) =>
      id === launchWeek.dashboard_id ? new Promise((resolve) => (open = resolve)) : details[id]
    )
    renderAt('/dashboards/2?project=7&range=30d')
    await screen.findByText('Events per day')
    await waitFor(() => expect(widgetData).toHaveBeenCalledTimes(product.widgets.length))

    // The sidebar link carries no selection: the old dashboard would now
    // compute its stored one (7d) and ask again for every widget.
    await userEvent.click(screen.getByRole('link', { name: 'Launch week' }))
    await waitFor(() => expect(location()).toBe('/dashboards/10'))
    await act(async () => {})
    expect(widgetData).toHaveBeenCalledTimes(product.widgets.length)

    await act(async () => open(launchWeek))
    expect(await screen.findByText('Signups during launch')).toBeInTheDocument()
    for (const call of widgetData.mock.calls.slice(product.widgets.length)) {
      expect(launchWeek.widgets.map((w) => w.widget_id)).toContain(call[0])
    }
  })

  it('asks for a project first when there are no active ones, and loads no widget', async () => {
    const widgetData = mockApi({ projects: [{ project_id: 3, name: 'legacy', archived: true, allowed_origins: [] }] })
    renderAt('/dashboards/1')

    expect(await screen.findByText('Create a project first')).toBeInTheDocument()
    expect(screen.getByText(/create_project/)).toBeInTheDocument()
    expect(widgetData).not.toHaveBeenCalled()
  })

  it('says so when a dashboard has no widgets', async () => {
    mockApi()
    renderAt('/dashboards/12')
    expect(await screen.findByText('No widgets yet — ask your agent to add some')).toBeInTheDocument()
  })

  it('shows the oldest data time on screen', async () => {
    mockApi()
    renderAt('/dashboards/10')
    expect(await screen.findByText(/^Data as of /)).toBeInTheDocument()
  })

  it('lists the dev errors in a banner and polls the dev version', async () => {
    mockApi({ dev: true })
    renderAt('/dashboards/10')
    expect(await screen.findByText(/unknown component/)).toBeInTheDocument()
    expect(screen.getByText(/dashboards\/sales/)).toBeInTheDocument()
    await waitFor(() => expect(endpoints.devVersion).toHaveBeenCalled())
  })

  it('shows the archived line and a Restore button only for an archived dashboard', async () => {
    mockApi()
    vi.spyOn(endpoints, 'restore').mockResolvedValue({ status: 'ok' })
    renderAt('/dashboards/11')

    expect(await screen.findByText('Archived: not in the sidebar')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Restore' }))
    await waitFor(() => expect(endpoints.restore).toHaveBeenCalledWith(11, false))
  })

  it('offers no writes in reporting dev: only Refresh, no tab menu, no sortable tabs', async () => {
    mockApi({ dev: true })
    renderAt('/dashboards/13')

    const tabs = await screen.findAllByRole('tab')
    for (const tab of tabs) expect(tab).not.toHaveAttribute('aria-roledescription')
    expect(screen.getByRole('link', { name: 'Marketing' })).not.toHaveAttribute('aria-roledescription')
    // No "Tab actions", and no sidebar "… actions": only the dashboard's
    // own menu and the widgets', which hold reads alone.
    const widgetMenus = await screen.findAllByRole('button', { name: 'Widget actions' })
    const labels = screen.getAllByRole('button', { name: /actions$/ }).map((b) => b.getAttribute('aria-label'))
    expect(new Set(labels)).toEqual(new Set(['Dashboard actions', 'Widget actions']))
    await userEvent.click(screen.getByRole('button', { name: 'Dashboard actions' }))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Refresh'])
    await userEvent.keyboard('{Escape}')
    await userEvent.click(widgetMenus[0])
    expect((await screen.findAllByRole('menuitem')).map((i) => i.textContent)).toEqual(['Download PNG'])
  })

  it('offers auto-refresh at the server\'s interval, remembered per dashboard in this browser', async () => {
    mockApi()
    vi.mocked(endpoints.dashboards).mockResolvedValue({ ...dashboardsList(), auto_refresh_seconds: 900 })
    renderAt('/dashboards/13')

    await userEvent.click(await screen.findByRole('button', { name: 'Dashboard actions' }))
    const auto = screen.getByRole('menuitemcheckbox', { name: 'Auto-refresh every 15 min' })
    expect(auto).toHaveAttribute('aria-checked', 'false')
    await userEvent.click(auto)
    expect(localStorage.getItem('twillingate.auto_refresh.13')).toBe('true')

    await userEvent.click(screen.getByRole('button', { name: 'Dashboard actions' }))
    expect(screen.getByRole('menuitemcheckbox', { name: 'Auto-refresh every 15 min' })).toHaveAttribute('aria-checked', 'true')
  })

  it('offers no auto-refresh when the server allows none', async () => {
    mockApi()
    renderAt('/dashboards/13')

    await userEvent.click(await screen.findByRole('button', { name: 'Dashboard actions' }))
    expect(screen.queryByRole('menuitemcheckbox')).not.toBeInTheDocument()
  })

  it('opens a system group out of the sidebar with all its tabs, its menus, and Show in sidebar', async () => {
    mockApi()
    const hidden = dashboardsList()
    hidden.dashboards = hidden.dashboards.map((d) => (d.owner === 'system' ? { ...d, sidebar: false } : d))
    vi.mocked(endpoints.dashboards).mockResolvedValue(hidden)
    // A built-in is never archived: its tabs are live, only the sidebar flag is off.
    vi.mocked(endpoints.dashboard).mockResolvedValue({ ...product, sidebar: false })
    vi.spyOn(endpoints, 'setSidebar').mockResolvedValue({} as never)
    vi.spyOn(endpoints, 'restore')
    renderAt('/dashboards/2')

    expect(await screen.findByText('Hidden from the sidebar')).toBeInTheDocument()
    expect(screen.queryByText('Archived: not in the sidebar')).not.toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Views', 'Product', 'Users', 'Groups', 'Retention'])
    )
    expect(screen.getByRole('tab', { name: 'Product' })).toHaveAttribute('aria-selected', 'true')
    await userEvent.click(screen.getByRole('button', { name: 'Dashboard actions' }))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Refresh', 'Duplicate dashboard'])
    await userEvent.keyboard('{Escape}')

    await userEvent.click(screen.getByRole('button', { name: 'Show in sidebar' }))
    await waitFor(() => expect(endpoints.setSidebar).toHaveBeenCalledWith(2, true))
    expect(endpoints.restore).not.toHaveBeenCalled()
  })

  it('offers no Show in sidebar on a user dashboard, which is always in it', async () => {
    mockApi()
    // Out of the sidebar only by a write outside the app (a project tab
    // keeps it live); the server refuses `sidebar` on your own dashboards,
    // so the page offers no way to write it.
    vi.mocked(endpoints.dashboard).mockResolvedValue({ ...launchWeek, sidebar: false })
    renderAt('/dashboards/10')

    expect(await screen.findByText('Signups during launch')).toBeInTheDocument()
    expect(screen.queryByText('Hidden from the sidebar')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Show in sidebar' })).not.toBeInTheDocument()
  })

  it('shows no hidden line on an archived dashboard, only the archived one', async () => {
    mockApi()
    vi.mocked(endpoints.dashboard).mockResolvedValue({ ...details[11], sidebar: false })
    renderAt('/dashboards/11')

    expect(await screen.findByText('Archived: not in the sidebar')).toBeInTheDocument()
    expect(screen.queryByText('Hidden from the sidebar')).not.toBeInTheDocument()
  })

  it('offers no Restore on an archived dashboard in reporting dev', async () => {
    mockApi({ dev: true })
    renderAt('/dashboards/11')

    expect(await screen.findByText('Archived: not in the sidebar')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Restore' })).not.toBeInTheDocument()
  })

  it('shows no archived line for a live dashboard', async () => {
    mockApi()
    renderAt('/dashboards/10')

    await screen.findByRole('heading', { level: 1, name: 'Launch week' })
    expect(screen.queryByText('Archived: not in the sidebar')).not.toBeInTheDocument()
  })

  it('reloads the page when the dev version changes', async () => {
    mockApi({ dev: true })
    vi.mocked(endpoints.devVersion).mockResolvedValueOnce({ version: 'v1' }).mockResolvedValue({ version: 'v2' })
    const reload = vi.fn()
    vi.stubGlobal('location', { ...window.location, reload })
    renderAt('/dashboards/10')
    await waitFor(() => expect(reload).toHaveBeenCalled(), { timeout: 3000 })
  })
})

// The grid reads its own width from a ResizeObserver; play one that reports
// a fixed width for the grid (and nothing for the charts inside it).
function observeGridAt(width: number) {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      cb: ResizeObserverCallback
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb
      }
      observe(el: Element) {
        if (el.getAttribute('data-slot') === 'widget-grid') {
          this.cb([{ contentRect: { width } } as ResizeObserverEntry], this as unknown as ResizeObserver)
        }
      }
      unobserve() {}
      disconnect() {}
    }
  )
}

describe.each([
  { screenWidth: 1280, gridWidth: 1000 },
  { screenWidth: 390, gridWidth: 358 },
])('the Views report at a $gridWidth px grid', ({ screenWidth, gridWidth }) => {
  it('renders every widget kind in a card spanning its adapted width', async () => {
    window.innerWidth = screenWidth
    observeGridAt(gridWidth)
    const widgetData = mockApi()
    renderAt('/dashboards/1?project=7&range=7d')

    const grid = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('[data-slot=widget-grid]')
      expect(el).not.toBeNull()
      return el!
    })
    for (const w of views.widgets) expect(within(grid).getByRole('heading', { name: w.title })).toBeInTheDocument()
    await screen.findByText(/^Data as of/)

    const cells = Array.from(grid.children) as HTMLElement[]
    expect(cells.map((c) => Number(c.dataset.span))).toEqual(views.widgets.map((w) => span(w.width, gridWidth)))
    expect(cells.map((c) => c.style.gridRow)).toEqual(views.widgets.map((w) => `span ${w.height} / span ${w.height}`))

    for (const cell of cells) {
      await waitFor(() => expect(cell.querySelector('[data-slot=skeleton]')).toBeNull())
      const card = within(cell)
      expect(card.queryByText("Couldn't load")).toBeNull()
      expect(card.queryByText('Query no longer runs')).toBeNull()
      expect(card.queryByText('Component removed')).toBeNull()
      expect(card.queryByText('No data for this range')).toBeNull()
    }
    await act(async () => {})
    // One request per widget: the header's freshness shares the cards' queries.
    expect(widgetData).toHaveBeenCalledTimes(views.widgets.length)
  }, 20_000)
})
