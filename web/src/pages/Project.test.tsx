import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { ApiError, endpoints, type DashboardDetail, type ProjectTab } from '@/lib/api'
import { answerFor, dashboardsList, details, launchWeek, widgetsById } from '@/test/fixtures'
import { contactPage, form } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import Project, { ProjectIndex, SetupRedirect } from './Project'
import { readLastTab, writeLastTab } from '@/lib/last-tab'

const actions = {
  update: vi.fn(), archive: vi.fn(), restore: vi.fn(), issueKey: vi.fn(),
  disableKey: vi.fn(), enableKey: vi.fn(), create: vi.fn(), pending: false,
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
    { project_id: 4, name: 'econumo.com', allowed_origins: ['https://econumo.com'], attributes: ['plan'] },
    { project_id: 7, name: 'shop', allowed_origins: ['https://shop.example'] },
    { project_id: 3, name: 'legacy', archived: true, allowed_origins: [] },
  ] })
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [
    { project_id: 4, label: 'web', key: 'ak_web_123456789', state: 'active' },
    { project_id: 4, label: 'old', key: 'ak_old_123456789', state: 'disabled' },
  ] })
  vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [] })
  vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({
    project_id: 4, from: 'a', to: 'b', values_cap: 50, breakdowns_used: 1, breakdowns_max: 50, keys_total: 1,
    keys: [{ key: 'plan', events: 900, max_values: 3, received: true, declared: true }],
  })
  vi.spyOn(endpoints, 'capUsage').mockResolvedValue({ project_id: 4, from: 'a', to: 'b', dimensions: [] })
  vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs })
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

function renderAt(path: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/projects/:id" element={<ProjectIndex />} />
        <Route path="/projects/:id/settings" element={<Project />} />
        <Route path="/projects/:id/setup" element={<SetupRedirect />} />
        <Route path="/projects/:id/dashboards/:dashId" element={<Project />} />
        <Route path="/projects/:id/forms" element={<Project tab="forms" />} />
        <Route path="/projects/:id/forms/:name" element={<Project tab="forms" />} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>
  )
}

describe('Project', () => {
  it('heads the page with a breadcrumb back to the projects', async () => {
    renderAt('/projects/4/settings')
    const nav = await screen.findByRole('navigation', { name: 'breadcrumb' })
    expect(within(nav).getByRole('link', { name: 'Projects' })).toHaveAttribute('href', '/projects')
    expect(await within(nav).findByText('econumo.com')).toHaveAttribute('aria-current', 'page')
  })

  it('renames the project in place through PATCH, the name alone', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4/settings')
    await user.click(await screen.findByRole('button', { name: 'Rename' }))
    const field = screen.getByRole('textbox', { name: 'Project name' })
    await user.clear(field)
    await user.type(field, 'econumo{Enter}')
    expect(actions.update).toHaveBeenCalledWith(4, { name: 'econumo' })
  })

  it('shows the allowed origins and adds one through PATCH, never the name or attributes', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4/settings')
    const origins = await screen.findByRole('region', { name: 'Allowed origins' })
    expect(within(origins).getByText('https://econumo.com')).toBeInTheDocument()
    expect(within(origins).queryByText('plan')).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Details' })).not.toBeInTheDocument()
    await user.click(within(origins).getByRole('button', { name: 'Add origin' }))
    await user.type(screen.getByLabelText('Origin'), 'https://app.econumo.com')
    await user.click(screen.getByRole('button', { name: 'Add' }))
    expect(actions.update).toHaveBeenCalledWith(4, { allowed_origins: ['https://econumo.com', 'https://app.econumo.com'] })
  })

  it('places Breakdowns between Allowed origins and Ingest keys', async () => {
    renderAt('/projects/4/settings')
    await screen.findByRole('region', { name: 'Breakdowns' })
    const names = screen.getAllByRole('region').map((r) => r.getAttribute('aria-label'))
    expect(names.indexOf('Allowed origins')).toBeGreaterThan(-1)
    expect(names.indexOf('Allowed origins')).toBeLessThan(names.indexOf('Breakdowns'))
    expect(names.indexOf('Breakdowns')).toBeLessThan(names.indexOf('Ingest keys'))
  })

  it('lists the breakdowns and removes one through PATCH after confirming', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4/settings')
    const section = await screen.findByRole('region', { name: 'Breakdowns' })
    expect(await within(section).findByText('900 events · 3 values')).toBeInTheDocument()
    await user.click(within(section).getByRole('button', { name: 'Remove plan' }))
    expect(actions.update).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Remove breakdown' }))
    expect(actions.update).toHaveBeenCalledWith(4, { attributes: [] })
  })

  it('adds a breakdown through PATCH with the full new list', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({
      project_id: 4, from: 'a', to: 'b', values_cap: 50, breakdowns_used: 1, breakdowns_max: 50, keys_total: 2,
      keys: [
        { key: 'plan', events: 900, max_values: 3, received: true, declared: true },
        { key: 'tier', events: 20, max_values: 2, received: true, declared: false },
      ],
    })
    renderAt('/projects/4/settings')
    const section = await screen.findByRole('region', { name: 'Breakdowns' })
    await user.click(within(section).getByRole('button', { name: 'Add breakdown' }))
    await user.click(await screen.findByRole('radio', { name: /tier/ }))
    await user.click(screen.getByRole('button', { name: 'Add' }))
    expect(actions.update).toHaveBeenCalledWith(4, { attributes: ['plan', 'tier'] })
  })

  it('lists keys and disables one after confirming', async () => {
    const user = userEvent.setup()
    actions.disableKey.mockResolvedValue(true)
    renderAt('/projects/4/settings')
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
    renderAt('/projects/4/settings')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    await user.click(await within(keys).findByRole('button', { name: 'Enable old' }))
    expect(actions.enableKey).toHaveBeenCalledWith(4, 'old')
  })

  it('issues a key and shows it with its snippet', async () => {
    const user = userEvent.setup()
    actions.issueKey.mockResolvedValue({ key: 'ak_ios', snippet: 'twillingate.init(…)', status: 'issued' })
    renderAt('/projects/4/settings')
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
    renderAt('/projects/4/settings')
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
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
  })

  it('says so for an unknown project', async () => {
    renderAt('/projects/77/settings')
    expect(await screen.findByText(/There is no project 77\./)).toBeInTheDocument()
  })

  it('shows usage and cap impact over the last 7 days and refetches both when the range changes', async () => {
    const user = userEvent.setup()
    renderAt('/projects/4/settings')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(await screen.findByRole('region', { name: 'Cap impact' })).toBeInTheDocument()
    const first = vi.mocked(endpoints.usage).mock.calls.at(-1)![0]
    expect(first).toMatchObject({ project_id: 4 })
    // Opens on the last 7 days, today included.
    expect(screen.getByRole('button', { name: 'Range: Last week' })).toBeInTheDocument()
    expect((Date.parse(first.to!) - Date.parse(first.from!)) / 86_400_000).toBe(6)
    await user.click(screen.getByRole('button', { name: /^Range:/ }))
    await user.click(await screen.findByRole('menuitemradio', { name: 'Last month' }))
    await vi.waitFor(() => expect(vi.mocked(endpoints.usage).mock.calls.at(-1)![0]).not.toEqual(first))
    await vi.waitFor(() => expect(vi.mocked(endpoints.capUsage).mock.calls.at(-1)![1]).toEqual({
      from: vi.mocked(endpoints.usage).mock.calls.at(-1)![0].from, to: vi.mocked(endpoints.usage).mock.calls.at(-1)![0].to,
    }))
  })

  it('keeps "kept until restored" when the server names no purge window', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList())
    renderAt('/projects/4/settings')
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
    renderAt('/projects/4/settings')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    expect(within(keys).queryByText(/No keys/)).not.toBeInTheDocument()
  })

  it('says why keys did not load, with Retry, not "No keys"', async () => {
    const user = userEvent.setup()
    const keysSpy = vi.spyOn(endpoints, 'keys').mockRejectedValueOnce(new Error('keys exploded'))
    renderAt('/projects/4/settings')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    expect(await within(keys).findByText(/Couldn't load keys\. keys exploded/)).toBeInTheDocument()
    expect(within(keys).queryByText(/No keys/)).not.toBeInTheDocument()
    await user.click(within(keys).getByRole('button', { name: 'Retry' }))
    expect(await within(keys).findByRole('row', { name: /web/ })).toBeInTheDocument()
    expect(keysSpy).toHaveBeenCalledTimes(2)
  })

  it('says "No keys" only once loaded and empty', async () => {
    vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [] })
    renderAt('/projects/4/settings')
    expect(await screen.findByText(/No keys: this project can receive nothing/)).toBeInTheDocument()
  })

  it('tolerates a registry answering no projects list', async () => {
    vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: null } as never)
    renderAt('/projects/4/settings')
    expect(await screen.findByText(/There is no project 4\./)).toBeInTheDocument()
  })

  it('keeps the key label in the disable dialog while it closes', async () => {
    const user = userEvent.setup()
    renderAt('/projects/4/settings')
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

  it('opens Settings when the project has no tabs', async () => {
    vi.mocked(endpoints.projectTabs).mockResolvedValue({ tabs: [] })
    renderAt('/projects/7')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/settings')
  })

  it('opens Settings in reporting dev, which serves no project tabs', async () => {
    vi.mocked(endpoints.dashboards).mockResolvedValue(dashboardsList({ dev: true }))
    renderAt('/projects/7?range=30d')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/settings?range=30d')
    expect(endpoints.projectTabs).not.toHaveBeenCalled()
  })

  it('redirects /setup to /settings keeping the range', async () => {
    renderAt('/projects/7/setup?range=30d')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/settings?range=30d')
  })

  it('reads Settings, Forms, the built-ins, your own, then Add tab', async () => {
    renderAt('/projects/7/settings')
    await screen.findByRole('tab', { name: 'Mine' })
    const row = screen.getAllByRole('tab')
    expect(row.map((t) => t.textContent)).toEqual(['Settings', 'Forms', 'Views', 'Product', 'Mine'])
    const add = screen.getByRole('button', { name: 'Add tab' })
    expect(row.at(-1)!.compareDocumentPosition(add) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('opens the Forms tab at /forms, keeping the range, and a form inside it', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [form('contact')] })
    vi.spyOn(endpoints, 'submissions').mockResolvedValue(contactPage)
    renderAt('/projects/7/settings?range=30d')
    await user.click(await screen.findByRole('tab', { name: 'Forms' }))
    expect(location()).toBe('/projects/7/forms?range=30d')
    expect(await screen.findByRole('list', { name: 'Forms' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Forms' })).toHaveAttribute('aria-selected', 'true')
    await user.click(screen.getByRole('link', { name: /contact/ }))
    expect(location()).toBe('/projects/7/forms/contact?range=30d')
    expect(await screen.findByRole('table')).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Forms' })).toHaveAttribute('aria-selected', 'true')
    expect(endpoints.forms).toHaveBeenCalledWith(7, false)
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

  it('keeps the range when switching tabs', async () => {
    const user = userEvent.setup()
    renderAt('/projects/7/dashboards/1?range=30d')
    await user.click(await screen.findByRole('tab', { name: 'Product' }))
    expect(await screen.findByRole('heading', { name: 'Product' })).toBeInTheDocument()
    expect(location()).toBe('/projects/7/dashboards/2?range=30d')
    await user.click(screen.getByRole('tab', { name: 'Settings' }))
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
    renderAt('/projects/7/settings')
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
    renderAt('/projects/7/settings')
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
        expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Settings', 'Forms', 'Views', 'Product', 'Ours', 'Mine'])
      )

      move.mockResolvedValue({ tabs: [...tabs, ours] })
      await user.click(screen.getByRole('button', { name: 'Tab actions' }))
      expect(screen.getByRole('menuitem', { name: 'Move right' })).toHaveAttribute('data-disabled')
      await user.click(screen.getByRole('menuitem', { name: 'Move left' }))
      expect(move).toHaveBeenLastCalledWith(7, 20, 2)
      await vi.waitFor(() =>
        expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Settings', 'Forms', 'Views', 'Product', 'Mine', 'Ours'])
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

  it('in reporting dev, which serves no project tabs, asks for none and offers no tab writes', async () => {
    vi.mocked(endpoints.dashboards).mockResolvedValue(dashboardsList({ dev: true }))
    vi.mocked(endpoints.projectTabs).mockRejectedValue(new ApiError(404, 'not found'))
    renderAt('/projects/7/settings')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Settings'])
    expect(screen.queryByRole('button', { name: 'Add tab' })).not.toBeInTheDocument()
    expect(endpoints.projectTabs).not.toHaveBeenCalled()
  })

  it('in reporting dev, says a dashboard tab is not served there rather than failing', async () => {
    vi.mocked(endpoints.dashboards).mockResolvedValue(dashboardsList({ dev: true }))
    vi.mocked(endpoints.projectTabs).mockRejectedValue(new ApiError(404, 'not found'))
    renderAt('/projects/7/dashboards/1')
    expect(await screen.findByText('No project tabs in reporting dev')).toBeInTheDocument()
    expect(screen.queryByText('No such dashboard')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Tab actions' })).not.toBeInTheDocument()
    expect(endpoints.projectTabs).not.toHaveBeenCalled()
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
