import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { ApiError, endpoints, type DashboardDetail, type Project } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import ProjectTabsDialog from './ProjectTabsDialog'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    endpoints: {
      ...actual.endpoints,
      projects: vi.fn(),
      addProjectTab: vi.fn(),
      removeProjectTab: vi.fn(),
      setProjectTab: vi.fn(),
    },
  }
})

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn() }),
}))

function project(project_id: number, name: string, archived = false): Project {
  return { project_id, name, archived, allowed_origins: [] }
}

const dashboard: DashboardDetail = {
  dashboard_id: 13,
  title: 'Marketing',
  owner: 'user',
  group_id: 13,
  widgets: [],
  follows_project: false,
  follows_range: false,
  tabs: [{ dashboard_id: 13, title: 'Marketing' }],
  project_ids: [2],
  sidebar: true,
  project_tab: false,
}

function renderDialog(d: DashboardDetail = dashboard) {
  const onOpenChange = vi.fn()
  const view = renderWithProviders(<ProjectTabsDialog dashboard={d} open onOpenChange={onOpenChange} />)
  return { ...view, onOpenChange }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(endpoints.projects).mockResolvedValue({
    projects: [project(1, 'Blog'), project(2, 'Shop'), project(3, 'Old site', true), project(4, 'Docs')],
  })
  vi.mocked(endpoints.addProjectTab).mockResolvedValue({ tabs: [] })
  vi.mocked(endpoints.removeProjectTab).mockResolvedValue({ tabs: [] })
  vi.mocked(endpoints.setProjectTab).mockResolvedValue({} as never)
})

describe('ProjectTabsDialog', () => {
  it('is titled Project tabs and checks the boxes of the projects in project_ids', async () => {
    renderDialog()

    expect(await screen.findByRole('dialog', { name: 'Project tabs' })).toBeInTheDocument()
    expect(await screen.findByRole('checkbox', { name: 'Shop' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Blog' })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Docs' })).not.toBeChecked()
  })

  it('lists active projects first, then archived ones marked (archived)', async () => {
    renderDialog()

    await screen.findByRole('checkbox', { name: 'Shop' })
    expect(screen.getAllByRole('checkbox').map((c) => c.closest('label')?.textContent)).toEqual([
      'Blog',
      'Shop',
      'Docs',
      'Old site (archived)',
    ])
  })

  it('checking an unchecked box adds the dashboard to that project', async () => {
    renderDialog()
    await userEvent.click(await screen.findByRole('checkbox', { name: 'Blog' }))

    await waitFor(() => expect(endpoints.addProjectTab).toHaveBeenCalledWith(1, { dashboard_id: 13 }))
  })

  it('unchecking a checked box removes the dashboard from that project', async () => {
    renderDialog()
    await userEvent.click(await screen.findByRole('checkbox', { name: 'Shop' }))

    await waitFor(() => expect(endpoints.removeProjectTab).toHaveBeenCalledWith(2, 13))
  })

  it('shows a refusal as a toast and leaves the box as the server has it', async () => {
    vi.mocked(endpoints.removeProjectTab).mockRejectedValue(new ApiError(400, 'would be unreachable'))
    renderDialog()
    await userEvent.click(await screen.findByRole('checkbox', { name: 'Shop' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('would be unreachable'))
    expect(screen.getByRole('checkbox', { name: 'Shop' })).toBeChecked()
  })

  it('toggling Add to new projects sets project_tab and refreshes the dashboards', async () => {
    const { client } = renderDialog()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const toggle = await screen.findByRole('switch', { name: 'Add to new projects' })
    expect(toggle).not.toBeChecked()
    await userEvent.click(toggle)

    await waitFor(() => expect(endpoints.setProjectTab).toHaveBeenCalledWith(13, true))
    await waitFor(() => {
      expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard'] })
      expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboards'] })
    })
  })

  it('turns the switch off when project_tab is on', async () => {
    renderDialog({ ...dashboard, project_tab: true })
    const toggle = await screen.findByRole('switch', { name: 'Add to new projects' })
    expect(toggle).toBeChecked()
    await userEvent.click(toggle)

    await waitFor(() => expect(endpoints.setProjectTab).toHaveBeenCalledWith(13, false))
  })
})
