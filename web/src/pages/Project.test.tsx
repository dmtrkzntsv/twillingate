import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { ApiError, endpoints, type DashboardDetail, type ProjectTab } from '@/lib/api'
import { answerFor, dashboardsList, details, launchWeek, widgetsById } from '@/test/fixtures'
import { contactPage, form } from '@/test/forms'
import { renderWithProviders, testClient } from '@/test/render'
import Project from './Project'
import { readLastTab, writeLastTab } from '@/lib/last-tab'
import { monthsRange, utcToday } from '@/lib/usage-months'

const actions = {
  update: vi.fn(), archive: vi.fn(), restore: vi.fn(), issueKey: vi.fn(),
  disableKey: vi.fn(), enableKey: vi.fn(), create: vi.fn(), renameEvent: vi.fn(), blockEvent: vi.fn(), removeEventRule: vi.fn(), addEventAlias: vi.fn(),
  renameAttribute: vi.fn(), mergeAttribute: vi.fn(), blockAttribute: vi.fn(), removeAttributeRule: vi.fn(), addAttributeAlias: vi.fn(), pending: false,
}
vi.mock('@/hooks/use-project-actions', () => ({ useProjectActions: () => actions }))

const tabs: ProjectTab[] = [
  { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1 },
  { dashboard_id: 2, title: 'Product', owner: 'system', group_id: 1 },
  { dashboard_id: 20, title: 'Mine', owner: 'user', group_id: 20 },
]

// A dashboard of the user's that no project has as a tab.
const spare: DashboardDetail = { ...launchWeek, dashboard_id: 1001, title: 'Spare', group_id: 1001, tabs: [{ dashboard_id: 1001, title: 'Spare' }] }
const mine: DashboardDetail = { ...launchWeek, dashboard_id: 20, title: 'Mine', group_id: 20, tabs: [{ dashboard_id: 20, title: 'Mine' }] }

beforeEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
  Object.values(actions).forEach((f) => typeof f === 'function' && f.mockReset())
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList({ purge_after_days: 30 }))
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [
    { project_id: 4, name: 'econumo.com', allowed_origins: ['https://econumo.com'] },
    { project_id: 7, name: 'shop', allowed_origins: ['https://shop.example'] },
    { project_id: 3, name: 'legacy', archived: true, allowed_origins: [] },
  ] })
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [
    { project_id: 4, label: 'web', key: 'ak_web_123456789', state: 'active' },
    { project_id: 4, label: 'old', key: 'ak_old_123456789', state: 'disabled' },
  ] })
  vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [] })
  vi.spyOn(endpoints, 'listAttributes').mockResolvedValue({
    project_id: 4, from: 'a', to: 'b', keys_total: 2,
    attributes: [
      { key: 'plan', events: 900, values: 3, received: true, blocked: false },
      { key: 'tier', events: 20, values: 2, received: true, blocked: false },
    ],
  })
  vi.spyOn(endpoints, 'eventNames').mockResolvedValue({ events: [{ family: 'product', name: 'checkout', events: 120 }] })
  vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'a', action: 'rename', to: 'b' }] })
  vi.spyOn(endpoints, 'limits').mockResolvedValue({ limits: [], events_30d: { count: 10, from: '2026-09-11', to: '2026-10-10' } })
  vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs })
  vi.spyOn(endpoints, 'projectActivity').mockResolvedValue({
    projects: [
      { project_id: 4, last_event_day: '2026-10-08', new_submissions: 0, forms: 0, draft_forms: 0 },
      { project_id: 7, last_event_day: '2026-10-08', new_submissions: 3, forms: 0, draft_forms: 0 },
    ],
  })
  vi.spyOn(endpoints, 'dashboard').mockImplementation(async (id) => {
    const d = { ...details, 20: mine, 1001: spare }[id]
    if (!d) throw new ApiError(404, 'no such dashboard', 'not_found')
    return d
  })
  vi.spyOn(endpoints, 'widgetData').mockImplementation(async (id) => answerFor(widgetsById.get(id)!))
  vi.spyOn(endpoints, 'saveView').mockResolvedValue({ status: 'saved' })
})

function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}

const location = () => screen.getByTestId('location').textContent

function renderAt(path: string, client = testClient()) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/projects/:id" element={<Project landing />} />
        <Route path="/projects/:id/settings" element={<Project />} />
        <Route path="/projects/:id/settings/:section" element={<Project />} />
        <Route path="/projects/:id/dashboards/:dashId" element={<Project />} />
        <Route path="/projects/:id/forms" element={<Project tab="forms" />} />
        <Route path="/projects/:id/forms/:name" element={<Project tab="forms" />} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
    client
  )
}

describe('Project', () => {
  it('heads the page with a breadcrumb back to the projects', async () => {
    renderAt('/projects/4/settings')
    const nav = await screen.findByRole('navigation', { name: 'breadcrumb' })
    expect(within(nav).getByRole('link', { name: 'Projects' })).toHaveAttribute('href', '/projects')
    expect(await within(nav).findByText('econumo.com')).toHaveAttribute('aria-current', 'page')
  })

  it('draws the page while it decides which tab to land on', async () => {
    vi.spyOn(endpoints, 'projectTabs').mockReturnValue(new Promise(() => {}))
    renderAt('/projects/4')
    const nav = await screen.findByRole('navigation', { name: 'breadcrumb' })
    expect(await within(nav).findByText('econumo.com')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'econumo.com' })).toBeInTheDocument()
    expect(location()).toBe('/projects/4')
  })

  it('lands from a fresh tab list when reopening the project already open', async () => {
    const user = userEvent.setup()
    writeLastTab(4, 20)
    renderWithProviders(
      <MemoryRouter initialEntries={['/projects/4/dashboards/20']}>
        <Routes>
          <Route path="/projects/:id" element={<Project landing />} />
          <Route path="/projects/:id/dashboards/:dashId" element={<Project />} />
        </Routes>
        <Link to="/projects/4">reopen</Link>
        <LocationProbe />
      </MemoryRouter>,
      testClient()
    )
    await screen.findByRole('heading', { name: 'Mine', level: 1 })
    // Mine is removed elsewhere while this page shows it.
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: tabs.filter((t) => t.dashboard_id !== 20) })
    await user.click(screen.getByRole('link', { name: 'reopen' }))
    await vi.waitFor(() => expect(location()).toBe('/projects/4/dashboards/1'))
  })

  it('renames the project in place through PATCH, the name alone', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4/dashboards/1')
    await user.click(await screen.findByRole('button', { name: 'Rename' }))
    const field = screen.getByRole('textbox', { name: 'Project name' })
    await user.clear(field)
    await user.type(field, 'econumo{Enter}')
    expect(actions.update).toHaveBeenCalledWith(4, { name: 'econumo' })
  })

  it('drops a rename draft when switching to another project of the same name', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [
      { project_id: 4, name: 'shop', allowed_origins: [] },
      { project_id: 7, name: 'shop', allowed_origins: [] },
    ] })
    renderWithProviders(
      <MemoryRouter initialEntries={['/projects/4/dashboards/1']}>
        <Routes>
          <Route path="/projects/:id" element={<Project landing />} />
          <Route path="/projects/:id/dashboards/:dashId" element={<Project />} />
        </Routes>
        <Link to="/projects/7/dashboards/1">other</Link>
      </MemoryRouter>,
      testClient()
    )
    await user.click(await screen.findByRole('button', { name: 'Rename' }))
    await user.type(screen.getByRole('textbox', { name: 'Project name' }), '-draft')
    await user.click(screen.getByRole('link', { name: 'other' }))
    expect(screen.queryByRole('textbox', { name: 'Project name' })).not.toBeInTheDocument()
  })

  it('shows the allowed origins and adds one through PATCH, never the name or attributes', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4/settings/domains')
    const origins = await screen.findByRole('region', { name: 'Allowed origins' })
    expect(within(origins).getByText('https://econumo.com')).toBeInTheDocument()
    expect(within(origins).queryByText('plan')).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Details' })).not.toBeInTheDocument()
    await user.click(within(origins).getByRole('button', { name: 'Add origin' }))
    await user.type(screen.getByLabelText('Origin'), 'https://app.econumo.com')
    await user.click(screen.getByRole('button', { name: 'Add' }))
    expect(actions.update).toHaveBeenCalledWith(4, { allowed_origins: ['https://econumo.com', 'https://app.econumo.com'] })
  })

  it('shows the Settings tabs in place of the dashboards, each with its own address, keeping the range', async () => {
    renderAt('/projects/4/settings?range=30d')
    expect(await screen.findByRole('heading', { name: 'Overview', level: 1 })).toBeInTheDocument()
    const nav = screen.getByRole('navigation', { name: 'Settings sections' })
    expect(within(nav).getAllByRole('link').map((l) => [l.textContent, l.getAttribute('href')])).toEqual([
      ['Overview', '/projects/4/settings/overview?range=30d'],
      ['Domains', '/projects/4/settings/domains?range=30d'],
      ['Events', '/projects/4/settings/events?range=30d'],
      ['Attributes', '/projects/4/settings/attributes?range=30d'],
      ['Danger', '/projects/4/settings/danger?range=30d'],
    ])
    expect(within(nav).getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByRole('tab')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Add tab' })).toBeNull()
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page')
  })

  it('shows one tab at a time, titled with its name, and the range only where it counts something', async () => {
    const user = userEvent.setup()
    renderAt('/projects/4/settings/overview?range=30d')
    expect(await screen.findByRole('heading', { name: 'Overview', level: 1 })).toBeInTheDocument()
    // Usage by month and by day, then the install snippet and the keys.
    const usage = await screen.findByRole('region', { name: 'Usage' })
    expect(within(usage).getByRole('heading', { name: 'By month', level: 3 })).toBeInTheDocument()
    expect(within(usage).getByRole('heading', { name: 'By day', level: 3 })).toBeInTheDocument()
    expect(await screen.findByRole('region', { name: 'Ingest keys' })).toBeInTheDocument()
    const regions = screen.getAllByRole('region').map((r) => r.getAttribute('aria-label'))
    expect(regions.filter((r) => ['Usage', 'Install', 'Ingest keys'].includes(r!))).toEqual(['Usage', 'Install', 'Ingest keys'])
    expect(screen.getByRole('button', { name: /^Range:/ })).toBeInTheDocument()
    const nav = screen.getByRole('navigation', { name: 'Settings sections' })
    expect(within(nav).getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page')

    await user.click(within(nav).getByRole('link', { name: 'Domains' }))
    expect(location()).toBe('/projects/4/settings/domains?range=30d')
    expect(await screen.findByRole('region', { name: 'Allowed origins' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Ingest keys' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Range:/ })).not.toBeInTheDocument()

    for (const [name, region] of [['Events', 'Events'], ['Attributes', 'Attributes'], ['Overview', 'Usage']]) {
      await user.click(within(nav).getByRole('link', { name }))
      expect(await screen.findByRole('region', { name: region })).toBeInTheDocument()
      expect(screen.getByRole('heading', { name, level: 1 })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /^Range:/ })).toBeInTheDocument()
    }

    await user.click(within(nav).getByRole('link', { name: 'Danger' }))
    expect(await screen.findByRole('region', { name: 'Danger' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Range:/ })).not.toBeInTheDocument()
  })

  it('goes from an unknown section to the first tab, keeping the range', async () => {
    renderAt('/projects/4/settings/keys?range=30d')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(location()).toBe('/projects/4/settings?range=30d')
  })

  it('goes from the first version\'s Usage and Install tabs to Overview, keeping the range', async () => {
    for (const old of ['usage', 'install']) {
      const { unmount } = renderAt(`/projects/4/settings/${old}?range=7d`)
      expect(await screen.findByRole('heading', { name: 'Overview', level: 1 })).toBeInTheDocument()
      expect(location()).toBe('/projects/4/settings?range=7d')
      unmount()
    }
  })

  it('has no Danger tab on an archived project, nor install or keys on its Overview', async () => {
    renderAt('/projects/3/settings/danger')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(location()).toBe('/projects/3/settings')
    const nav = screen.getByRole('navigation', { name: 'Settings sections' })
    expect(within(nav).getAllByRole('link').map((l) => l.textContent)).toEqual(['Overview', 'Domains', 'Events', 'Attributes'])
    expect(screen.queryByRole('region', { name: 'Install' })).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Ingest keys' })).not.toBeInTheDocument()
    expect(endpoints.keys).not.toHaveBeenCalled()
  })

  it('keeps Restore in the header of every tab of an archived project', async () => {
    const user = userEvent.setup()
    renderAt('/projects/3/settings')
    const nav = await screen.findByRole('navigation', { name: 'Settings sections' })
    for (const name of ['Overview', 'Domains', 'Events', 'Attributes']) {
      await user.click(within(nav).getByRole('link', { name }))
      expect(await screen.findByRole('heading', { name, level: 1 })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Restore' })).toBeInTheDocument()
    }
  })

  it('heads Settings with a back button to the dashboard last opened here, else the first, as the gear does', async () => {
    const user = userEvent.setup()
    writeLastTab(4, 2)
    const { unmount } = renderAt('/projects/4/settings/events?range=30d')
    const back = await screen.findByRole('link', { name: 'Back to the dashboards of econumo.com' })
    expect(back).toHaveTextContent('econumo.com')
    // In place of the name and its pencil, which only a dashboard page has.
    expect(screen.queryByRole('heading', { name: 'econumo.com' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Rename' })).not.toBeInTheDocument()
    await vi.waitFor(() => expect(back).toHaveAttribute('href', '/projects/4/dashboards/2?range=30d'))
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/projects/4/dashboards/2?range=30d')
    await user.click(back)
    expect(await screen.findByRole('heading', { name: 'Product', level: 1 })).toBeInTheDocument()
    expect(location()).toBe('/projects/4/dashboards/2?range=30d')
    expect(screen.getByRole('heading', { name: 'econumo.com' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Rename' })).toBeInTheDocument()
    // Back on a dashboard, the gear opens Settings again.
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/projects/4/settings?range=30d')
    unmount()
    localStorage.clear()
    renderAt('/projects/4/settings')
    await vi.waitFor(() => expect(screen.getByRole('link', { name: 'Back to the dashboards of econumo.com' })).toHaveAttribute('href', '/projects/4/dashboards/1'))
  })

  it('heads the Forms pages with the back button too, and shows no dashboard tabs there', async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [] })
    renderAt('/projects/4/forms?range=30d')
    await vi.waitFor(() => expect(screen.getByRole('link', { name: 'Back to the dashboards of econumo.com' })).toHaveAttribute('href', '/projects/4/dashboards/1?range=30d'))
    // No forms: no tabs, the empty state, and New form and Find a person in the row.
    expect(await screen.findByRole('region', { name: 'Add a form' })).toBeInTheDocument()
    expect(screen.queryByRole('tab')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Add tab' })).toBeNull()
    // "+" in the row, and the empty state's own.
    expect(screen.getAllByRole('button', { name: 'New form' })).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Find a person' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /^Forms/ })).toHaveAttribute('aria-current', 'page')
    expect(location()).toBe('/projects/4/forms?range=30d')
  })

  it('keeps the Archived badge beside the back button', async () => {
    renderAt('/projects/3/settings')
    const back = await screen.findByRole('link', { name: 'Back to the dashboards of legacy' })
    expect(back.parentElement).toHaveTextContent('Archived')
  })

  it('keeps the name and its pencil, with neither the back button nor Add dashboard, when the tabs cannot be read', async () => {
    vi.mocked(endpoints.projectTabs).mockRejectedValue(new ApiError(500, 'database is locked', 'internal'))
    renderAt('/projects/7/settings')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    await vi.waitFor(() => expect(endpoints.projectTabs).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByRole('button', { name: 'Add dashboard' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Back to the dashboards/ })).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'shop' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Rename' })).toBeInTheDocument()
  })

  it('offers Add dashboard in place of the back button on a project with no tabs, and opens the one added', async () => {
    const user = userEvent.setup()
    vi.mocked(endpoints.projectTabs).mockResolvedValue({ tabs: [] })
    const add = vi.spyOn(endpoints, 'addProjectTab').mockResolvedValue({ tabs: [{ dashboard_id: 3, title: 'Users', owner: 'system', group_id: 1 }] })
    renderAt('/projects/7/settings?range=30d')
    const addButton = await screen.findByRole('button', { name: 'Add dashboard' })
    // The name stays renamable: with no dashboard, Settings is the only page left to do it.
    expect(screen.getByRole('button', { name: 'Rename' })).toBeInTheDocument()
    await user.click(addButton)
    expect(screen.queryByRole('link', { name: /Back to the dashboards/ })).not.toBeInTheDocument()
    const builtin = await screen.findByRole('group', { name: 'Built-in' })
    await user.click(within(builtin).getByRole('button', { name: 'Users' }))
    expect(add).toHaveBeenCalledWith(7, { dashboard_id: 3 })
    await vi.waitFor(() => expect(location()).toBe('/projects/7/dashboards/3?range=30d'))
  })

  it('blocks an attribute, hiding its past events at once', async () => {
    const user = userEvent.setup()
    actions.blockAttribute.mockResolvedValue(true)
    renderAt('/projects/4/settings/attributes')
    const section = await screen.findByRole('region', { name: 'Attributes' })
    await user.click(await within(section).findByRole('button', { name: 'Actions for tier' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Block…' }))
    expect(await screen.findByText(/hidden from every report at once/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Block attribute' }))
    expect(actions.blockAttribute).toHaveBeenCalledWith(4, { key: 'tier' })
  })

  it('lists keys and disables one after confirming', async () => {
    const user = userEvent.setup()
    actions.disableKey.mockResolvedValue(true)
    renderAt('/projects/4/settings/overview')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    const row = await within(keys).findByRole('row', { name: /web/ })
    expect(within(row).getByText('active')).toBeInTheDocument()
    await user.click(within(row).getByRole('button', { name: 'Disable web' }))
    expect(actions.disableKey).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Disable key' }))
    expect(actions.disableKey).toHaveBeenCalledWith(4, 'web')
    expect(within(within(keys).getByRole('row', { name: /old/ })).getByRole('button', { name: 'Enable old' })).toBeInTheDocument()
  })

  it('enables a disabled key without asking', async () => {
    const user = userEvent.setup()
    actions.enableKey.mockResolvedValue(true)
    renderAt('/projects/4/settings/overview')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    await user.click(await within(keys).findByRole('button', { name: 'Enable old' }))
    expect(actions.enableKey).toHaveBeenCalledWith(4, 'old')
  })

  it('lists the events and blocks one, hiding its past events at once', async () => {
    const user = userEvent.setup()
    actions.blockEvent.mockResolvedValue(true)
    actions.removeEventRule.mockResolvedValue(true)
    renderAt('/projects/4/settings/events')
    const events = await screen.findByRole('region', { name: 'Events' })
    await user.click(await within(events).findByRole('button', { name: 'Actions for checkout (product)' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Block…' }))
    expect(await screen.findByText(/past events included; new ones are not stored/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Block event' }))
    expect(actions.blockEvent).toHaveBeenCalledWith(4, { family: 'product', name: 'checkout' })
    await user.click(await within(events).findByRole('button', { name: 'Remove alias a' }))
    await user.click(await screen.findByRole('button', { name: 'Remove alias' }))
    expect(actions.removeEventRule).toHaveBeenCalledWith(4, { family: 'product', name: 'a' })
  })

  it('issues a key and shows it with its snippet', async () => {
    const user = userEvent.setup()
    actions.issueKey.mockResolvedValue({ key: 'ak_ios', snippet: 'twillingate.init(…)', status: 'issued' })
    renderAt('/projects/4/settings/overview')
    await user.click(await screen.findByRole('button', { name: 'Issue key' }))
    await user.type(screen.getByLabelText('Label'), 'ios')
    await user.click(screen.getByRole('button', { name: 'Issue' }))
    expect(actions.issueKey).toHaveBeenCalledWith(4, 'ios')
    expect(await screen.findByText('ak_ios')).toBeInTheDocument()
    expect(screen.getByText('twillingate.init(…)')).toBeInTheDocument()
  })

  it('archives after a confirmation naming the purge window', async () => {
    const user = userEvent.setup()
    actions.archive.mockResolvedValue(true)
    renderAt('/projects/4/settings/danger')
    const danger = await screen.findByRole('region', { name: 'Danger' })
    // The only place that offers Archive.
    expect(screen.getAllByRole('button', { name: 'Archive' })).toHaveLength(1)
    await user.click(within(danger).getByRole('button', { name: 'Archive' }))
    const purge = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: 'UTC' }).format(new Date(Date.now() + 30 * 86_400_000))
    expect(screen.getByText(new RegExp(`deleted on ${purge} \\(30 days\\)`))).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Archive project' }))
    expect(actions.archive).toHaveBeenCalledWith(4)
  })

  it('offers Restore on an archived project and still shows it', async () => {
    renderAt('/projects/3/settings')
    expect(await screen.findByRole('button', { name: 'Restore' })).toBeInTheDocument()
    expect(screen.getByText('Archived')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Archive' })).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Danger' })).not.toBeInTheDocument()
  })

  it('shows only the origins under Domains on an archived project, which refuses every change', async () => {
    const monitor = vi.spyOn(endpoints, 'monitors').mockResolvedValue({ interval_seconds: 60, timeout_seconds: 5, paused: false, monitors: [], domains: [] })
    renderAt('/projects/3/settings/domains')
    expect(await screen.findByRole('button', { name: 'Restore' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Allowed origins' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Monitoring' })).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Domain expiry' })).not.toBeInTheDocument()
    expect(monitor).not.toHaveBeenCalled()
  })

  it('says so for an unknown project', async () => {
    renderAt('/projects/77/settings')
    expect(await screen.findByText(/There is no project 77\./)).toBeInTheDocument()
  })

  it('shows daily usage over the last 30 days on Overview, no cap impact, and refetches when the range changes', async () => {
    const user = userEvent.setup()
    // The daily chart's calls; the monthly chart reads its own six months.
    const daily = () => vi.mocked(endpoints.usage).mock.calls.map(([q]) => q).filter((q) => q.from !== monthsRange(utcToday()).from)
    renderAt('/projects/4/settings')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Cap impact' })).not.toBeInTheDocument()
    await vi.waitFor(() => expect(daily().length).toBeGreaterThan(0))
    const first = daily().at(-1)!
    expect(first).toMatchObject({ project_id: 4 })
    // Opens on the last 30 days, today included.
    expect(screen.getByRole('button', { name: 'Range: Last month' })).toBeInTheDocument()
    expect((Date.parse(first.to!) - Date.parse(first.from!)) / 86_400_000).toBe(29)
    await user.click(screen.getByRole('button', { name: /^Range:/ }))
    await user.click(await screen.findByRole('menuitemradio', { name: 'Last week' }))
    await vi.waitFor(() => expect(daily().at(-1)).not.toEqual(first))
    expect(vi.mocked(endpoints.usage)).toHaveBeenCalledWith(expect.objectContaining(monthsRange(utcToday())))
  })

  it('opens Events on the last 7 days', async () => {
    renderAt('/projects/4/settings/events')
    expect(await screen.findByRole('button', { name: 'Range: Last week' })).toBeInTheDocument()
  })

  it('keeps "kept until restored" when the server names no purge window', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList())
    renderAt('/projects/4/settings/danger')
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    expect(screen.getByText(/It is kept until restored/)).toBeInTheDocument()
  })

  it('says so for a non-numeric id and asks for no keys', async () => {
    renderAt('/projects/abc/settings')
    expect(await screen.findByText(/There is no project abc\./)).toBeInTheDocument()
    expect(endpoints.keys).not.toHaveBeenCalled()
  })

  it('treats 0 and a fractional id the same way', async () => {
    renderAt('/projects/0/settings')
    expect(await screen.findByText(/There is no project 0\./)).toBeInTheDocument()
    renderAt('/projects/4.5/settings')
    expect(await screen.findByText(/There is no project 4\.5\./)).toBeInTheDocument()
    expect(endpoints.keys).not.toHaveBeenCalled()
  })

  it('shows a skeleton, not "No keys", while keys load', async () => {
    vi.spyOn(endpoints, 'keys').mockReturnValue(new Promise(() => {}))
    renderAt('/projects/4/settings/overview')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    expect(within(keys).queryByText(/No keys/)).not.toBeInTheDocument()
  })

  it('says why keys did not load, with Retry, not "No keys"', async () => {
    const user = userEvent.setup()
    const keysSpy = vi.spyOn(endpoints, 'keys').mockRejectedValueOnce(new Error('keys exploded'))
    renderAt('/projects/4/settings/overview')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    expect(await within(keys).findByText(/Couldn't load keys\. keys exploded/)).toBeInTheDocument()
    expect(within(keys).queryByText(/No keys/)).not.toBeInTheDocument()
    await user.click(within(keys).getByRole('button', { name: 'Retry' }))
    expect(await within(keys).findByRole('row', { name: /web/ })).toBeInTheDocument()
    expect(keysSpy).toHaveBeenCalledTimes(2)
  })

  it('says "No keys" only once loaded and empty', async () => {
    vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [] })
    renderAt('/projects/4/settings/overview')
    expect(await screen.findByText(/No keys: this project can receive nothing/)).toBeInTheDocument()
  })

  it('tolerates a registry answering no projects list', async () => {
    vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: null } as never)
    renderAt('/projects/4/settings')
    expect(await screen.findByText(/There is no project 4\./)).toBeInTheDocument()
  })

  it('keeps the key label in the disable dialog while it closes', async () => {
    const user = userEvent.setup()
    renderAt('/projects/4/settings/overview')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    await user.click(await within(keys).findByRole('button', { name: 'Disable web' }))
    expect(await screen.findByText('Disable web?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByText('Disable null?')).not.toBeInTheDocument()
  })
})

describe('Project tabs', () => {
  it('opens a project on its first tab, keeping the range', async () => {
    renderAt('/projects/7?range=30d')
    expect(await screen.findByRole('heading', { name: 'Views', level: 1 })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/dashboards/1?range=30d')
  })

  it('opens the remembered tab', async () => {
    writeLastTab(7, 2)
    renderAt('/projects/7')
    expect(await screen.findByRole('heading', { name: 'Product', level: 1 })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/dashboards/2')
  })

  it('remembers the tab it shows, per project', async () => {
    const user = userEvent.setup()
    renderAt('/projects/7/dashboards/1')
    await user.click(await screen.findByRole('tab', { name: 'Product' }))
    await screen.findByRole('heading', { name: 'Product', level: 1 })
    expect(readLastTab(7)).toBe(2)
    expect(readLastTab(4)).toBeNull()
  })

  it('falls back to the first tab when the remembered one is no longer a tab', async () => {
    writeLastTab(7, 999)
    renderAt('/projects/7')
    expect(await screen.findByRole('heading', { name: 'Views', level: 1 })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/dashboards/1')
  })

  it('decides on fresh tabs, not the ones cached from earlier in the session', async () => {
    // Tab 999 was removed elsewhere since the cache was filled.
    const client = testClient()
    client.setQueryData(['project-tabs', 7], { tabs: [...tabs, { dashboard_id: 999, title: 'Gone', owner: 'user', group_id: 999 }] })
    writeLastTab(7, 999)
    renderAt('/projects/7', client)
    expect(await screen.findByRole('heading', { name: 'Views', level: 1 })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/dashboards/1')
  })

  it('opens Settings when the dashboards list cannot be read, which a tab needs', async () => {
    vi.mocked(endpoints.dashboards).mockRejectedValue(new ApiError(500, 'database is locked', 'internal'))
    renderAt('/projects/7')
    await vi.waitFor(() => expect(location()).toBe('/projects/7/settings'))
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
  })

  it('opens Settings when the project has no tabs', async () => {
    vi.mocked(endpoints.projectTabs).mockResolvedValue({ tabs: [] })
    renderAt('/projects/7')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/settings')
  })

  it('reads the built-ins and your own as tabs, then Add tab, with Forms and Settings as buttons after them', async () => {
    renderAt('/projects/7/dashboards/1')
    await screen.findByRole('tab', { name: 'Mine' })
    const row = screen.getAllByRole('tab')
    expect(row.map((t) => t.textContent)).toEqual(['Views', 'Product', 'Mine'])
    const add = screen.getByRole('button', { name: 'Add tab' })
    expect(row.at(-1)!.compareDocumentPosition(add) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const forms = await screen.findByRole('link', { name: 'Forms, 3 new' })
    expect(add.compareDocumentPosition(forms) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(forms.compareDocumentPosition(screen.getByRole('link', { name: 'Settings' })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('titles the Settings page with its first tab, Overview', async () => {
    renderAt('/projects/7/settings')
    expect(await screen.findByRole('heading', { name: 'Overview', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page')
  })

  it('opens the Forms page at /forms on its first form, keeping the range, with the forms as tabs', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [form('contact'), form('survey')] })
    vi.spyOn(endpoints, 'submissions').mockResolvedValue(contactPage)
    renderAt('/projects/7/settings?range=30d')
    await user.click(await screen.findByRole('link', { name: /^Forms/ }))
    await vi.waitFor(() => expect(location()).toBe('/projects/7/forms/contact?range=30d'))
    expect(await screen.findByRole('table')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /^Forms/ })).toHaveAttribute('aria-current', 'page')
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['contact', 'survey'])
    expect(screen.getByRole('tab', { name: 'contact' })).toHaveAttribute('aria-selected', 'true')
    await user.click(screen.getByRole('tab', { name: 'survey' }))
    expect(location()).toBe('/projects/7/forms/survey?range=30d')
    expect(await screen.findByRole('heading', { name: 'survey' })).toBeInTheDocument()
    expect(endpoints.forms).toHaveBeenCalledWith(7, false)
  })

  it('lands /forms on the form last opened on the project', async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [form('contact'), form('survey')] })
    vi.spyOn(endpoints, 'submissions').mockResolvedValue(contactPage)
    const { unmount } = renderAt('/projects/7/forms/survey')
    await screen.findByRole('heading', { name: 'survey' })
    unmount()
    renderAt('/projects/7/forms?range=7d')
    await vi.waitFor(() => expect(location()).toBe('/projects/7/forms/survey?range=7d'))
  })

  it("keeps the notice for a form name that is not among the project's forms", async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [form('contact')] })
    renderAt('/projects/7/forms/gone')
    expect(await screen.findByText(/No form gone among this project's active forms/)).toBeInTheDocument()
    expect(screen.queryByRole('tab', { selected: true })).toBeNull()
  })

  it('says a project with no events yet has none, and links to its Overview', async () => {
    vi.mocked(endpoints.projectActivity).mockResolvedValue({ projects: [{ project_id: 7, last_event_day: null, new_submissions: 0, forms: 0, draft_forms: 0 }] })
    renderAt('/projects/7/dashboards/1?range=30d')
    const notice = (await screen.findByText('No events received yet')).closest('[role=status]') as HTMLElement
    expect(notice).not.toBeNull()
    expect(within(notice).getByRole('link', { name: 'Set up this project' })).toHaveAttribute('href', '/projects/7/settings/overview?range=30d')
  })

  it('shows no notice on the Forms pages, where a forms-only project has its work', async () => {
    vi.mocked(endpoints.projectActivity).mockResolvedValue({ projects: [{ project_id: 7, last_event_day: null, new_submissions: 0, forms: 0, draft_forms: 0 }] })
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [form('contact')] })
    vi.spyOn(endpoints, 'submissions').mockResolvedValue(contactPage)
    renderAt('/projects/7/forms')
    // Lands on the form, then loads its table.
    await screen.findByRole('table', {}, { timeout: 5000 })
    await vi.waitFor(() => expect(endpoints.projectActivity).toHaveBeenCalled())
    expect(screen.queryByText('No events received yet')).not.toBeInTheDocument()
  })

  it('shows no notice on Settings itself, nor for a project that has data', async () => {
    vi.mocked(endpoints.projectActivity).mockResolvedValue({
      projects: [
        { project_id: 7, last_event_day: null, new_submissions: 0, forms: 0, draft_forms: 0 },
        { project_id: 4, last_event_day: '2026-10-08', new_submissions: 0, forms: 0, draft_forms: 0 },
      ],
    })
    const { unmount } = renderAt('/projects/7/settings')
    await screen.findByRole('region', { name: 'Usage' })
    await vi.waitFor(() => expect(endpoints.projectActivity).toHaveBeenCalled())
    expect(screen.queryByText('No events received yet')).not.toBeInTheDocument()
    unmount()
    renderAt('/projects/4/dashboards/1')
    await screen.findByRole('heading', { name: 'Views', level: 1 })
    await vi.waitFor(() => expect(endpoints.projectActivity).toHaveBeenCalledTimes(2))
    expect(screen.queryByText('No events received yet')).not.toBeInTheDocument()
  })

  it('shows a dashboard tab with the project pinned on every widget that follows one', async () => {
    // The dashboard's own saved view names another project: the page ignores it.
    vi.mocked(endpoints.dashboard).mockImplementation(async (id) => ({ ...details[id], project_id: 1 }))
    renderAt('/projects/7/dashboards/1')
    expect(await screen.findByRole('heading', { name: 'Views', level: 1 })).toBeInTheDocument()
    expect(await screen.findByText('Visitors per day')).toBeInTheDocument()
    await vi.waitFor(() => expect(endpoints.widgetData).toHaveBeenCalled())
    const asked = vi.mocked(endpoints.widgetData).mock.calls
    for (const [id, q] of asked) {
      if (widgetsById.get(id)!.follows_project) expect(q.project_id).toBe(7)
    }
    expect(screen.queryByRole('button', { name: /^Project:/ })).not.toBeInTheDocument()
  })

  it("offers Share… and Download PNG on a dashboard tab's widgets, under the pinned project", async () => {
    const user = userEvent.setup()
    renderAt('/projects/7/dashboards/1')
    await vi.waitFor(() => expect(endpoints.widgetData).toHaveBeenCalled())
    await user.click((await screen.findAllByRole('button', { name: 'Widget actions' }))[0])
    expect(await screen.findByRole('menuitem', { name: 'Share…' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Download PNG' })).toBeInTheDocument()
  })

  it('offers Add widget and the card edits on a tab showing your own dashboard', async () => {
    const user = userEvent.setup()
    renderAt('/projects/7/dashboards/20')
    // The whole project page loads first: slow when the suite runs in parallel.
    expect(await screen.findByRole('button', { name: 'Add widget' }, { timeout: 5000 })).toBeInTheDocument()
    await user.click((await screen.findAllByRole('button', { name: 'Widget actions' }))[0])
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual([
      'Edit…',
      'Rename',
      'Duplicate',
      'Copy to dashboard…',
      'Share…',
      'Download PNG',
      'Archive',
    ])
  }, 20_000)

  it('offers no Add widget and no card edits on a built-in tab', async () => {
    const user = userEvent.setup()
    renderAt('/projects/7/dashboards/1')
    await vi.waitFor(() => expect(endpoints.widgetData).toHaveBeenCalled(), { timeout: 5000 })
    expect(screen.queryByRole('button', { name: 'Add widget' })).not.toBeInTheDocument()
    await user.click((await screen.findAllByRole('button', { name: 'Widget actions' }))[0])
    expect(screen.queryByRole('menuitem', { name: 'Edit…' })).not.toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: 'Duplicate' })).not.toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Copy to dashboard…' })).toBeInTheDocument()
  }, 20_000)

  it('keeps the range when switching tabs', async () => {
    const user = userEvent.setup()
    renderAt('/projects/7/dashboards/1?range=30d')
    await user.click(await screen.findByRole('tab', { name: 'Product' }))
    expect(await screen.findByRole('heading', { name: 'Product' })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/dashboards/2?range=30d')
    await user.click(screen.getByRole('link', { name: 'Settings' }))
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/settings?range=30d')
  })

  it("says a dashboard isn't a tab of the project, and adds it", async () => {
    const user = userEvent.setup()
    const add = vi.spyOn(endpoints, 'addProjectTab').mockResolvedValue({
      tabs: [...tabs, { dashboard_id: 1001, title: 'Spare', owner: 'user', group_id: 1001 }],
    })
    renderAt('/projects/7/dashboards/1001')
    expect(await screen.findByText("Spare isn't a tab of shop")).toBeInTheDocument()
    await user.click(screen.getByText('Add tab', { selector: 'button' }))
    expect(add).toHaveBeenCalledWith(7, { dashboard_id: 1001 })
    expect(await screen.findByRole('heading', { name: 'Spare' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Spare' })).toBeInTheDocument()
  })

  it('lists a removed built-in under Built-in, and adds it as a tab', async () => {
    const user = userEvent.setup()
    const add = vi.spyOn(endpoints, 'addProjectTab').mockResolvedValue({
      tabs: [tabs[0], tabs[1], { dashboard_id: 3, title: 'Users', owner: 'system', group_id: 1 }, tabs[2]],
    })
    renderAt('/projects/7/dashboards/1')
    await user.click(await screen.findByRole('button', { name: 'Add tab' }))
    const builtin = await screen.findByRole('group', { name: 'Built-in' })
    expect(within(builtin).getAllByRole('button').map((b) => b.textContent)).toEqual(['Users', 'Groups', 'Retention'])
    const own = screen.getByRole('group', { name: 'Your dashboards' })
    expect(within(own).getAllByRole('button').map((b) => b.textContent)).toEqual(['Launch week', 'Marketing', 'Funnel'])
    await user.click(within(builtin).getByRole('button', { name: 'Users' }))
    expect(add).toHaveBeenCalledWith(7, { dashboard_id: 3 })
    await vi.waitFor(() => expect(location()).toBe('/projects/7/dashboards/3'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('says when every dashboard is already a tab', async () => {
    const user = userEvent.setup()
    const all = dashboardsList().dashboards.filter((d) => !d.archived_at)
    vi.mocked(endpoints.projectTabs).mockResolvedValue({
      tabs: all.map((d) => ({ dashboard_id: d.dashboard_id, title: d.title, owner: d.owner, group_id: d.group_id })),
    })
    renderAt('/projects/7/dashboards/1')
    await user.click(await screen.findByRole('button', { name: 'Add tab' }))
    expect(await screen.findByText('Every dashboard is already a tab of this project')).toBeInTheDocument()
  })

  it('removes a tab from the project and lands on the one before it', async () => {
    const user = userEvent.setup()
    const remove = vi.spyOn(endpoints, 'removeProjectTab').mockResolvedValue({ tabs: [tabs[0], tabs[2]] })
    renderAt('/projects/7/dashboards/2?range=7d')
    await user.click(await screen.findByRole('button', { name: 'Tab actions' }))
    expect(screen.getByRole('menuitem', { name: 'Open as dashboard' })).toHaveAttribute('href', '/dashboards/2?project=7')
    await user.click(screen.getByRole('menuitem', { name: 'Remove from this project' }))
    expect(remove).toHaveBeenCalledWith(7, 2)
    await vi.waitFor(() => expect(location()).toBe('/projects/7/dashboards/1?range=7d'))
  })

  it('moves a tab right or left from its menu on a phone, naming the tab it goes after', async () => {
    const width = window.innerWidth
    window.innerWidth = 390
    try {
      const user = userEvent.setup()
      const ours: ProjectTab = { dashboard_id: 21, title: 'Ours', owner: 'user', group_id: 21 }
      vi.mocked(endpoints.projectTabs).mockResolvedValue({ tabs: [...tabs, ours] })
      const move = vi.spyOn(endpoints, 'moveProjectTab').mockResolvedValue({ tabs: [tabs[0], tabs[1], ours, tabs[2]] })
      renderAt('/projects/7/dashboards/20')
      await screen.findByRole('heading', { name: 'Mine', level: 1 })

      await user.click(screen.getByRole('button', { name: 'Tab actions' }))
      expect(screen.getByRole('menuitem', { name: 'Move left' })).not.toHaveAttribute('data-disabled')
      await user.click(screen.getByRole('menuitem', { name: 'Move right' }))
      expect(move).toHaveBeenCalledWith(7, 20, 21)
      await vi.waitFor(() =>
        expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Views', 'Product', 'Ours', 'Mine'])
      )

      move.mockResolvedValue({ tabs: [...tabs, ours] })
      await user.click(screen.getByRole('button', { name: 'Tab actions' }))
      expect(screen.getByRole('menuitem', { name: 'Move right' })).toHaveAttribute('data-disabled')
      await user.click(screen.getByRole('menuitem', { name: 'Move left' }))
      expect(move).toHaveBeenLastCalledWith(7, 20, 2)
      await vi.waitFor(() =>
        expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Views', 'Product', 'Mine', 'Ours'])
      )
    } finally {
      window.innerWidth = width
    }
  })

  it('moves a built-in tab too, first or after the last', async () => {
    const width = window.innerWidth
    window.innerWidth = 390
    try {
      const user = userEvent.setup()
      const move = vi.spyOn(endpoints, 'moveProjectTab').mockResolvedValue({ tabs })
      renderAt('/projects/7/dashboards/2')
      await screen.findByRole('heading', { name: 'Product', level: 1 })

      await user.click(screen.getByRole('button', { name: 'Tab actions' }))
      expect(screen.getByRole('menuitem', { name: 'Move right' })).not.toHaveAttribute('data-disabled')
      await user.click(screen.getByRole('menuitem', { name: 'Move left' }))
      expect(move).toHaveBeenCalledWith(7, 2, 0)

      await user.click(screen.getByRole('button', { name: 'Tab actions' }))
      await user.click(screen.getByRole('menuitem', { name: 'Move right' }))
      expect(move).toHaveBeenLastCalledWith(7, 2, 20)
    } finally {
      window.innerWidth = width
    }
  })

  it('removes the first tab and lands on the one after it', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'removeProjectTab').mockResolvedValue({ tabs: [tabs[1], tabs[2]] })
    renderAt('/projects/7/dashboards/1')
    await user.click(await screen.findByRole('button', { name: 'Tab actions' }))
    await user.click(screen.getByRole('menuitem', { name: 'Remove from this project' }))
    await vi.waitFor(() => expect(location()).toBe('/projects/7/dashboards/2'))
  })

  it('removes the only tab and lands on Settings', async () => {
    const user = userEvent.setup()
    vi.mocked(endpoints.projectTabs).mockResolvedValue({ tabs: [tabs[0]] })
    vi.spyOn(endpoints, 'removeProjectTab').mockResolvedValue({ tabs: [] })
    renderAt('/projects/7/dashboards/1?range=7d')
    await user.click(await screen.findByRole('button', { name: 'Tab actions' }))
    await user.click(screen.getByRole('menuitem', { name: 'Remove from this project' }))
    await vi.waitFor(() => expect(location()).toBe('/projects/7/settings?range=7d'))
  })

  it('never saves a dashboard view, whatever the range or tab', async () => {
    const user = userEvent.setup()
    renderAt('/projects/7/dashboards/1')
    await screen.findByRole('heading', { name: 'Views', level: 1 })
    await user.click(screen.getByRole('button', { name: /^Range:/ }))
    await user.click(await screen.findByRole('menuitemradio', { name: 'Last month' }))
    await vi.waitFor(() => expect(location()).toBe('/projects/7/dashboards/1?range=30d'))
    await user.click(screen.getByRole('tab', { name: 'Product' }))
    await screen.findByRole('heading', { name: 'Product' })
    expect(endpoints.saveView).not.toHaveBeenCalled()
  })
})
