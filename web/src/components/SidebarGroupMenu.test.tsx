import { useEffect } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SidebarProvider, useSidebar } from '@/components/ui/sidebar'
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
  renderWithProviders(
    <SidebarProvider>
      <SidebarGroupMenu group={group} userGroups={userGroups} currentId={currentId} />
    </SidebarProvider>
  )
}

describe('SidebarGroupMenu, system group', () => {
  it('has exactly Duplicate and Delete: a system group is deleted, not archived', async () => {
    renderMenu(systemGroup, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))

    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Duplicate', 'Delete'])
  })

  it('duplicate calls duplicate(first, { wholeGroup: true }), copying every tab', async () => {
    renderMenu(systemGroup, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Duplicate'))

    expect(duplicate).toHaveBeenCalledWith(systemGroup.members[0], { wholeGroup: true })
  })

  it('delete while viewing one of its tabs archives the whole group and navigates to /', async () => {
    renderMenu(systemGroup, 2)
    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Delete'))

    expect(archive).toHaveBeenCalledWith(systemGroup.members[0], { wholeGroup: true, navigateTo: '/dashboards', deleted: true })
  })

  it('delete while elsewhere does not navigate', async () => {
    renderMenu(systemGroup, 99)
    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Delete'))

    expect(archive).toHaveBeenCalledWith(systemGroup.members[0], { wholeGroup: true, navigateTo: undefined, deleted: true })
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

    expect(archive).toHaveBeenCalledWith(userGroupA.members[0], { wholeGroup: true, navigateTo: '/dashboards', deleted: false })
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

/** Reads `openMobile` so a test can observe the phone drawer close. */
function DrawerProbe() {
  const { openMobile } = useSidebar()
  return <div data-testid="drawer">{openMobile ? 'open' : 'closed'}</div>
}

/** Opens the drawer on mount, as the sidebar's own trigger would. */
function OpenDrawer() {
  const { setOpenMobile } = useSidebar()
  useEffect(() => setOpenMobile(true), [setOpenMobile])
  return null
}

function renderOnPhone(group: Group, currentId: number) {
  renderWithProviders(
    <SidebarProvider>
      <OpenDrawer />
      <DrawerProbe />
      <SidebarGroupMenu group={group} userGroups={userGroups} currentId={currentId} />
    </SidebarProvider>
  )
}

describe('SidebarGroupMenu, phone drawer (D37)', () => {
  beforeEach(() => {
    window.innerWidth = 390
  })

  afterEach(() => {
    window.innerWidth = 1024
  })

  it('closes the drawer when Duplicate opens the copy', async () => {
    renderOnPhone(systemGroup, 99)
    await waitFor(() => expect(screen.getByTestId('drawer')).toHaveTextContent('open'))

    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Duplicate'))

    expect(screen.getByTestId('drawer')).toHaveTextContent('closed')
  })

  it('closes the drawer when Delete navigates away', async () => {
    renderOnPhone(systemGroup, 2)
    await waitFor(() => expect(screen.getByTestId('drawer')).toHaveTextContent('open'))

    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Delete'))

    expect(screen.getByTestId('drawer')).toHaveTextContent('closed')
  })

  it('leaves the drawer open when Delete does not navigate', async () => {
    renderOnPhone(systemGroup, 99)
    await waitFor(() => expect(screen.getByTestId('drawer')).toHaveTextContent('open'))

    await userEvent.click(screen.getByRole('button', { name: 'Views actions' }))
    await userEvent.click(screen.getByText('Delete'))

    expect(screen.getByTestId('drawer')).toHaveTextContent('open')
  })
})
