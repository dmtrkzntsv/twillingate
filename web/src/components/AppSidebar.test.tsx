import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
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

  it('is closed elsewhere, and opens to links that are not active there', async () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/dashboards/1']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={1} />
        </SidebarProvider>
      </MemoryRouter>
    )
    const toggle = screen.getByRole('button', { name: 'Gallery' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('link', { name: 'Components' })).not.toBeInTheDocument()
    await userEvent.click(toggle)
    expect(screen.getByRole('link', { name: 'Components' })).not.toHaveAttribute('data-active', 'true')
  })

  it('stays open with the sidebar down to icons, where its heading is hidden', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/dashboards/1']}>
        <SidebarProvider defaultOpen={false}>
          <AppSidebar dashboards={[]} currentId={1} />
        </SidebarProvider>
      </MemoryRouter>
    )
    expect(screen.getByRole('link', { name: 'Components' })).toBeInTheDocument()
  })

  it('links to the dashboards gallery, labelled "Dashboards", active there and not on Components (D17)', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/gallery/dashboards']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} />
        </SidebarProvider>
      </MemoryRouter>
    )
    const link = screen.getByRole('link', { name: 'Dashboards' })
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

  it('shows the system group once, named Reports, linking to its first member', () => {
    renderSidebar(dashboards, 1)
    expect(screen.getByRole('link', { name: 'Reports' })).toHaveAttribute('href', '/dashboards/1')
    expect(screen.queryByRole('link', { name: 'Views' })).not.toBeInTheDocument()
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

  it('names an entry by its group_title, not its first tab', () => {
    const named = dashboards.map((d) => (d.group_id === 13 ? { ...d, group_title: 'Ops' } : d))
    renderSidebar(named, 0)
    expect(screen.getByRole('link', { name: 'Ops' })).toHaveAttribute('href', '/dashboards/13')
    expect(screen.queryByRole('link', { name: 'Marketing' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Ops actions' })).toBeInTheDocument()
  })

  it('renames a group in place from its menu: Rename, type, Enter sends the PATCH and closes the field', async () => {
    const renameGroup = vi.spyOn(endpoints, 'renameGroup').mockResolvedValue({ dashboard_id: 13 } as DashboardInfo)
    renderSidebar(dashboards, 0)

    await userEvent.click(screen.getByRole('button', { name: 'Marketing actions' }))
    await userEvent.click(screen.getByRole('menuitem', { name: 'Rename' }))
    const field = screen.getByRole('textbox', { name: 'Group name' })
    expect(field).toHaveValue('Marketing')
    expect(screen.queryByRole('link', { name: 'Marketing' })).not.toBeInTheDocument()
    await userEvent.clear(field)
    await userEvent.type(field, 'Platform{Enter}')

    expect(renameGroup).toHaveBeenCalledWith(13, 'Platform')
    await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Group name' })).not.toBeInTheDocument())
  })

  it('offers Rename on user groups only', async () => {
    renderSidebar(dashboards, 0)
    await userEvent.click(screen.getByRole('button', { name: 'Reports actions' }))
    expect(screen.queryByRole('menuitem', { name: 'Rename' })).not.toBeInTheDocument()
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
    expect(screen.getByRole('link', { name: 'Reports' })).not.toHaveAttribute('aria-roledescription')
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
    renderSidebar([{ dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1, widgets: 1, sidebar: true, project_tab: false }])
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

describe('Shares link', () => {
  it('sits in the footer under Archive and is active on /shares', () => {
    renderWithProviders(
      <MemoryRouter initialEntries={['/shares']}>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} />
        </SidebarProvider>
      </MemoryRouter>
    )
    const shares = screen.getByRole('link', { name: 'Shares' })
    expect(shares).toHaveAttribute('href', '/shares')
    expect(shares).toHaveAttribute('data-active', 'true')
    const footer = document.querySelector('[data-sidebar="footer"]') as HTMLElement
    expect(footer).toContainElement(shares)
    expect(within(footer).getByRole('link', { name: 'Archive' }).compareDocumentPosition(shares) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('is absent in dev mode, as Projects is', () => {
    renderWithProviders(
      <MemoryRouter>
        <SidebarProvider>
          <AppSidebar dashboards={[]} currentId={0} readOnly />
        </SidebarProvider>
      </MemoryRouter>
    )
    expect(screen.queryByRole('link', { name: 'Shares' })).not.toBeInTheDocument()
  })
})

describe('Dashboards group', () => {
  const dashboards = [
    { dashboard_id: 1, title: 'Views', owner: 'system' as const, group_id: 1, widgets: 1, sidebar: true, project_tab: false },
    { dashboard_id: 2, title: 'Web Vitals', owner: 'system' as const, group_id: 2, widgets: 1, sidebar: true, project_tab: false },
    { dashboard_id: 7, title: 'Launch week', owner: 'user' as const, group_id: 7, widgets: 1, sidebar: true, project_tab: false },
  ]

  it('lists every dashboard under one heading, the built-in ones first with a badge', () => {
    renderSidebar(dashboards)
    expect(screen.getByText('Dashboards')).toBeInTheDocument()
    expect(screen.queryByText('Yours')).not.toBeInTheDocument()
    const pinned = screen.getByRole('list', { name: 'Built-in dashboards' })
    // The badge is shown, not read: the links keep the dashboards' names.
    const builtIn = within(pinned).getAllByRole('link')
    expect(builtIn).toHaveLength(2)
    expect(builtIn[0]).toHaveAccessibleName('Views')
    expect(builtIn[1]).toHaveAccessibleName('Web Vitals')
    const mine = screen.getByRole('link', { name: 'Launch week' })
    expect(within(pinned).getAllByText('Built-in')).toHaveLength(2)
    expect(within(pinned).getAllByText('Built-in')[0]).toHaveAttribute('title', 'Comes with twillingate and is always listed first')
    expect(within(mine).queryByText('Built-in')).toBeNull()
    expect(within(pinned).queryByRole('link', { name: 'Launch week' })).toBeNull()
    expect(screen.getByRole('link', { name: 'Web Vitals' }).compareDocumentPosition(mine) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('leaves out a group whose sidebar flag is off, system or your own', () => {
    renderSidebar(
      dashboards.map((d) => (d.dashboard_id === 2 || d.dashboard_id === 7 ? { ...d, sidebar: false } : d))
    )
    expect(screen.getByRole('link', { name: 'Views' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Web Vitals' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Launch week' })).not.toBeInTheDocument()
  })

  it('says how to get one of your own when there is none', () => {
    renderSidebar(dashboards.slice(0, 2))
    expect(screen.getByText('None of your own yet. Ask your agent to make one.')).toBeInTheDocument()
  })
})

describe('AppSidebar footer', () => {
  it('holds Archive, then Shares, above Log out', () => {
    localStorage.setItem('twillingate.token', 'pasted')
    renderSidebar([{ dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1, widgets: 1, sidebar: true, project_tab: false }])
    const footer = document.querySelector('[data-sidebar="footer"]') as HTMLElement
    const archive = within(footer).getByRole('link', { name: 'Archive' })
    const shares = within(footer).getByRole('link', { name: 'Shares' })
    const logOut = within(footer).getByRole('button', { name: 'Log out' })
    expect(archive.compareDocumentPosition(shares) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(shares.compareDocumentPosition(logOut) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('keeps Archive without a login to forget', () => {
    renderSidebar()
    const footer = document.querySelector('[data-sidebar="footer"]') as HTMLElement
    expect(within(footer).getByRole('link', { name: 'Archive' })).toBeInTheDocument()
    expect(within(footer).queryByRole('button', { name: 'Log out' })).toBeNull()
  })
})
