import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { useDashboardActions, type DashboardActions } from '@/hooks/use-dashboard-actions'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { ProjectTab } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import ProjectTabMenu from './ProjectTabMenu'

vi.mock('@/hooks/use-dashboard-actions', () => ({ useDashboardActions: vi.fn() }))

const rename = vi.fn()
const tabActions: ProjectTabActions = { add: vi.fn(), create: vi.fn(), remove: vi.fn(), move: vi.fn(), pending: false }
const mine: ProjectTab = { dashboard_id: 30, title: 'Launch', owner: 'user', group_id: 30 }
const views: ProjectTab = { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1 }

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(useDashboardActions).mockReturnValue({
    duplicate: vi.fn(),
    archive: vi.fn(),
    restore: vi.fn(),
    setSidebar: vi.fn(),
    move: vi.fn(),
    rename,
    renameGroup: vi.fn(),
    pending: false,
  } satisfies DashboardActions)
})

function renderMenu(tab: ProjectTab, renamable: boolean) {
  renderWithProviders(
    <MemoryRouter>
      <ProjectTabMenu projectId={9} tab={tab} tabs={[views, mine]} actions={tabActions} search="" renamable={renamable} />
    </MemoryRouter>
  )
}

describe('ProjectTabMenu, Rename tab', () => {
  it('retitles your own dashboard from its project tab', async () => {
    rename.mockResolvedValue(true)
    renderMenu(mine, true)
    await userEvent.click(screen.getByRole('button', { name: 'Tab actions' }))
    await userEvent.click(screen.getByRole('menuitem', { name: 'Rename tab…' }))
    const field = screen.getByRole('textbox', { name: 'Tab name' })
    expect(field).toHaveValue('Launch')

    await userEvent.clear(field)
    await userEvent.type(field, 'Launch week{Enter}')
    expect(rename).toHaveBeenCalledWith(30, 'Launch week')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('offers no rename where it is not renamable (a built-in tab)', async () => {
    renderMenu(views, false)
    await userEvent.click(screen.getByRole('button', { name: 'Tab actions' }))
    expect(screen.queryByRole('menuitem', { name: 'Rename tab…' })).not.toBeInTheDocument()
  })
})
