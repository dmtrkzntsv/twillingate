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

  it('lists each tab with a link to its dashboard and a "Copy as a dashboard" button', async () => {
    mockApi()
    renderGallery()

    const product = await screen.findByRole('link', { name: 'Product' })
    expect(product).toHaveAttribute('href', '/dashboards/2')
  })

  it('"Copy as a dashboard" calls duplicate(tab)', async () => {
    mockApi()
    renderGallery()
    await screen.findByRole('link', { name: 'Product' })

    const row = screen.getByRole('link', { name: 'Product' }).closest('li')!
    await userEvent.click(within(row).getByRole('button', { name: 'Copy as a dashboard' }))

    expect(duplicate).toHaveBeenCalledWith(dashboards[1])
  })

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
  })

  it('shows no "Copy as a dashboard" in reporting dev, which takes no writes', async () => {
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC', dashboards, dev: true })
    renderGallery()

    await screen.findByRole('heading', { name: 'Views' })
    expect(screen.queryByRole('button', { name: 'Copy as a dashboard' })).not.toBeInTheDocument()
  })
})
