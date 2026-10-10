import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { ProjectTab } from '@/lib/api'
import { FORMS_ID, SETTINGS_ID } from '@/lib/project-tabs'
import ProjectTabBar from './ProjectTabBar'

const tabs: ProjectTab[] = [
  { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1 },
  { dashboard_id: 2, title: 'Product', owner: 'system', group_id: 1 },
  { dashboard_id: 9, title: 'Mine', owner: 'user', group_id: 9 },
]

const actions: ProjectTabActions = { add: vi.fn(), create: vi.fn(), remove: vi.fn(), move: vi.fn(), pending: false }

function renderBar({ currentId, newSubmissions, readOnly }: { currentId: number; newSubmissions: number; readOnly?: boolean }) {
  return render(
    <MemoryRouter initialEntries={['/projects/7/settings?range=30d']}>
      <TooltipProvider>
        <ProjectTabBar
          projectId={7}
          currentId={currentId}
          tabs={tabs}
          dashboards={[]}
          actions={actions}
          newSubmissions={newSubmissions}
          readOnly={readOnly}
        />
      </TooltipProvider>
    </MemoryRouter>
  )
}

describe('ProjectTabBar', () => {
  it('shows every dashboard as a tab and Settings and Forms as buttons', () => {
    renderBar({ currentId: 1, newSubmissions: 0 })
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Views', 'Product', 'Mine'])
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/projects/7/settings?range=30d')
    expect(screen.getByRole('link', { name: 'Forms' })).toHaveAttribute('href', '/projects/7/forms?range=30d')
  })

  it('drags the built-ins like the rest', () => {
    renderBar({ currentId: 1, newSubmissions: 0 })
    expect(screen.getAllByRole('tab').map((t) => t.getAttribute('aria-roledescription'))).toEqual(['sortable', 'sortable', 'sortable'])
  })

  it('counts new submissions on the Forms button', () => {
    renderBar({ currentId: 1, newSubmissions: 3 })
    expect(screen.getByRole('link', { name: 'Forms, 3 new' })).toHaveTextContent('3')
  })

  it('caps the badge at 99+', () => {
    renderBar({ currentId: 1, newSubmissions: 140 })
    expect(screen.getByRole('link', { name: 'Forms, 140 new' })).toHaveTextContent('99+')
  })

  it('marks the gear current on Settings and leaves no tab selected', () => {
    renderBar({ currentId: SETTINGS_ID, newSubmissions: 0 })
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByRole('tab', { selected: true })).toBeNull()
  })

  it('marks the inbox current on Forms', () => {
    renderBar({ currentId: FORMS_ID, newSubmissions: 0 })
    expect(screen.getByRole('link', { name: 'Forms' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Settings' })).not.toHaveAttribute('aria-current')
  })

  it('shows the phone select placeholder when no tab is current', () => {
    renderBar({ currentId: FORMS_ID, newSubmissions: 0 })
    expect(screen.getByRole('combobox', { name: 'Tab' })).toHaveTextContent('Tabs')
  })

  it('shows the current tab in the phone select', () => {
    renderBar({ currentId: 2, newSubmissions: 0 })
    expect(screen.getByRole('combobox', { name: 'Tab' })).toHaveTextContent('Product')
  })

  it('in reporting dev keeps the gear alone: no Forms, no Add tab, no dragging', () => {
    renderBar({ currentId: SETTINGS_ID, newSubmissions: 0, readOnly: true })
    expect(screen.queryByRole('link', { name: 'Forms' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Add tab' })).toBeNull()
    expect(screen.getByRole('link', { name: 'Settings' })).toBeInTheDocument()
  })

  describe('New dashboard…', () => {
    async function openForm() {
      const user = userEvent.setup()
      renderBar({ currentId: 1, newSubmissions: 0 })
      await user.click(screen.getByRole('button', { name: 'Add tab' }))
      await user.click(await screen.findByRole('button', { name: 'New dashboard…' }))
      return { user, field: await screen.findByRole('textbox', { name: 'Dashboard title' }) }
    }

    it('asks for two characters before Create is enabled', async () => {
      const { user, field } = await openForm()
      await user.type(field, 'A')
      expect(screen.getByText('At least 2 characters')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled()
      await user.type(field, 'B')
      expect(screen.getByRole('button', { name: 'Create' })).toBeEnabled()
    })

    it('creates on Enter for the project and closes when it resolves true', async () => {
      vi.mocked(actions.create).mockResolvedValue(true)
      const { user, field } = await openForm()
      await user.type(field, ' Launch {Enter}')
      expect(actions.create).toHaveBeenCalledWith(7, 'Launch')
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    })

    it('stays open with the title when the create is refused', async () => {
      vi.mocked(actions.create).mockResolvedValue(false)
      const { user, field } = await openForm()
      await user.type(field, 'Launch{Enter}')
      await waitFor(() => expect(actions.create).toHaveBeenCalled())
      expect(screen.getByRole('textbox', { name: 'Dashboard title' })).toHaveValue('Launch')
    })

    it('Back returns to the picker, and a closed dialog opens at the picker again', async () => {
      const { user } = await openForm()
      await user.click(screen.getByRole('button', { name: 'Back' }))
      expect(screen.getByRole('button', { name: 'New dashboard…' })).toBeInTheDocument()
      await user.click(screen.getByRole('button', { name: 'New dashboard…' }))
      await user.keyboard('{Escape}')
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      await user.click(screen.getByRole('button', { name: 'Add tab' }))
      expect(await screen.findByRole('button', { name: 'New dashboard…' })).toBeInTheDocument()
    })
  })
})
