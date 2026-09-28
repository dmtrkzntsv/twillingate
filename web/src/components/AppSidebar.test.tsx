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

describe('AppSidebar gallery', () => {
  it('links to the components gallery, active on a gallery page', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/gallery/components']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} />
        </SidebarProvider>
      </MemoryRouter>
    )
    const link = screen.getByRole('link', { name: 'Components' })
    expect(link).toHaveAttribute('href', '/gallery/components')
    expect(link).toHaveAttribute('data-active', 'true')
    expect(screen.getByText('Gallery')).toBeInTheDocument()
  })

  it('is not active on a dashboard', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/dashboards/1']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={1} />
        </SidebarProvider>
      </MemoryRouter>
    )
    expect(screen.getByRole('link', { name: 'Components' })).not.toHaveAttribute('data-active', 'true')
  })
})
