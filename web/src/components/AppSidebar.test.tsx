import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import type { DashboardInfo } from '@/lib/api'
import { endpoints, type Project } from '@/lib/api'
import { _resetForTests, getAuthHeader } from '@/lib/auth'
import { dashboardsList } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import AppSidebar from './AppSidebar'

function renderSidebar(dashboards: DashboardInfo[] = [], currentId = 0) {
  renderWithProviders(
    <MemoryRouter>
      <SidebarProvider>
        <AppSidebar dashboards={dashboards} currentId={currentId} />
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

  it('links to the templates gallery, labelled "Templates", active there and not on Components (D17)', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/gallery/dashboards']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} />
        </SidebarProvider>
      </MemoryRouter>
    )
    const link = screen.getByRole('link', { name: 'Templates' })
    expect(link).toHaveAttribute('href', '/gallery/dashboards')
    expect(link).toHaveAttribute('data-active', 'true')
    expect(screen.getByRole('link', { name: 'Components' })).not.toHaveAttribute('data-active', 'true')
  })
})

describe('AppSidebar archive', () => {
  it('links to the archive, between Yours and Gallery, shown with nothing archived', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} />
        </SidebarProvider>
      </MemoryRouter>
    )
    const link = screen.getByRole('link', { name: 'Archive' })
    expect(link).toHaveAttribute('href', '/archive')
    expect(link).not.toHaveAttribute('data-active', 'true')
  })

  it('is active on /archive', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/archive']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} />
        </SidebarProvider>
      </MemoryRouter>
    )
    expect(screen.getByRole('link', { name: 'Archive' })).toHaveAttribute('data-active', 'true')
  })
})

describe('AppSidebar dashboard groups', () => {
  const dashboards = dashboardsList().dashboards

  it('shows the system group once, named by its first member, with no "Reports" entry', () => {
    renderSidebar(dashboards, 1)
    expect(screen.getByRole('link', { name: 'Views' })).toHaveAttribute('href', '/dashboards/1')
    expect(screen.queryByRole('link', { name: 'Reports' })).not.toBeInTheDocument()
    // Only one sidebar entry for the whole group: none of the other four titles is its own link.
    for (const title of ['Product', 'Users', 'Groups', 'Retention']) {
      expect(screen.queryByRole('link', { name: title })).not.toBeInTheDocument()
    }
  })

  it('gives a two-dashboard user group one link, named by the first tab', () => {
    renderSidebar(dashboards, 0)
    const link = screen.getByRole('link', { name: 'Marketing' })
    expect(link).toHaveAttribute('href', '/dashboards/13')
    expect(screen.queryByRole('link', { name: 'Funnel' })).not.toBeInTheDocument()
  })

  it('is active on the group entry when the current page is the second tab', () => {
    renderSidebar(dashboards, 14)
    expect(screen.getByRole('link', { name: 'Marketing' })).toHaveAttribute('data-active', 'true')
  })
})

describe('AppSidebar reordering', () => {
  const dashboards = dashboardsList().dashboards

  function Probe() {
    return <output data-testid="location">{useLocation().pathname}</output>
  }

  it('makes the user entries sortable and leaves the system ones and the menus alone', () => {
    renderSidebar(dashboards, 1)
    expect(screen.getByRole('link', { name: 'Launch week' })).toHaveAttribute('aria-roledescription', 'sortable')
    expect(screen.getByRole('link', { name: 'Marketing' })).toHaveAttribute('aria-roledescription', 'sortable')
    expect(screen.getByRole('link', { name: 'Views' })).not.toHaveAttribute('aria-roledescription')
    expect(screen.getByRole('button', { name: 'Marketing actions' })).not.toHaveAttribute('aria-roledescription')
  })

  it('still navigates on a plain click of a sortable entry', async () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/dashboards/1']}>
        <SidebarProvider>
          <Routes>
            <Route
              path="*"
              element={
                <>
                  <AppSidebar dashboards={dashboards} currentId={1} />
                  <Probe />
                </>
              }
            />
          </Routes>
        </SidebarProvider>
      </MemoryRouter>
    )

    await userEvent.click(screen.getByRole('link', { name: 'Marketing' }))

    expect(screen.getByTestId('location')).toHaveTextContent('/dashboards/13')
  })
})

describe('Projects group', () => {
  beforeEach(() => {
    localStorage.clear()
    const projects: Project[] = Array.from({ length: 40 }, (_, i) => ({ project_id: i + 1, name: `site-${i + 1}`, allowed_origins: [] }))
    projects.push({ project_id: 99, name: 'gone', archived: true, allowed_origins: [] })
    vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects })
  })

  it('is a link to the list, with no projects listed under it', async () => {
    renderSidebar()
    expect(await screen.findByRole('link', { name: 'Projects' })).toHaveAttribute('href', '/projects')
    expect(screen.queryByRole('link', { name: 'site-1' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /projects/i })).not.toBeInTheDocument()
    expect(endpoints.projects).not.toHaveBeenCalled()
  })

  it('is active on the list and on a project', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/projects/4']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} />
        </SidebarProvider>
      </MemoryRouter>
    )
    expect(screen.getByRole('link', { name: 'Projects' })).toHaveAttribute('data-active', 'true')
  })

  it('comes first, above the dashboards', async () => {
    renderSidebar([{ dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1, widgets: 1 }])
    const projects = await screen.findByRole('link', { name: 'Projects' })
    const views = screen.getByRole('link', { name: 'Views' })
    expect(projects.compareDocumentPosition(views) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('is absent in dev mode, which serves none of the console routes', async () => {
    renderWithProviders(
      <MemoryRouter>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} readOnly />
        </SidebarProvider>
      </MemoryRouter>
    )
    expect(await screen.findByRole('link', { name: 'Archive' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Projects' })).not.toBeInTheDocument()
  })
})

describe('Dashboards group', () => {
  const dashboards = [
    { dashboard_id: 1, title: 'Views', owner: 'system' as const, group_id: 1, widgets: 1 },
    { dashboard_id: 2, title: 'Web Vitals', owner: 'system' as const, group_id: 2, widgets: 1 },
    { dashboard_id: 7, title: 'Launch week', owner: 'user' as const, group_id: 7, widgets: 1 },
  ]

  it('lists every dashboard under one heading, the system ones pinned first', () => {
    renderSidebar(dashboards)
    expect(screen.getByText('Dashboards')).toBeInTheDocument()
    expect(screen.queryByText('Yours')).not.toBeInTheDocument()
    const pinned = screen.getByRole('list', { name: 'Pinned dashboards' })
    expect(within(pinned).getAllByRole('link').map((l) => l.textContent)).toEqual(['Views', 'Web Vitals'])
    const mine = screen.getByRole('link', { name: 'Launch week' })
    expect(within(pinned).queryByRole('link', { name: 'Launch week' })).toBeNull()
    expect(screen.getByRole('link', { name: 'Web Vitals' }).compareDocumentPosition(mine) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('says how to get one of your own when there is none', () => {
    renderSidebar(dashboards.slice(0, 2))
    expect(screen.getByText('None of your own yet. Ask your agent to make one.')).toBeInTheDocument()
  })
})
