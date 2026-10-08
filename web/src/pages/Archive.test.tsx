import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import type { DashboardActions } from '@/hooks/use-dashboard-actions'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { WidgetShareActions } from '@/hooks/use-widget-share-actions'
import { useWidgetShareActions } from '@/hooks/use-widget-share-actions'
import { endpoints, type DashboardInfo, type WidgetShare } from '@/lib/api'
import { form } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import Archive from './Archive'

vi.mock('@/hooks/use-dashboard-actions', () => ({
  useDashboardActions: vi.fn(),
}))

vi.mock('@/hooks/use-widget-share-actions', () => ({
  useWidgetShareActions: vi.fn(),
}))

const restoreShare = vi.fn()
const duplicate = vi.fn()
const archive = vi.fn()
const restore = vi.fn()
const setSidebar = vi.fn()

function info(dashboard_id: number, title: string, owner: 'system' | 'user', group_id: number, extra: Partial<DashboardInfo> = {}): DashboardInfo {
  return { dashboard_id, title, owner, group_id, widgets: 1, sidebar: true, project_tab: false, ...extra }
}

// Views/Product/Users/Groups/Retention: a 5-tab system group, hidden
// from the sidebar. Reports: a live system group, uninvolved. Launch week: a live
// user dashboard, uninvolved. Old experiment: an archived, lone user
// dashboard. Marketing/Funnel: a user group where Funnel alone is
// archived, Marketing stays live.
const dashboards: DashboardInfo[] = [
  info(1, 'Views', 'system', 1, { sidebar: false }),
  info(2, 'Product', 'system', 1, { sidebar: false }),
  info(3, 'Users', 'system', 1, { sidebar: false }),
  info(4, 'Groups', 'system', 1, { sidebar: false }),
  info(5, 'Retention', 'system', 1, { sidebar: false }),
  info(6, 'Reports', 'system', 6),
  info(10, 'Launch week', 'user', 10),
  info(11, 'Old experiment', 'user', 11, { archived_at: '2026-09-01T00:00:00Z' }),
  info(13, 'Marketing', 'user', 13),
  info(14, 'Funnel', 'user', 13, { archived_at: '2026-09-01T00:00:00Z' }),
]

function share(id: string, title: string, extra: Partial<WidgetShare> = {}): WidgetShare {
  return {
    id,
    url: `https://t.example/share/${id}`,
    image_url: `https://t.example/share/${id}.png`,
    image_2x_url: `https://t.example/share/${id}@2x.png`,
    widget_id: 42,
    dashboard_id: 1,
    dashboard_title: 'Overview',
    project_id: 7,
    project_name: 'blog',
    from: '2026-09-05',
    to: '2026-10-04',
    title,
    created_at: '2026-09-01T10:00:00Z',
    archive_at: '2026-10-05T10:00:00Z',
    archived_at: '2026-10-05T10:00:00Z',
    caption_project: true,
    caption_range: true,
    ...extra,
  }
}

const archivedShares: WidgetShare[] = [
  share('0190a0a0-0000-7000-8000-000000000001', 'Visitors', { archived_at: '2026-10-05T10:00:00Z' }),
  share('0190a0a0-0000-7000-8000-000000000002', 'Top pages', { project_name: 'docs', archived_at: '2026-10-01T10:00:00Z' }),
]

function mockShares(shares: WidgetShare[] = []) {
  vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({ shares })
  vi.spyOn(endpoints, 'widgetShareImage').mockResolvedValue(new Blob(['png'], { type: 'image/png' }))
}

function mockApi(purge_after_days?: number) {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards, purge_after_days })
}

function mockApiWith(list: DashboardInfo[], purge_after_days?: number) {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards: list, purge_after_days })
}

beforeEach(() => {
  vi.clearAllMocks()
  URL.createObjectURL = vi.fn((b: Blob) => `blob:thumb-${b.size}`)
  URL.revokeObjectURL = vi.fn()
  mockShares()
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [] })
  vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [] })
  vi.mocked(useWidgetShareActions).mockReturnValue({
    create: vi.fn(),
    setArchiveAfter: vi.fn(),
    archive: vi.fn(),
    restore: restoreShare,
    pending: false,
  } satisfies WidgetShareActions)
  vi.mocked(useDashboardActions).mockReturnValue({
    duplicate,
    archive,
    restore,
    setSidebar,
    move: vi.fn(),
    renameGroup: vi.fn(),
    pending: false,
  } satisfies DashboardActions)
})

function renderArchive() {
  return renderWithProviders(
    <MemoryRouter>
      <Archive />
    </MemoryRouter>
  )
}

describe('Archive, description', () => {
  it('names the purge window when purge_after_days is set', async () => {
    mockApi(30)
    renderArchive()

    expect(await screen.findByText(/deleted after 30 days/)).toBeInTheDocument()
  })

  it('says nothing about a purge window without purge_after_days', async () => {
    mockApi()
    renderArchive()

    await screen.findByText('Archived dashboards are out of the sidebar.', { exact: false })
    expect(screen.queryByText(/deleted after/)).not.toBeInTheDocument()
  })
})

/** The group card named `name` on the page (not in the sidebar). */
async function group(name: string) {
  await screen.findByRole('heading', { name: 'Archive' })
  return within(screen.getByRole('main')).findByRole('listitem', { name })
}

describe('Archive, a lone dashboard', () => {
  it('shows its group_title in place of its own title, still linking to the dashboard', async () => {
    mockApiWith(dashboards.map((d) => (d.dashboard_id === 11 ? { ...d, group_title: 'Retired ideas' } : d)))
    renderArchive()

    const link = await screen.findByRole('link', { name: 'Retired ideas' })
    expect(link).toHaveAttribute('href', '/dashboards/11')
    expect(screen.queryByRole('link', { name: 'Old experiment' })).not.toBeInTheDocument()
  })

  it('is one row linking to its page', async () => {
    mockApi()
    renderArchive()

    const link = await screen.findByRole('link', { name: 'Old experiment' })
    expect(link).toHaveAttribute('href', '/dashboards/11')
    expect(within(link.closest('li')!).queryByText(/tabs/)).not.toBeInTheDocument()
  })

  it('shows "deleted on 1 Oct" given archived_at plus purge_after_days', async () => {
    mockApi(30)
    renderArchive()

    const row = (await screen.findByText('Old experiment')).closest('li')!
    expect(within(row).getByText(/deleted on 1 Oct/)).toBeInTheDocument()
  })

  it('shows no "deleted on" line without purge_after_days', async () => {
    mockApi()
    renderArchive()

    const row = (await screen.findByText('Old experiment')).closest('li')!
    expect(within(row).queryByText(/deleted on/)).not.toBeInTheDocument()
  })

  it('Restore calls restore(id) alone, no whole group', async () => {
    mockApi()
    renderArchive()
    const row = (await screen.findByText('Old experiment')).closest('li')!

    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))

    expect(restore).toHaveBeenCalledWith(11)
  })
})

describe('Archive, a user group', () => {
  it('is one card named by its first live tab, listing every tab', async () => {
    mockApi()
    renderArchive()

    const card = await group('Marketing')
    expect(within(card).getByText(/2 tabs · 1 archived/)).toBeInTheDocument()
    expect(within(card).getByRole('link', { name: 'Marketing' })).toHaveAttribute('href', '/dashboards/13')
    expect(within(card).getByRole('link', { name: 'Funnel' })).toHaveAttribute('href', '/dashboards/14')
  })

  it('is named by its group_title when it has one', async () => {
    mockApiWith(
      dashboards.map((d) => (d.group_id === 13 ? { ...d, group_title: 'Team metrics' } : d))
    )
    renderArchive()

    const card = await group('Team metrics')
    expect(within(card).getByRole('link', { name: 'Marketing' })).toHaveAttribute('href', '/dashboards/13')
  })

  it('marks the live tab as in the sidebar, with no Restore', async () => {
    mockApi()
    renderArchive()

    const card = await group('Marketing')
    const live = within(card).getByRole('link', { name: 'Marketing' }).closest('li')!
    expect(within(live).getByText('in the sidebar')).toBeInTheDocument()
    expect(within(live).queryByRole('button')).not.toBeInTheDocument()
  })

  it('restores one archived tab on its own', async () => {
    mockApi(30)
    renderArchive()

    const card = await group('Marketing')
    const tab = within(card).getByRole('link', { name: 'Funnel' }).closest('li')!
    expect(within(tab).getByText(/deleted on 1 Oct/)).toBeInTheDocument()
    await userEvent.click(within(tab).getByRole('button', { name: 'Restore' }))

    expect(restore).toHaveBeenCalledWith(14)
  })

  it('offers Restore all only with two or more archived tabs', async () => {
    mockApi()
    renderArchive()

    const card = await group('Marketing')
    expect(within(card).queryByRole('button', { name: 'Restore all' })).not.toBeInTheDocument()
  })

  it('Restore all restores the whole group', async () => {
    mockApiWith([
      info(13, 'Marketing', 'user', 13, { archived_at: '2026-09-01T00:00:00Z' }),
      info(14, 'Funnel', 'user', 13, { archived_at: '2026-09-02T00:00:00Z' }),
    ])
    renderArchive()

    const card = await group('Marketing')
    expect(within(card).getByText(/2 tabs · all archived/)).toBeInTheDocument()
    expect(within(card).getAllByRole('button', { name: 'Restore' })).toHaveLength(2)
    await userEvent.click(within(card).getByRole('button', { name: 'Restore all' }))

    expect(restore).toHaveBeenCalledWith(13, true)
  })
})

describe('Archive, system groups', () => {
  it('leaves out every system group, hidden or live: a hidden one comes back from the gallery', async () => {
    mockApi()
    renderArchive()

    await group('Marketing')
    // The sidebar links to live system dashboards; scope to the page content.
    const main = within(screen.getByRole('main'))
    for (const name of ['Views', 'Product', 'Reports']) expect(main.queryByRole('link', { name })).not.toBeInTheDocument()
    expect(main.queryByText('System')).not.toBeInTheDocument()
    expect(main.getByText(/A hidden built-in dashboard comes back from Gallery › Dashboards/)).toBeInTheDocument()
  })

  it('reads "Nothing archived." when only a system group is', async () => {
    mockApiWith([info(1, 'Views', 'system', 1, { sidebar: false })])
    renderArchive()

    expect(await screen.findByText('Nothing archived.')).toBeInTheDocument()
  })
})

describe('Archive, empty state', () => {
  it('reads "Nothing archived." when nothing is', async () => {
    mockApiWith([info(1, 'Reports', 'system', 1), info(10, 'Launch week', 'user', 10)])
    renderArchive()

    expect(await screen.findByText('Nothing archived.')).toBeInTheDocument()
    // The sidebar has its own "Yours" group label; scope to the page content.
    const main = within(screen.getByRole('main'))
    expect(main.queryByText('Yours')).not.toBeInTheDocument()
    expect(main.queryByText('System')).not.toBeInTheDocument()
  })
})

describe('Archive, writes', () => {
  it('shows no Restore in reporting dev, which takes no writes', async () => {
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards, dev: true })
    renderArchive()

    await screen.findByText('Old experiment')
    expect(within(screen.getByRole('main')).queryByRole('button', { name: /Restore/ })).not.toBeInTheDocument()
  })
})

describe('Archive, shares', () => {
  it('asks only for the archived ones', async () => {
    mockApi()
    renderArchive()

    await screen.findByText('Old experiment')
    expect(endpoints.widgetShares).toHaveBeenCalledWith({ state: 'archived' })
  })

  it('lists each with its thumbnail, title, project and purge date from archived_at', async () => {
    mockApi(30)
    mockShares(archivedShares)
    renderArchive()

    const heading = await screen.findByRole('heading', { level: 2, name: 'Shares' })
    const section = heading.closest('section')!
    const rows = within(section).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('Visitors')).toBeInTheDocument()
    expect(within(rows[0]).getByText('blog')).toBeInTheDocument()
    expect(within(rows[0]).getByText(/^archived · deleted on/)).toHaveTextContent('archived · deleted on 4 Nov')
    // The date stays on one line: never "4" at one line's end and "Nov" at the next's start.
    expect(within(rows[0]).getByText('4 Nov')).toHaveClass('whitespace-nowrap')
    const thumb = await within(rows[0]).findByRole('img', { name: 'Shared image of Visitors' })
    expect(thumb).toHaveAttribute('src', 'blob:thumb-3')
    expect(endpoints.widgetShareImage).toHaveBeenCalledWith(archivedShares[0].id)
    expect(within(rows[1]).getByText('Top pages')).toBeInTheDocument()
    expect(within(rows[1]).getByText('docs')).toBeInTheDocument()
    expect(within(rows[1]).getByText(/^archived · deleted on/)).toHaveTextContent('archived · deleted on 31 Oct')
  })

  it('keeps a blank tile for a thumbnail that cannot be fetched', async () => {
    mockApi(30)
    mockShares(archivedShares)
    vi.mocked(endpoints.widgetShareImage).mockImplementation(async (id) => {
      if (id === archivedShares[0].id) throw new Error('gone')
      return new Blob(['png'])
    })
    renderArchive()

    expect(await screen.findByRole('img', { name: 'Shared image of Top pages' })).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: 'Shared image of Visitors' })).not.toBeInTheDocument()
  })

  it('says just "archived" without purge_after_days', async () => {
    mockApi()
    mockShares(archivedShares)
    renderArchive()

    const rows = within((await screen.findByRole('heading', { name: 'Shares' })).closest('section')!).getAllByRole('listitem')
    expect(within(rows[0]).getByText('archived')).toBeInTheDocument()
    expect(within(rows[0]).queryByText(/deleted on/)).not.toBeInTheDocument()
  })

  it('Restore asks Archive after, one month by default, and restores with it', async () => {
    mockApi(30)
    mockShares(archivedShares)
    restoreShare.mockResolvedValue(archivedShares[0])
    renderArchive()
    const row = (await screen.findByText('Visitors')).closest('li')!

    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))

    const dialog = await screen.findByRole('dialog', { name: 'Restore share' })
    expect(within(dialog).getByRole('combobox', { name: 'Archive after' })).toHaveValue('30d')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Restore' }))

    expect(restoreShare).toHaveBeenCalledWith(archivedShares[0].id, '30d')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('names the share in the dialog, and keeps its title while the dialog closes', async () => {
    mockApi(30)
    mockShares(archivedShares)
    restoreShare.mockResolvedValue(archivedShares[0])
    renderArchive()
    const row = (await screen.findByText('Visitors')).closest('li')!

    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))
    const dialog = await screen.findByRole('dialog', { name: 'Restore share' })
    expect(within(dialog).getByText('Visitors answers at its old link again.')).toBeInTheDocument()
  })

  it('restores with the date picked', async () => {
    mockApi(30)
    mockShares(archivedShares)
    restoreShare.mockResolvedValue(archivedShares[1])
    renderArchive()
    const row = (await screen.findByText('Top pages')).closest('li')!

    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))
    const dialog = await screen.findByRole('dialog', { name: 'Restore share' })
    await userEvent.selectOptions(within(dialog).getByRole('combobox', { name: 'Archive after' }), 'project')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Restore' }))

    expect(restoreShare).toHaveBeenCalledWith(archivedShares[1].id, 'project')
  })

  it('keeps the dialog open when the restore fails', async () => {
    mockApi(30)
    mockShares(archivedShares)
    restoreShare.mockResolvedValue(undefined)
    renderArchive()
    const row = (await screen.findByText('Visitors')).closest('li')!

    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))
    const dialog = await screen.findByRole('dialog', { name: 'Restore share' })
    await userEvent.click(within(dialog).getByRole('button', { name: 'Restore' }))

    expect(restoreShare).toHaveBeenCalled()
    expect(screen.getByRole('dialog', { name: 'Restore share' })).toBeInTheDocument()
  })

  it('opens a fresh dialog at one month after a cancelled one', async () => {
    mockApi(30)
    mockShares(archivedShares)
    renderArchive()
    const row = (await screen.findByText('Visitors')).closest('li')!

    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))
    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Archive after' }), '7d')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))

    expect(await screen.findByRole('combobox', { name: 'Archive after' })).toHaveValue('30d')
  })

  it('has no Shares heading when none is archived', async () => {
    mockApi()
    renderArchive()

    await screen.findByText('Old experiment')
    expect(screen.queryByRole('heading', { name: 'Shares' })).not.toBeInTheDocument()
  })

  it('does not flash "Nothing archived." while the dashboards are loading', async () => {
    vi.spyOn(endpoints, 'dashboards').mockReturnValue(new Promise(() => {}))
    renderArchive()

    await screen.findByRole('heading', { name: 'Archive' })
    await waitFor(() => expect(endpoints.dashboards).toHaveBeenCalled())
    expect(screen.queryByText('Nothing archived.')).not.toBeInTheDocument()
  })

  it('does not flash "Nothing archived." while the shares are loading', async () => {
    mockApiWith([info(10, 'Launch week', 'user', 10)])
    let resolve: (v: { shares: WidgetShare[] }) => void = () => {}
    vi.spyOn(endpoints, 'widgetShares').mockReturnValue(new Promise((r) => (resolve = r)))
    renderArchive()

    await screen.findByRole('heading', { name: 'Archive' })
    await waitFor(() => expect(endpoints.widgetShares).toHaveBeenCalled())
    expect(screen.queryByText('Nothing archived.')).not.toBeInTheDocument()
    resolve({ shares: [] })
    expect(await screen.findByText('Nothing archived.')).toBeInTheDocument()
  })

  it('says archived shares answer 404 until restored', async () => {
    mockApi()
    renderArchive()

    expect(await screen.findByText(/Archived shares answer 404 until restored\./)).toBeInTheDocument()
  })

  it('reads "Nothing archived." only when no dashboard and no share is', async () => {
    mockApiWith([info(10, 'Launch week', 'user', 10)])
    mockShares(archivedShares)
    renderArchive()

    await screen.findByRole('heading', { name: 'Shares' })
    expect(screen.queryByText('Nothing archived.')).not.toBeInTheDocument()
  })

  it('asks for no shares in reporting dev, which serves none, and offers no Restore', async () => {
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards, dev: true })
    mockShares(archivedShares)
    renderArchive()

    await screen.findByText('Old experiment')
    expect(endpoints.widgetShares).not.toHaveBeenCalled()
    expect(screen.queryByRole('heading', { name: 'Shares' })).not.toBeInTheDocument()
    expect(within(screen.getByRole('main')).queryByRole('button', { name: /Restore/ })).not.toBeInTheDocument()
  })
})

describe('Archive, forms', () => {
  it("lists each live project's archived forms with the project, the purge date and Restore", async () => {
    const user = userEvent.setup()
    mockApi(30)
    vi.mocked(endpoints.projects).mockResolvedValue({ projects: [
      { project_id: 4, name: 'shop', allowed_origins: [] },
      { project_id: 5, name: 'blog', allowed_origins: [] },
      { project_id: 3, name: 'legacy', archived: true, allowed_origins: [] },
    ] })
    vi.mocked(endpoints.forms).mockImplementation(async (id, archived) => ({
      action_base: '',
      forms: id === 4 && archived ? [form('contact', { archived: true, archived_at: '2026-09-01T00:00:00Z' })] : [],
    }))
    const restoreForm = vi.spyOn(endpoints, 'restoreForm').mockResolvedValue({ status: 'restored' })
    renderArchive()
    const section = await screen.findByRole('region', { name: 'Forms' })
    const row = within(section).getByRole('listitem')
    expect(within(row).getByText('contact')).toBeInTheDocument()
    expect(within(row).getByText('shop')).toBeInTheDocument()
    expect(within(row).getByText(/archived · deleted on/)).toBeInTheDocument()
    expect(within(row).getByText('1 Oct')).toBeInTheDocument()
    expect(endpoints.forms).toHaveBeenCalledWith(4, true)
    expect(endpoints.forms).toHaveBeenCalledWith(5, true)
    expect(endpoints.forms).not.toHaveBeenCalledWith(3, true)
    await user.click(within(row).getByRole('button', { name: 'Restore' }))
    await waitFor(() => expect(restoreForm).toHaveBeenCalledWith(4, 'contact'))
  })

  it('has no Forms section when none is archived', async () => {
    mockApiWith([info(10, 'Launch week', 'user', 10)])
    vi.mocked(endpoints.projects).mockResolvedValue({ projects: [{ project_id: 4, name: 'shop', allowed_origins: [] }] })
    renderArchive()
    await screen.findByText('Nothing archived.')
    expect(screen.queryByRole('region', { name: 'Forms' })).toBeNull()
  })
})
