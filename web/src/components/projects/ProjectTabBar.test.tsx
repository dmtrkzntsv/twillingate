import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { ProjectTab } from '@/lib/api'
import { writeLastTab } from '@/lib/last-tab'
import { FORMS_ID, SETTINGS_ID, type SettingsSection } from '@/lib/project-tabs'
import ProjectTabBar from './ProjectTabBar'

const tabs: ProjectTab[] = [
  { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1 },
  { dashboard_id: 2, title: 'Product', owner: 'system', group_id: 1 },
  { dashboard_id: 9, title: 'Mine', owner: 'user', group_id: 9 },
]

const actions: ProjectTabActions = { add: vi.fn(), create: vi.fn(), remove: vi.fn(), move: vi.fn(), pending: false }

function renderBar({
  currentId,
  newSubmissions,
  draftForms = 0,
  readOnly,
  section,
  archived,
  tabs: shown = tabs,
  tabsLoaded,
}: {
  currentId: number
  newSubmissions: number
  draftForms?: number
  readOnly?: boolean
  section?: SettingsSection
  archived?: boolean
  tabs?: ProjectTab[]
  tabsLoaded?: boolean
}) {
  return render(
    <MemoryRouter initialEntries={['/projects/7/settings?range=30d']}>
      <TooltipProvider>
        <ProjectTabBar
          projectId={7}
          currentId={currentId}
          tabs={shown}
          tabsLoaded={tabsLoaded}
          dashboards={[]}
          actions={actions}
          newSubmissions={newSubmissions}
          draftForms={draftForms}
          readOnly={readOnly}
          section={section}
          archived={archived}
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

  it('dots the Forms button for a draft with nothing new', () => {
    renderBar({ currentId: 1, newSubmissions: 0, draftForms: 2 })
    const link = screen.getByRole('link', { name: 'Forms, 2 drafts' })
    expect(link).toHaveTextContent('')
    expect(link.querySelector('[data-forms-dot]')).toBeInTheDocument()
  })

  it('counts new submissions over the dot, naming both', () => {
    renderBar({ currentId: 1, newSubmissions: 3, draftForms: 1 })
    const link = screen.getByRole('link', { name: 'Forms, 3 new, 1 draft' })
    expect(link).toHaveTextContent('3')
    expect(link.querySelector('[data-forms-dot]')).not.toBeInTheDocument()
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

  describe('on Settings', () => {
    beforeEach(() => localStorage.clear())

    it('swaps the dashboards for the Settings tabs, after "← Dashboards", with Forms and the gear kept', () => {
      renderBar({ currentId: SETTINGS_ID, newSubmissions: 0, section: 'events' })
      expect(screen.queryByRole('tab')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Add tab' })).toBeNull()
      expect(screen.queryByRole('combobox', { name: 'Tab' })).toBeNull()
      const nav = screen.getByRole('navigation', { name: 'Settings sections' })
      const links = within(nav).getAllByRole('link')
      expect(links.map((l) => l.textContent)).toEqual(['Usage', 'Install', 'Domains', 'Events', 'Attributes', 'Danger'])
      expect(links.map((l) => l.getAttribute('aria-current'))).toEqual([null, null, null, 'page', null, null])
      expect(links[1]).toHaveAttribute('href', '/projects/7/settings/install?range=30d')
      const back = screen.getByRole('link', { name: 'Dashboards' })
      expect(back.compareDocumentPosition(nav) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(screen.getByRole('link', { name: 'Forms' })).toHaveAttribute('href', '/projects/7/forms?range=30d')
      expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page')
    })

    it('marks Usage current when no section is named', () => {
      renderBar({ currentId: SETTINGS_ID, newSubmissions: 0 })
      expect(screen.getByRole('link', { name: 'Usage' })).toHaveAttribute('aria-current', 'page')
    })

    it('leaves out Install and Danger on an archived project', () => {
      renderBar({ currentId: SETTINGS_ID, newSubmissions: 0, archived: true })
      const nav = screen.getByRole('navigation', { name: 'Settings sections' })
      expect(within(nav).getAllByRole('link').map((l) => l.textContent)).toEqual(['Usage', 'Domains', 'Events', 'Attributes'])
    })

    it('goes back to the remembered dashboard from "← Dashboards" and the gear, else the first', () => {
      writeLastTab(7, 9)
      const { unmount } = renderBar({ currentId: SETTINGS_ID, newSubmissions: 0 })
      expect(screen.getByRole('link', { name: 'Dashboards' })).toHaveAttribute('href', '/projects/7/dashboards/9?range=30d')
      expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/projects/7/dashboards/9?range=30d')
      unmount()
      writeLastTab(7, 404)
      renderBar({ currentId: SETTINGS_ID, newSubmissions: 0 })
      expect(screen.getByRole('link', { name: 'Dashboards' })).toHaveAttribute('href', '/projects/7/dashboards/1?range=30d')
    })

    it('offers Add dashboard, the "+" picker, when the project has no tabs, and the gear stays on Settings', async () => {
      const user = userEvent.setup()
      renderBar({ currentId: SETTINGS_ID, newSubmissions: 0, tabs: [] })
      expect(screen.queryByRole('link', { name: 'Dashboards' })).toBeNull()
      expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/projects/7/settings?range=30d')
      await user.click(screen.getByRole('button', { name: 'Add dashboard' }))
      expect(await screen.findByRole('button', { name: 'New dashboard…' })).toBeInTheDocument()
    })

    it('offers neither while the tabs load', () => {
      renderBar({ currentId: SETTINGS_ID, newSubmissions: 0, tabs: [], tabsLoaded: false })
      expect(screen.queryByRole('link', { name: 'Dashboards' })).toBeNull()
      expect(screen.queryByRole('button', { name: 'Add dashboard' })).toBeNull()
    })
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

    it('makes one dashboard however often it is submitted while the first is in flight', async () => {
      let finish: (ok: boolean) => void = () => {}
      vi.mocked(actions.create).mockClear()
      vi.mocked(actions.create).mockReturnValue(new Promise((r) => (finish = r)))
      const { user, field } = await openForm()
      await user.type(field, 'Launch{Enter}{Enter}')
      await user.click(screen.getByRole('button', { name: 'Create' }))
      expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled()
      finish(false)
      await waitFor(() => expect(screen.getByRole('button', { name: 'Create' })).toBeEnabled())
      expect(actions.create).toHaveBeenCalledTimes(1)
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
