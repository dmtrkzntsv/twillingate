import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { DashboardActions } from '@/hooks/use-dashboard-actions'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardDetail, DashboardInfo, DashboardTab } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import DashboardMenu from './DashboardMenu'

vi.mock('@/hooks/use-dashboard-actions', () => ({
  useDashboardActions: vi.fn(),
}))

const duplicate = vi.fn()
const archive = vi.fn()
const move = vi.fn()

function info(dashboard_id: number, title: string, owner: 'system' | 'user', group_id: number): DashboardInfo {
  return { dashboard_id, title, owner, group_id, widgets: 1 }
}

function tab(dashboard_id: number, title: string): DashboardTab {
  return { dashboard_id, title }
}

function detail(dashboard_id: number, title: string, group_id: number, tabs: DashboardTab[]): DashboardDetail {
  return { dashboard_id, title, owner: 'user', group_id, widgets: [], follows_project: false, follows_range: false, tabs }
}

// Views/Product: a system group, uninvolved in these tests other than
// proving it does not leak into "Move to" (that only lists user groups).
// Marketing/Funnel/Reach: a three-tab user group. Launch week: a lone
// user dashboard — the only "other" group for Marketing's "Move to".
const list: DashboardInfo[] = [
  info(1, 'Views', 'system', 1),
  info(2, 'Product', 'system', 1),
  info(13, 'Marketing', 'user', 13),
  info(14, 'Funnel', 'user', 13),
  info(15, 'Reach', 'user', 13),
  info(20, 'Launch week', 'user', 20),
]

const marketingTabs = [tab(13, 'Marketing'), tab(14, 'Funnel'), tab(15, 'Reach')]
const marketing = detail(13, 'Marketing', 13, marketingTabs)
const reach = detail(15, 'Reach', 13, marketingTabs)
const launchWeek = detail(20, 'Launch week', 20, [tab(20, 'Launch week')])

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(useDashboardActions).mockReturnValue({
    duplicate,
    archive,
    restore: vi.fn(),
    move,
    pending: false,
  } satisfies DashboardActions)
})

function renderMenu(dashboard: DashboardDetail) {
  renderWithProviders(<DashboardMenu dashboard={dashboard} list={list} />)
}

async function openMenu() {
  await userEvent.click(screen.getByRole('button', { name: 'Dashboard actions' }))
}

describe('DashboardMenu, a tab among others (n > 1)', () => {
  it('has Duplicate tab, Archive tab, Move left (disabled first), Move right and Move to', async () => {
    renderMenu(marketing)
    await openMenu()

    expect(screen.getByText('Duplicate tab')).toBeInTheDocument()
    expect(screen.getByText('Archive tab')).toBeInTheDocument()
    expect(screen.getByText('Move left').closest('[role="menuitem"]')).toHaveAttribute('data-disabled')
    expect(screen.getByText('Move right').closest('[role="menuitem"]')).not.toHaveAttribute('data-disabled')
    expect(screen.getByText('Move to')).toBeInTheDocument()
  })

  it('disables Move right on the last tab', async () => {
    renderMenu(reach)
    await openMenu()

    expect(screen.getByText('Move right').closest('[role="menuitem"]')).toHaveAttribute('data-disabled')
    expect(screen.getByText('Move left').closest('[role="menuitem"]')).not.toHaveAttribute('data-disabled')
  })

  it('Duplicate tab calls duplicate(d) with no wholeGroup', async () => {
    renderMenu(marketing)
    await openMenu()
    await userEvent.click(screen.getByText('Duplicate tab'))

    expect(duplicate).toHaveBeenCalledWith(marketing)
  })

  it('Move left calls move with the tab before it as after', async () => {
    renderMenu(reach)
    await openMenu()
    await userEvent.click(screen.getByText('Move left'))

    expect(move).toHaveBeenCalledWith(15, { after: 13 })
  })

  it('Move right calls move with the tab after it as after', async () => {
    renderMenu(marketing)
    await openMenu()
    await userEvent.click(screen.getByText('Move right'))

    expect(move).toHaveBeenCalledWith(13, { after: 14 })
  })

  it('Archive tab on the last tab navigates to the previous one', async () => {
    renderMenu(reach)
    await openMenu()
    await userEvent.click(screen.getByText('Archive tab'))

    expect(archive).toHaveBeenCalledWith(reach, { navigateTo: '/dashboards/14' })
  })

  it('"Move to" → "Own dashboard" calls move(id, {group_id: 0})', async () => {
    renderMenu(marketing)
    await openMenu()
    await userEvent.click(screen.getByText('Move to'))
    await userEvent.click(await screen.findByText('Own dashboard'))

    expect(move).toHaveBeenCalledWith(13, { group_id: 0 })
  })

  it('"Move to" lists the other live user group by its first member\'s title', async () => {
    renderMenu(marketing)
    await openMenu()
    await userEvent.click(screen.getByText('Move to'))

    expect(await screen.findByText('Launch week')).toBeInTheDocument()
    expect(screen.queryByText('Views')).not.toBeInTheDocument()
  })
})

describe('DashboardMenu, a lone dashboard (n === 1)', () => {
  it('has Duplicate, Archive, no Move left/right, and "Move to" without "Own dashboard"', async () => {
    renderMenu(launchWeek)
    await openMenu()

    expect(screen.getByText('Duplicate')).toBeInTheDocument()
    expect(screen.getByText('Archive')).toBeInTheDocument()
    expect(screen.queryByText('Move left')).not.toBeInTheDocument()
    expect(screen.queryByText('Move right')).not.toBeInTheDocument()
    await userEvent.click(screen.getByText('Move to'))
    expect(await screen.findByText('Marketing')).toBeInTheDocument()
    expect(screen.queryByText('Own dashboard')).not.toBeInTheDocument()
  })

  it('Duplicate calls duplicate(d, { wholeGroup: true })', async () => {
    renderMenu(launchWeek)
    await openMenu()
    await userEvent.click(screen.getByText('Duplicate'))

    expect(duplicate).toHaveBeenCalledWith(launchWeek, { wholeGroup: true })
  })

  it('Archive calls archive(d, { navigateTo: \'/\' })', async () => {
    renderMenu(launchWeek)
    await openMenu()
    await userEvent.click(screen.getByText('Archive'))

    expect(archive).toHaveBeenCalledWith(launchWeek, { navigateTo: '/' })
  })

  it('"Move to" → "Marketing" calls move(id, {group_id: <Marketing\'s group>})', async () => {
    renderMenu(launchWeek)
    await openMenu()
    await userEvent.click(screen.getByText('Move to'))
    await userEvent.click(await screen.findByText('Marketing'))

    expect(move).toHaveBeenCalledWith(20, { group_id: 13 })
  })

  it('hides "Move to" entirely when there are no other user groups', async () => {
    const lone = detail(30, 'Solo', 30, [tab(30, 'Solo')])
    renderWithProviders(<DashboardMenu dashboard={lone} list={[info(1, 'Views', 'system', 1), info(30, 'Solo', 'user', 30)]} />)
    await openMenu()

    expect(screen.queryByText('Move to')).not.toBeInTheDocument()
  })
})
