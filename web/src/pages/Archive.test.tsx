import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import type { DashboardActions } from '@/hooks/use-dashboard-actions'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { endpoints, type DashboardInfo } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import Archive from './Archive'

vi.mock('@/hooks/use-dashboard-actions', () => ({
  useDashboardActions: vi.fn(),
}))

const duplicate = vi.fn()
const archive = vi.fn()
const restore = vi.fn()

function info(dashboard_id: number, title: string, owner: 'system' | 'user', group_id: number, extra: Partial<DashboardInfo> = {}): DashboardInfo {
  return { dashboard_id, title, owner, group_id, widgets: 1, ...extra }
}

// Views/Product/Users/Groups/Retention: a 5-tab system group, archived
// whole. Reports: a live system group, uninvolved. Launch week: a live
// user dashboard, uninvolved. Old experiment: an archived, lone user
// dashboard. Marketing/Funnel: a user group where Funnel alone is
// archived, Marketing stays live.
const dashboards: DashboardInfo[] = [
  info(1, 'Views', 'system', 1, { archived_at: '2026-09-15T00:00:00Z' }),
  info(2, 'Product', 'system', 1, { archived_at: '2026-09-15T00:00:00Z' }),
  info(3, 'Users', 'system', 1, { archived_at: '2026-09-15T00:00:00Z' }),
  info(4, 'Groups', 'system', 1, { archived_at: '2026-09-15T00:00:00Z' }),
  info(5, 'Retention', 'system', 1, { archived_at: '2026-09-15T00:00:00Z' }),
  info(6, 'Reports', 'system', 6),
  info(10, 'Launch week', 'user', 10),
  info(11, 'Old experiment', 'user', 11, { archived_at: '2026-09-01T00:00:00Z' }),
  info(13, 'Marketing', 'user', 13),
  info(14, 'Funnel', 'user', 13, { archived_at: '2026-09-01T00:00:00Z' }),
]

function mockApi(purge_after_days?: number) {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards, purge_after_days })
}

function mockApiWith(list: DashboardInfo[], purge_after_days?: number) {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards: list, purge_after_days })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(useDashboardActions).mockReturnValue({
    duplicate,
    archive,
    restore,
    move: vi.fn(),
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

  it('restores a lone system dashboard with its group', async () => {
    mockApiWith([info(6, 'Reports', 'system', 6, { archived_at: '2026-09-15T00:00:00Z' })])
    renderArchive()
    const row = (await screen.findByRole('link', { name: 'Reports' })).closest('li')!

    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))

    expect(restore).toHaveBeenCalledWith(6, true)
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

describe('Archive, a system group', () => {
  it('is one card listing its tabs, never deleted', async () => {
    mockApi()
    renderArchive()

    const card = await group('Views')
    expect(within(card).getByText(/5 tabs · all archived/)).toBeInTheDocument()
    expect(within(card).getAllByRole('link')).toHaveLength(5)
    expect(within(card).getByRole('link', { name: 'Users' })).toHaveAttribute('href', '/dashboards/3')
    expect(within(card).getAllByText(/never deleted/)).toHaveLength(5)
  })

  it('leaves out a live system group', async () => {
    mockApi()
    renderArchive()

    await group('Views')
    // Reports (live) is still a legitimate sidebar link; scope to the
    // page content so that one does not make this a false negative.
    const main = within(screen.getByRole('main'))
    expect(main.queryByRole('link', { name: 'Reports' })).not.toBeInTheDocument()
  })

  it('restores only whole: Restore group, and no button per tab', async () => {
    mockApi()
    renderArchive()

    const card = await group('Views')
    expect(within(card).queryByRole('button', { name: 'Restore' })).not.toBeInTheDocument()
    await userEvent.click(within(card).getByRole('button', { name: 'Restore group' }))

    expect(restore).toHaveBeenCalledWith(1, true)
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
