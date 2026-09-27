import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { _resetForTests, getAuthHeader } from '@/lib/auth'
import { renderWithProviders } from '@/test/render'
import AppSidebar from './AppSidebar'

function renderSidebar() {
  renderWithProviders(
    <MemoryRouter>
      <SidebarProvider>
        <AppSidebar dashboards={[]} currentId={0} />
      </SidebarProvider>
    </MemoryRouter>
  )
}

beforeEach(() => {
  localStorage.clear()
  _resetForTests()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('AppSidebar log out', () => {
  it('is hidden in open mode, with no credential to forget', () => {
    renderSidebar()
    expect(screen.queryByRole('button', { name: 'Log out' })).not.toBeInTheDocument()
  })

  it('forgets the credentials and goes to the login page', async () => {
    localStorage.setItem('twillingate.token', 'pasted')
    localStorage.setItem('twillingate.last_dashboard', '3')
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
    renderSidebar()

    await userEvent.click(screen.getByRole('button', { name: 'Log out' }))

    expect(getAuthHeader()).toBeUndefined()
    expect(localStorage.getItem('twillingate.last_dashboard')).toBeNull()
    expect(assign).toHaveBeenCalledWith('/app/login')
  })
})
