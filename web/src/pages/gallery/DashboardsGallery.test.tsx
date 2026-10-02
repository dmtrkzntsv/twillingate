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
const restore = vi.fn()

function info(dashboard_id: number, title: string, owner: 'system' | 'user', group_id: number, extra: Partial<DashboardInfo> = {}): DashboardInfo {
  return { dashboard_id, title, owner, group_id, widgets: 1, ...extra }
}

// Views/Product: a live system group. Reach: an archived, lone system
// group — still a template here, archive state is irrelevant. Launch
// week: a live user dashboard, left out entirely (not a template).
const dashboards: DashboardInfo[] = [
  info(1, 'Views', 'system', 1),
  info(2, 'Product', 'system', 1),
  info(20, 'Reach', 'system', 20, { archived_at: '2026-09-15T00:00:00Z' }),
  info(10, 'Launch week', 'user', 10),
]

function mockApi() {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(useDashboardActions).mockReturnValue({
    duplicate,
    archive: vi.fn(),
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

describe('DashboardsGallery, templates', () => {
  it('lists a live and an archived system group identically, with no badge, Archive or Restore', async () => {
    mockApi()
    renderGallery()

    const views = (await screen.findByRole('heading', { name: 'Views' })).closest('li')!
    expect(within(views).queryByText('In the sidebar')).not.toBeInTheDocument()
    expect(within(views).queryByRole('button', { name: 'Archive' })).not.toBeInTheDocument()

    const reach = screen.getByRole('heading', { name: 'Reach' }).closest('li')!
    expect(within(reach).queryByText('Archived')).not.toBeInTheDocument()
    expect(within(reach).queryByRole('button', { name: 'Restore' })).not.toBeInTheDocument()
  })

  it('leaves out a live user dashboard: not a template', async () => {
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    // The sidebar legitimately links to Launch week; scope to the page content.
    const main = within(screen.getByRole('main'))
    expect(main.queryByText('Launch week')).not.toBeInTheDocument()
  })

  it('shows each group as one row: its first tab\'s title, opening that tab, and its tabs listed', async () => {
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    // The sidebar links to Views too; scope to the page content.
    const main = within(screen.getByRole('main'))
    const views = main.getByRole('link', { name: 'Views' })
    expect(views).toHaveAttribute('href', '/dashboards/1')
    const row = views.closest('li')!
    expect(within(row).getByText('2 tabs · Views, Product')).toBeInTheDocument()
    expect(within(row).queryByRole('link', { name: 'Product' })).not.toBeInTheDocument()
    // A lone-tab group lists no tabs.
    const reach = main.getByRole('link', { name: 'Reach' }).closest('li')!
    expect(within(reach).queryByText(/tabs/)).not.toBeInTheDocument()
  })

  it('duplicates the whole group from the row\'s "…" menu', async () => {
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    await userEvent.click(within(screen.getByRole('main')).getByRole('button', { name: 'Views actions' }))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Duplicate dashboard'])
    await userEvent.click(screen.getByRole('menuitem', { name: 'Duplicate dashboard' }))

    expect(duplicate).toHaveBeenCalledWith(dashboards[0], { wholeGroup: true })
  })

  it('makes no copy while another action runs: Duplicate waits', async () => {
    vi.mocked(useDashboardActions).mockReturnValue({
      duplicate,
      archive: vi.fn(),
      restore,
      move: vi.fn(),
      pending: true,
    } satisfies DashboardActions)
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    await userEvent.click(within(screen.getByRole('main')).getByRole('button', { name: 'Views actions' }))
    expect(screen.getByRole('menuitem', { name: 'Duplicate dashboard' })).toHaveAttribute('data-disabled')
  })

  it('shows no "…" menu in reporting dev, which takes no writes', async () => {
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards, dev: true })
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    expect(within(screen.getByRole('main')).queryByRole('button', { name: 'Views actions' })).not.toBeInTheDocument()
  })
})
