import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
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

const actions: ProjectTabActions = { add: vi.fn(), remove: vi.fn(), move: vi.fn(), pending: false }

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
})
