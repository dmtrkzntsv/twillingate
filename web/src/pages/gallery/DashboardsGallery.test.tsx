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
    renameGroup: vi.fn(),
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
  it('lists a live and a hidden system group alike, the hidden one marked hidden', async () => {
    mockApi()
    renderGallery()

    const views = (await screen.findByRole('heading', { name: 'Views' })).closest('li')!
    expect(within(views).queryByText(/hidden/)).not.toBeInTheDocument()
    expect(within(views).queryByRole('button', { name: /Archive|Restore|Hide/ })).not.toBeInTheDocument()

    const reach = screen.getByRole('heading', { name: 'Reach' }).closest('li')!
    expect(within(reach).getByText('1 widget · hidden')).toBeInTheDocument()
    expect(within(reach).queryByText(/archived/i)).not.toBeInTheDocument()
  })

  it('shows a hidden group in the sidebar again from its "…" menu, and offers that only there', async () => {
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    const main = within(screen.getByRole('main'))
    await userEvent.click(main.getByRole('button', { name: 'Views actions' }))
    expect(screen.queryByRole('menuitem', { name: 'Show in sidebar' })).not.toBeInTheDocument()
    await userEvent.keyboard('{Escape}')

    await userEvent.click(main.getByRole('button', { name: 'Reach actions' }))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Show in sidebar', 'Duplicate dashboard'])
    await userEvent.click(screen.getByRole('menuitem', { name: 'Show in sidebar' }))

    expect(restore).toHaveBeenCalledWith(20, true)
  })

  it('leaves out a live user dashboard: not a template', async () => {
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    // The sidebar legitimately links to Launch week; scope to the page content.
    const main = within(screen.getByRole('main'))
    expect(main.queryByText('Launch week')).not.toBeInTheDocument()
  })

  it('shows a group as one card with its tabs, each opening itself, as on the Archive page', async () => {
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    // The sidebar links to Views too; scope to the page content.
    const main = within(screen.getByRole('main'))
    const card = main.getByRole('listitem', { name: 'Views' })
    expect(within(card).getByText('2 tabs')).toBeInTheDocument()
    expect(within(card).getByRole('link', { name: 'Views' })).toHaveAttribute('href', '/dashboards/1')
    expect(within(card).getByRole('link', { name: 'Product' })).toHaveAttribute('href', '/dashboards/2')
    expect(within(card).getAllByText('1 widget')).toHaveLength(2)
    // A lone-tab group is a single row: no tab count, no tab menu.
    const reach = main.getByRole('link', { name: 'Reach' }).closest('li')!
    expect(within(reach).queryByText(/tabs/)).not.toBeInTheDocument()
    expect(within(reach).queryByRole('button', { name: 'Reach tab actions' })).not.toBeInTheDocument()
  })

  it('titles a group card by its group_title, its tabs keeping their own titles', async () => {
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue({
      timezone: 'UTC',
      dashboards: dashboards.map((d) => (d.group_id === 1 ? { ...d, group_title: 'Reports' } : d)),
    })
    renderGallery()

    const card = (await screen.findByRole('heading', { name: 'Reports' })).closest('li')!
    expect(within(card).getByRole('link', { name: 'Views' })).toHaveAttribute('href', '/dashboards/1')
    expect(within(card).getByRole('link', { name: 'Product' })).toHaveAttribute('href', '/dashboards/2')
  })

  it('copies one tab to a new dashboard from that tab\'s "…" menu', async () => {
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    await userEvent.click(within(screen.getByRole('main')).getByRole('button', { name: 'Product tab actions' }))
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Copy to new dashboard'])
    await userEvent.click(screen.getByRole('menuitem', { name: 'Copy to new dashboard' }))

    expect(duplicate).toHaveBeenCalledWith(dashboards[1])
  })

  it('duplicates a lone group whole from its row\'s "…" menu', async () => {
    mockApi()
    renderGallery()

    await screen.findByRole('heading', { name: 'Reach' })
    await userEvent.click(within(screen.getByRole('main')).getByRole('button', { name: 'Reach actions' }))
    await userEvent.click(screen.getByRole('menuitem', { name: 'Duplicate dashboard' }))

    expect(duplicate).toHaveBeenCalledWith(dashboards[2], { wholeGroup: true })
  })

  it('duplicates the whole group from the card\'s "…" menu', async () => {
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
      renameGroup: vi.fn(),
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
    expect(within(screen.getByRole('main')).queryByRole('button', { name: /actions/ })).not.toBeInTheDocument()
  })
})
