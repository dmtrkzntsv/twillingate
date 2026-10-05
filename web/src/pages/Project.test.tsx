import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { endpoints } from '@/lib/api'
import { dashboardsList } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Project from './Project'

const actions = {
  update: vi.fn(), archive: vi.fn(), restore: vi.fn(), issueKey: vi.fn(),
  disableKey: vi.fn(), enableKey: vi.fn(), create: vi.fn(), pending: false,
}
vi.mock('@/hooks/use-project-actions', () => ({ useProjectActions: () => actions }))

beforeEach(() => {
  vi.restoreAllMocks()
  Object.values(actions).forEach((f) => typeof f === 'function' && f.mockReset())
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList({ purge_after_days: 30 }))
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [
    { project_id: 4, name: 'econumo.com', allowed_origins: ['https://econumo.com'], attributes: ['plan'] },
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
})

function renderAt(path: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Routes><Route path="/projects/:id" element={<Project />} /></Routes>
    </MemoryRouter>
  )
}

describe('Project', () => {
  it('heads the page with a breadcrumb back to the projects', async () => {
    renderAt('/projects/4')
    const nav = await screen.findByRole('navigation', { name: 'breadcrumb' })
    expect(within(nav).getByRole('link', { name: 'Projects' })).toHaveAttribute('href', '/projects')
    expect(await within(nav).findByText('econumo.com')).toHaveAttribute('aria-current', 'page')
  })

  it('renames the project in place through PATCH, the name alone', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4')
    await user.click(await screen.findByRole('button', { name: 'Rename' }))
    const field = screen.getByRole('textbox', { name: 'Project name' })
    await user.clear(field)
    await user.type(field, 'econumo{Enter}')
    expect(actions.update).toHaveBeenCalledWith(4, { name: 'econumo' })
  })

  it('shows the allowed origins and adds one through PATCH, never the name or attributes', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4')
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
    renderAt('/projects/4')
    await screen.findByRole('region', { name: 'Breakdowns' })
    const names = screen.getAllByRole('region').map((r) => r.getAttribute('aria-label'))
    expect(names.indexOf('Allowed origins')).toBeGreaterThan(-1)
    expect(names.indexOf('Allowed origins')).toBeLessThan(names.indexOf('Breakdowns'))
    expect(names.indexOf('Breakdowns')).toBeLessThan(names.indexOf('Ingest keys'))
  })

  it('lists the breakdowns and removes one through PATCH after confirming', async () => {
    const user = userEvent.setup()
    actions.update.mockResolvedValue(true)
    renderAt('/projects/4')
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
    renderAt('/projects/4')
    const section = await screen.findByRole('region', { name: 'Breakdowns' })
    await user.click(within(section).getByRole('button', { name: 'Add breakdown' }))
    await user.click(await screen.findByRole('radio', { name: /tier/ }))
    await user.click(screen.getByRole('button', { name: 'Add' }))
    expect(actions.update).toHaveBeenCalledWith(4, { attributes: ['plan', 'tier'] })
  })

  it('lists keys and disables one after confirming', async () => {
    const user = userEvent.setup()
    actions.disableKey.mockResolvedValue(true)
    renderAt('/projects/4')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    const row = within(keys).getByRole('row', { name: /web/ })
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
    renderAt('/projects/4')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    await user.click(within(keys).getByRole('button', { name: 'Enable old' }))
    expect(actions.enableKey).toHaveBeenCalledWith(4, 'old')
  })

  it('issues a key and shows it with its snippet', async () => {
    const user = userEvent.setup()
    actions.issueKey.mockResolvedValue({ key: 'ak_ios', snippet: 'twillingate.init(…)', status: 'issued' })
    renderAt('/projects/4')
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
    renderAt('/projects/4')
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    const purge = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: 'UTC' }).format(new Date(Date.now() + 30 * 86_400_000))
    expect(screen.getByText(new RegExp(`deleted on ${purge} \\(30 days\\)`))).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Archive project' }))
    expect(actions.archive).toHaveBeenCalledWith(4)
  })

  it('offers Restore on an archived project and still shows it', async () => {
    renderAt('/projects/3')
    expect(await screen.findByRole('button', { name: 'Restore' })).toBeInTheDocument()
    expect(screen.getByText('Archived')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Archive' })).not.toBeInTheDocument()
  })

  it('says so for an unknown project', async () => {
    renderAt('/projects/77')
    expect(await screen.findByText('No project 77')).toBeInTheDocument()
  })

  it('shows usage and cap impact and refetches both when the range changes', async () => {
    const user = userEvent.setup()
    renderAt('/projects/4')
    expect(await screen.findByRole('region', { name: 'Usage' })).toBeInTheDocument()
    expect(await screen.findByRole('region', { name: 'Cap impact' })).toBeInTheDocument()
    const first = vi.mocked(endpoints.usage).mock.calls.at(-1)![0]
    expect(first).toMatchObject({ project_id: 4 })
    await user.click(screen.getByRole('button', { name: /^Range:/ }))
    await user.click(await screen.findByRole('menuitemradio', { name: 'Last week' }))
    await vi.waitFor(() => expect(vi.mocked(endpoints.usage).mock.calls.at(-1)![0]).not.toEqual(first))
    await vi.waitFor(() => expect(vi.mocked(endpoints.capUsage).mock.calls.at(-1)![1]).toEqual({
      from: vi.mocked(endpoints.usage).mock.calls.at(-1)![0].from, to: vi.mocked(endpoints.usage).mock.calls.at(-1)![0].to,
    }))
  })

  it('keeps "kept until restored" when the server names no purge window', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList())
    renderAt('/projects/4')
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    expect(screen.getByText(/It is kept until restored/)).toBeInTheDocument()
  })

  it('says so for a non-numeric id and asks for no keys', async () => {
    renderAt('/projects/abc')
    expect(await screen.findByText('No project abc')).toBeInTheDocument()
    expect(endpoints.keys).not.toHaveBeenCalled()
  })

  it('treats 0 and a fractional id the same way', async () => {
    renderAt('/projects/0')
    expect(await screen.findByText('No project 0')).toBeInTheDocument()
    renderAt('/projects/4.5')
    expect(await screen.findByText('No project 4.5')).toBeInTheDocument()
    expect(endpoints.keys).not.toHaveBeenCalled()
  })

  it('shows a skeleton, not "No keys", while keys load', async () => {
    vi.spyOn(endpoints, 'keys').mockReturnValue(new Promise(() => {}))
    renderAt('/projects/4')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    expect(within(keys).queryByText(/No keys/)).not.toBeInTheDocument()
  })

  it('says why keys did not load, with Retry, not "No keys"', async () => {
    const user = userEvent.setup()
    const keysSpy = vi.spyOn(endpoints, 'keys').mockRejectedValueOnce(new Error('keys exploded'))
    renderAt('/projects/4')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    expect(await within(keys).findByText(/Couldn't load keys\. keys exploded/)).toBeInTheDocument()
    expect(within(keys).queryByText(/No keys/)).not.toBeInTheDocument()
    await user.click(within(keys).getByRole('button', { name: 'Retry' }))
    expect(await within(keys).findByRole('row', { name: /web/ })).toBeInTheDocument()
    expect(keysSpy).toHaveBeenCalledTimes(2)
  })

  it('says "No keys" only once loaded and empty', async () => {
    vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [] })
    renderAt('/projects/4')
    expect(await screen.findByText(/No keys: this project can receive nothing/)).toBeInTheDocument()
  })

  it('tolerates a registry answering no projects list', async () => {
    vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: null } as never)
    renderAt('/projects/4')
    expect(await screen.findByText('No project 4')).toBeInTheDocument()
  })

  it('keeps the key label in the disable dialog while it closes', async () => {
    const user = userEvent.setup()
    renderAt('/projects/4')
    const keys = await screen.findByRole('region', { name: 'Ingest keys' })
    await user.click(within(keys).getByRole('button', { name: 'Disable web' }))
    expect(await screen.findByText('Disable web?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByText('Disable null?')).not.toBeInTheDocument()
  })
})
