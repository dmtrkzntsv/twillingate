import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { DashboardActions } from '@/hooks/use-dashboard-actions'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardDetail, DashboardInfo, DashboardTab } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import { GroupMenu, TabMenu } from './DashboardMenu'

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

function detail(
  dashboard_id: number,
  title: string,
  group_id: number,
  tabs: DashboardTab[],
  extra: Partial<DashboardDetail> = {}
): DashboardDetail {
  return { dashboard_id, title, owner: 'user', group_id, widgets: [], follows_project: false, follows_range: false, tabs, ...extra }
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
const systemTabs = [tab(1, 'Views'), tab(2, 'Product')]
const product = detail(2, 'Product', 1, systemTabs, { owner: 'system' })

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

function renderTabMenu(dashboard: DashboardDetail) {
  renderWithProviders(<TabMenu dashboard={dashboard} list={list} />)
}

function renderGroupMenu(dashboard: DashboardDetail) {
  renderWithProviders(<GroupMenu dashboard={dashboard} />)
}

async function openTabMenu() {
  await userEvent.click(screen.getByRole('button', { name: 'Tab actions' }))
}

async function openGroupMenu() {
  await userEvent.click(screen.getByRole('button', { name: 'Dashboard actions' }))
}

const items = () => screen.getAllByRole('menuitem').map((i) => i.textContent)

describe('TabMenu, a user tab among others', () => {
  it('has Duplicate tab, Copy to new dashboard, Archive tab, Move left (disabled first), Move right and Move to', async () => {
    renderTabMenu(marketing)
    await openTabMenu()

    expect(items()).toEqual(['Duplicate tab', 'Copy to new dashboard', 'Archive tab', 'Move left', 'Move right', 'Move to'])
    expect(screen.getByText('Move left').closest('[role="menuitem"]')).toHaveAttribute('data-disabled')
    expect(screen.getByText('Move right').closest('[role="menuitem"]')).not.toHaveAttribute('data-disabled')
  })

  it('disables Move right on the last tab', async () => {
    renderTabMenu(reach)
    await openTabMenu()

    expect(screen.getByText('Move right').closest('[role="menuitem"]')).toHaveAttribute('data-disabled')
    expect(screen.getByText('Move left').closest('[role="menuitem"]')).not.toHaveAttribute('data-disabled')
  })

  it('Duplicate tab copies it into this dashboard: duplicate(d, { groupId: its group })', async () => {
    renderTabMenu(marketing)
    await openTabMenu()
    await userEvent.click(screen.getByText('Duplicate tab'))

    expect(duplicate).toHaveBeenCalledWith(marketing, { groupId: 13 })
  })

  it('Copy to new dashboard copies it out: duplicate(d), no group', async () => {
    renderTabMenu(marketing)
    await openTabMenu()
    await userEvent.click(screen.getByText('Copy to new dashboard'))

    expect(duplicate).toHaveBeenCalledWith(marketing)
  })

  it('Move left calls move with the tab before it as after', async () => {
    renderTabMenu(reach)
    await openTabMenu()
    await userEvent.click(screen.getByText('Move left'))

    expect(move).toHaveBeenCalledWith(15, { after: 13 })
  })

  it('Move right calls move with the tab after it as after', async () => {
    renderTabMenu(marketing)
    await openTabMenu()
    await userEvent.click(screen.getByText('Move right'))

    expect(move).toHaveBeenCalledWith(13, { after: 14 })
  })

  it('Archive tab on the last tab navigates to the previous one', async () => {
    renderTabMenu(reach)
    await openTabMenu()
    await userEvent.click(screen.getByText('Archive tab'))

    expect(archive).toHaveBeenCalledWith(reach, { navigateTo: '/dashboards/14' })
  })

  it('"Move to" → "Own dashboard" calls move(id, {group_id: 0})', async () => {
    renderTabMenu(marketing)
    await openTabMenu()
    await userEvent.click(screen.getByText('Move to'))
    await userEvent.click(await screen.findByText('Own dashboard'))

    expect(move).toHaveBeenCalledWith(13, { group_id: 0 })
  })

  it('"Move to" lists the other live user group by its first member\'s title', async () => {
    renderTabMenu(marketing)
    await openTabMenu()
    await userEvent.click(screen.getByText('Move to'))

    expect(await screen.findByText('Launch week')).toBeInTheDocument()
    expect(screen.queryByText('Views')).not.toBeInTheDocument()
  })
})

describe('TabMenu, the one tab of a lone user dashboard', () => {
  it('has the same items, Move left and right both disabled', async () => {
    renderTabMenu(launchWeek)
    await openTabMenu()

    expect(items()).toEqual(['Duplicate tab', 'Copy to new dashboard', 'Archive tab', 'Move left', 'Move right', 'Move to'])
    expect(screen.getByText('Move left').closest('[role="menuitem"]')).toHaveAttribute('data-disabled')
    expect(screen.getByText('Move right').closest('[role="menuitem"]')).toHaveAttribute('data-disabled')
  })

  it('"Move to" lists other groups but no "Own dashboard": it already is one', async () => {
    renderTabMenu(launchWeek)
    await openTabMenu()
    await userEvent.click(screen.getByText('Move to'))
    await userEvent.click(await screen.findByText('Marketing'))

    expect(move).toHaveBeenCalledWith(20, { group_id: 13 })
    expect(screen.queryByText('Own dashboard')).not.toBeInTheDocument()
  })

  it('Archive tab lands on "/", there being no other tab', async () => {
    renderTabMenu(launchWeek)
    await openTabMenu()
    await userEvent.click(screen.getByText('Archive tab'))

    expect(archive).toHaveBeenCalledWith(launchWeek, { navigateTo: '/' })
  })

  it('hides "Move to" entirely when there are no other user groups', async () => {
    const lone = detail(30, 'Solo', 30, [tab(30, 'Solo')])
    renderWithProviders(<TabMenu dashboard={lone} list={[info(1, 'Views', 'system', 1), info(30, 'Solo', 'user', 30)]} />)
    await openTabMenu()

    expect(screen.queryByText('Move to')).not.toBeInTheDocument()
  })
})

describe('TabMenu, a system tab', () => {
  it('offers only Copy to new dashboard: a system group takes no new tabs, and a system tab is never archived or moved alone', async () => {
    renderTabMenu(product)
    await openTabMenu()
    expect(items()).toEqual(['Copy to new dashboard'])

    await userEvent.click(screen.getByText('Copy to new dashboard'))
    expect(duplicate).toHaveBeenCalledWith(product)
  })
})

describe('GroupMenu', () => {
  it('duplicates and archives the whole group, named by its first tab', async () => {
    renderGroupMenu(reach)
    await openGroupMenu()
    expect(items()).toEqual(['Duplicate dashboard', 'Archive dashboard'])

    await userEvent.click(screen.getByText('Duplicate dashboard'))
    expect(duplicate).toHaveBeenCalledWith(marketingTabs[0], { wholeGroup: true })

    await openGroupMenu()
    await userEvent.click(screen.getByText('Archive dashboard'))
    expect(archive).toHaveBeenCalledWith(marketingTabs[0], { wholeGroup: true, navigateTo: '/' })
  })

  it('offers the same on a system group', async () => {
    renderGroupMenu(product)
    await openGroupMenu()

    expect(items()).toEqual(['Duplicate dashboard', 'Archive dashboard'])
  })

  it('offers only Duplicate on an archived group, whose banner offers Restore', async () => {
    renderGroupMenu({ ...product, archived_at: '2026-09-30T00:00:00Z' })
    await openGroupMenu()

    expect(items()).toEqual(['Duplicate dashboard'])
  })
})
