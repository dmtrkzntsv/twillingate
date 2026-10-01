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

  it('"Copy as a dashboard" calls duplicate(tab, {archiveSource: false})', async () => {
    mockApi()
    renderGallery()
    await screen.findByRole('link', { name: 'Product' })

    const row = screen.getByRole('link', { name: 'Product' }).closest('li')!
    await userEvent.click(within(row).getByRole('button', { name: 'Copy as a dashboard' }))

    expect(duplicate).toHaveBeenCalledWith(dashboards[1], { archiveSource: false })
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
