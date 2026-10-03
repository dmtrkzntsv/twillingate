import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type ProjectStats } from '@/lib/api'
import { dashboardsList } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Projects from './Projects'

const create = vi.fn()
vi.mock('@/hooks/use-project-actions', () => ({
  useProjectActions: () => ({ create, restore: vi.fn(), pending: false }),
}))

function stats(project_id: number, over: Partial<ProjectStats> = {}): ProjectStats {
  return {
    project_id,
    series: [{ day: '2026-10-02', views: 5, events: 2, measures: 0 }],
    totals: { views: 5, events: 2, measures: 0 },
    last_received_at: new Date(Date.now() - 120_000).toISOString(),
    first_day: '2026-09-01', raw_days: 30, rolled_up_days: 2,
    size: { raw_bytes: 1_000_000, aggregate_bytes: 1_300_000, total_bytes: 2_300_000 },
    unused_attributes: [],
    ...over,
  }
}

beforeEach(() => {
  vi.restoreAllMocks()
  create.mockReset()
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList())
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [
    { project_id: 4, name: 'econumo.com', allowed_origins: ['https://econumo.com'] },
    { project_id: 5, name: 'quiet.dev', allowed_origins: [], attributes: ['plan'] },
    { project_id: 3, name: 'legacy', archived: true, allowed_origins: [] },
  ] })
  vi.spyOn(endpoints, 'stats').mockResolvedValue({ from: '2026-09-03', to: '2026-10-02', database_bytes: 2_100_000_000, projects: [
    stats(4),
    stats(5, { last_received_at: null, totals: { views: 0, events: 0, measures: 0 }, size: null }),
    stats(3),
  ] })
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [{ project_id: 4, label: 'web', key: 'ak_1', state: 'active' }] })
  vi.spyOn(endpoints, 'limits').mockResolvedValue({ limits: [
    { setting: 'VIEWS_DIMENSIONS_TOP_N', value: 0, default: 1000, caps: 'values per views breakdown' },
    { setting: 'PRODUCT_ATTRIBUTES_TOP_N', value: 100, default: 100, caps: 'values per attribute key' },
    { setting: 'IDENTITIES_TOP_N', value: 1000, default: 1000, caps: 'users and groups' },
  ] })
})

function renderPage() {
  return renderWithProviders(<MemoryRouter><Projects /></MemoryRouter>)
}

describe('Projects', () => {
  it('shows a card per active project with its usage, and the database size', async () => {
    renderPage()
    const card = await screen.findByRole('article', { name: 'econumo.com' })
    expect(within(card).getByText('2 min ago')).toBeInTheDocument()
    expect(within(card).getByText(/7 events/)).toBeInTheDocument()
    expect(within(card).getByText(/2\.3 MB/)).toBeInTheDocument()
    expect(within(card).getByText(/1 active key/)).toBeInTheDocument()
    expect(screen.getByText(/2 projects · 2\.1 GB on disk/)).toBeInTheDocument()
  })

  it('says a project never sent anything instead of failing', async () => {
    renderPage()
    const card = await screen.findByRole('article', { name: 'quiet.dev' })
    expect(within(card).getByText('Nothing received yet')).toBeInTheDocument()
  })

  it('keeps archived projects in a collapsed group', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByRole('article', { name: 'econumo.com' })
    expect(screen.queryByRole('article', { name: 'legacy' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Archived \(1\)/ }))
    expect(screen.getByRole('article', { name: 'legacy' })).toBeInTheDocument()
  })

  it('shows the caps, 0 as no cap', async () => {
    renderPage()
    const panel = await screen.findByRole('region', { name: 'Limits' })
    expect(within(panel).getByText('VIEWS_DIMENSIONS_TOP_N')).toBeInTheDocument()
    expect(within(panel).getByText('no cap')).toBeInTheDocument()
  })

  it('creates a project and shows its key and snippet once', async () => {
    const user = userEvent.setup()
    create.mockResolvedValue({ project_id: 9, key: 'ak_new', snippet: '<script src="…"></script>' })
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'New project' }))
    await user.type(screen.getByLabelText('Name'), 'shop')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    expect(create).toHaveBeenCalledWith({ name: 'shop', allowed_origins: [], attributes: [] })
    expect(await screen.findByText('ak_new')).toBeInTheDocument()
    expect(screen.getByText('<script src="…"></script>')).toBeInTheDocument()
  })
})
