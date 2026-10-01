import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import type { DashboardActions } from '@/hooks/use-dashboard-actions'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { endpoints, type DashboardInfo } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import DashboardsGallery from './DashboardsGallery'

vi.mock('@/hooks/use-dashboard-actions', () => ({
  useDashboardActions: vi.fn(),
}))

const duplicate = vi.fn()
const archive = vi.fn()
const restore = vi.fn()

function info(dashboard_id: number, title: string, owner: 'system' | 'user', group_id: number, extra: Partial<DashboardInfo> = {}): DashboardInfo {
  return { dashboard_id, title, owner, group_id, widgets: 1, ...extra }
}

// Views/Product: a live system group. Reach: an archived, lone system
// group. Launch week: a live user dashboard, uninvolved. Old experiment:
// an archived, lone user dashboard. Marketing/Funnel: a user group where
// Funnel alone is archived, Marketing stays live.
const dashboards: DashboardInfo[] = [
  info(1, 'Views', 'system', 1),
  info(2, 'Product', 'system', 1),
  info(20, 'Reach', 'system', 20, { archived_at: '2026-09-15T00:00:00Z' }),
  info(10, 'Launch week', 'user', 10),
  info(11, 'Old experiment', 'user', 11, { archived_at: '2026-09-01T00:00:00Z' }),
  info(13, 'Marketing', 'user', 13),
  info(14, 'Funnel', 'user', 13, { archived_at: '2026-09-01T00:00:00Z' }),
]

function mockApi(purge_after_days?: number) {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards, purge_after_days })
}

function mockApiWith(list: DashboardInfo[]) {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards: list })
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

function renderGallery() {
  return renderWithProviders(
    <MemoryRouter>
      <DashboardsGallery />
    </MemoryRouter>
  )
}

describe('DashboardsGallery, System section', () => {
  it('shows Archive and "In the sidebar" for a live group, Restore and "Archived" for an archived one', async () => {
    mockApi()
    renderGallery()

    const views = (await screen.findByRole('heading', { name: 'Views' })).closest('li')!
    expect(within(views).getByText('In the sidebar')).toBeInTheDocument()
    expect(within(views).getByRole('button', { name: 'Archive' })).toBeInTheDocument()

    const reach = screen.getByRole('heading', { name: 'Reach' }).closest('li')!
    expect(within(reach).getByText('Archived')).toBeInTheDocument()
    expect(within(reach).getByRole('button', { name: 'Restore' })).toBeInTheDocument()
  })

  it('Archive calls archive(first, {wholeGroup: true})', async () => {
    mockApi()
    renderGallery()
    const views = (await screen.findByRole('heading', { name: 'Views' })).closest('li')!

    await userEvent.click(within(views).getByRole('button', { name: 'Archive' }))

    expect(archive).toHaveBeenCalledWith(dashboards[0], { wholeGroup: true })
  })

  it('Restore calls restore(first.dashboard_id, true)', async () => {
    mockApi()
    renderGallery()
    const reach = (await screen.findByRole('heading', { name: 'Reach' })).closest('li')!

    await userEvent.click(within(reach).getByRole('button', { name: 'Restore' }))

    expect(restore).toHaveBeenCalledWith(20, true)
  })

  it('lists each tab with a link to its dashboard and a "Copy as a dashboard" button', async () => {
    mockApi()
    renderGallery()

    const product = await screen.findByRole('link', { name: 'Product' })
    expect(product).toHaveAttribute('href', '/dashboards/2')
  })

  it('"Copy as a dashboard" calls duplicate(tab), archiving nothing', async () => {
    mockApi()
    renderGallery()
    await screen.findByRole('link', { name: 'Product' })

    const row = screen.getByRole('link', { name: 'Product' }).closest('li')!
    await userEvent.click(within(row).getByRole('button', { name: 'Copy as a dashboard' }))

    expect(duplicate).toHaveBeenCalledWith(dashboards[1])
  })

  it('shows Archive for a group with a live member even when its literal first is archived', async () => {
    const first = info(30, 'Reports', 'system', 30, { archived_at: '2026-09-10T00:00:00Z' })
    mockApiWith([first, info(31, 'Extra', 'system', 30)])
    renderGallery()

    const row = (await screen.findByRole('heading', { name: 'Reports' })).closest('li')!
    expect(within(row).getByText('In the sidebar')).toBeInTheDocument()
    await userEvent.click(within(row).getByRole('button', { name: 'Archive' }))

    expect(archive).toHaveBeenCalledWith(first, { wholeGroup: true })
  })
})

describe('DashboardsGallery, Archived section', () => {
  it('shows "deleted on 1 Oct" given archived_at plus purge_after_days', async () => {
    mockApi(30)
    renderGallery()

    const row = (await screen.findByText('Old experiment')).closest('li')!
    expect(within(row).getByText(/deleted on 1 Oct/)).toBeInTheDocument()
  })

  it('shows no "deleted on" line without purge_after_days', async () => {
    mockApi()
    renderGallery()

    const row = (await screen.findByText('Old experiment')).closest('li')!
    expect(within(row).queryByText(/deleted on/)).not.toBeInTheDocument()
  })

  it('names the group for an archived dashboard with a live sibling', async () => {
    mockApi()
    renderGallery()

    const row = (await screen.findByText('Funnel')).closest('li')!
    expect(within(row).getByText(/in Marketing/)).toBeInTheDocument()
  })

  it('never names a dashboard after itself when its whole group is archived', async () => {
    mockApiWith([
      info(13, 'Marketing', 'user', 13, { archived_at: '2026-09-01T00:00:00Z' }),
      info(14, 'Funnel', 'user', 13, { archived_at: '2026-09-02T00:00:00Z' }),
    ])
    renderGallery()

    const marketingRow = (await screen.findByText('Marketing')).closest('li')!
    expect(within(marketingRow).getByText(/in Funnel/)).toBeInTheDocument()
    expect(within(marketingRow).queryByText(/in Marketing/)).not.toBeInTheDocument()

    const funnelRow = screen.getByText('Funnel').closest('li')!
    expect(within(funnelRow).getByText(/in Marketing/)).toBeInTheDocument()
    expect(within(funnelRow).queryByText(/in Funnel/)).not.toBeInTheDocument()
  })

  it('names nothing for an archived dashboard with no siblings', async () => {
    mockApi()
    renderGallery()

    const row = (await screen.findByText('Old experiment')).closest('li')!
    expect(within(row).queryByText(/^in /)).not.toBeInTheDocument()
  })

  it('Restore calls restore(id) alone, no whole group', async () => {
    mockApi()
    renderGallery()
    const row = (await screen.findByText('Old experiment')).closest('li')!

    await userEvent.click(within(row).getByRole('button', { name: 'Restore' }))

    expect(restore).toHaveBeenCalledWith(11)
  })

  it('reads "Nothing archived." when there is nothing to restore', async () => {
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue({
      timezone: 'UTC',
      dashboards: [info(1, 'Views', 'system', 1), info(10, 'Launch week', 'user', 10)],
    })
    renderGallery()

    expect(await screen.findByText('Nothing archived.')).toBeInTheDocument()
  })
})

describe('DashboardsGallery, writes', () => {
  it('makes one copy from a double click: the buttons wait while an action runs', async () => {
    const actual = await vi.importActual<typeof import('@/hooks/use-dashboard-actions')>('@/hooks/use-dashboard-actions')
    vi.mocked(useDashboardActions).mockImplementation(actual.useDashboardActions)
    const copy = vi.spyOn(endpoints, 'duplicate').mockReturnValue(new Promise(() => {}))
    mockApi()
    renderGallery()
    const views = (await screen.findByRole('heading', { name: 'Views' })).closest('li')!
    const button = within(views).getAllByRole('button', { name: 'Copy as a dashboard' })[0]

    await userEvent.dblClick(button)

    expect(copy).toHaveBeenCalledTimes(1)
    expect(button).toBeDisabled()
    expect(within(views).getByRole('button', { name: 'Archive' })).toBeDisabled()
  })

  it('shows no Archive, Restore or Copy in reporting dev, which takes no writes', async () => {
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards, dev: true })
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    expect(screen.getByText('Old experiment')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^(Archive|Restore|Copy as a dashboard)$/ })).not.toBeInTheDocument()
  })
})
