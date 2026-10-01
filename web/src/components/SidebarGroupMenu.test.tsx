import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { DashboardActions } from '@/hooks/use-dashboard-actions'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { Group } from '@/lib/arrange'
import { renderWithProviders } from '@/test/render'
import SidebarGroupMenu from './SidebarGroupMenu'

vi.mock('@/hooks/use-dashboard-actions', () => ({
  useDashboardActions: vi.fn(),
}))

const duplicate = vi.fn()
const archive = vi.fn()
const move = vi.fn()

function member(dashboard_id: number, title: string, owner: 'system' | 'user', group_id: number) {
  return { dashboard_id, title, owner, group_id, widgets: 1 }
}

const systemGroup: Group = {
  groupId: 1,
  owner: 'system',
  members: [member(1, 'Views', 'system', 1), member(2, 'Product', 'system', 1)],
}

const userGroupA: Group = {
  groupId: 13,
  owner: 'user',
  members: [member(13, 'Marketing', 'user', 13)],
}

const userGroupB: Group = {
  groupId: 20,
  owner: 'user',
  members: [member(20, 'Launch week', 'user', 20)],
}

const userGroups = [userGroupA, userGroupB]

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

function renderMenu(group: Group, currentId: number) {
  renderWithProviders(<SidebarGroupMenu group={group} userGroups={userGroups} currentId={currentId} />)
}

describe('SidebarGroupMenu, system group', () => {
  it('has exactly Duplicate and Archive', async () => {
    renderMenu(systemGroup, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))

    const items = screen.getAllByRole('menuitem')
    expect(items).toHaveLength(2)
    expect(screen.getByText('Duplicate')).toBeInTheDocument()
    expect(screen.getByText('Your copy replaces it in the sidebar')).toBeInTheDocument()
    expect(screen.getByText('Archive')).toBeInTheDocument()
  })

  it('duplicate calls duplicate(first, {}), letting the server default apply', async () => {
    renderMenu(systemGroup, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Duplicate'))

    expect(duplicate).toHaveBeenCalledWith(systemGroup.members[0], {})
  })

  it('archive while viewing one of its tabs navigates to /', async () => {
    renderMenu(systemGroup, 2)
    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Archive'))

    expect(archive).toHaveBeenCalledWith(systemGroup.members[0], { wholeGroup: true, navigateTo: '/' })
  })

  it('archive while elsewhere does not navigate', async () => {
    renderMenu(systemGroup, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Archive'))

    expect(archive).toHaveBeenCalledWith(systemGroup.members[0], { wholeGroup: true, navigateTo: undefined })
  })
})

describe('SidebarGroupMenu, user group', () => {
  it('has Duplicate, Archive, Move up and Move down, Move up disabled on the first group', async () => {
    renderMenu(userGroupA, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Marketing actions' }))

    const items = screen.getAllByRole('menuitem')
    expect(items.map((i) => i.textContent)).toEqual(['Duplicate', 'Archive', 'Move up', 'Move down'])
    expect(screen.getByText('Move up').closest('[role="menuitem"]')).toHaveAttribute('data-disabled')
    expect(screen.getByText('Move down').closest('[role="menuitem"]')).not.toHaveAttribute('data-disabled')
  })

  it('Move down disabled on the last group', async () => {
    renderMenu(userGroupB, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Launch week actions' }))

    expect(screen.getByText('Move down').closest('[role="menuitem"]')).toHaveAttribute('data-disabled')
    expect(screen.getByText('Move up').closest('[role="menuitem"]')).not.toHaveAttribute('data-disabled')
  })

  it('duplicate calls duplicate(first, { wholeGroup: true })', async () => {
    renderMenu(userGroupA, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Marketing actions' }))
    await userEvent.click(screen.getByText('Duplicate'))

    expect(duplicate).toHaveBeenCalledWith(userGroupA.members[0], { wholeGroup: true })
  })

  it('archive calls archive(first, { wholeGroup: true, navigateTo }) when current is in this group', async () => {
    renderMenu(userGroupA, 13)
    await userEvent.click(screen.getByRole('button', { name: 'Marketing actions' }))
    await userEvent.click(screen.getByText('Archive'))

    expect(archive).toHaveBeenCalledWith(userGroupA.members[0], { wholeGroup: true, navigateTo: '/' })
  })

  it('clicking Move down on the first group calls move with the second group as after', async () => {
    renderMenu(userGroupA, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Marketing actions' }))
    await userEvent.click(screen.getByText('Move down'))

    expect(move).toHaveBeenCalledWith(13, { after: 20 })
  })

  it('clicking Move up on the second group calls move with after: 0', async () => {
    renderMenu(userGroupB, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Launch week actions' }))
    await userEvent.click(screen.getByText('Move up'))

    expect(move).toHaveBeenCalledWith(20, { after: 0 })
  })
})
