import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type ProjectUsage } from '@/lib/api'
import { dashboardsList } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Projects from './Projects'

const create = vi.fn()
vi.mock('@/hooks/use-project-actions', () => ({
  useProjectActions: () => ({ create, restore: vi.fn(), move: vi.fn(), pending: false }),
}))

function stats(project_id: number, over: Partial<ProjectUsage> = {}): ProjectUsage {
  return {
    project_id,
    series: [{ day: '2026-10-02', views: 5, events: 2, measures: 0, total_bytes: null }],
    totals: { views: 5, events: 2, measures: 0 },
    last_received_at: new Date(Date.now() - 120_000).toISOString(),
    first_day: '2026-09-01',
    size: { total_bytes: 2_300_000, measured_at: '2026-10-03' },
    ...over,
  }
}

beforeEach(() => {
  vi.restoreAllMocks()
  create.mockReset()
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList())
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [
    { project_id: 4, name: 'econumo.com', allowed_origins: ['https://econumo.com'] },
    { project_id: 5, name: 'quiet.dev', allowed_origins: [] },
    { project_id: 3, name: 'legacy', archived: true, allowed_origins: [] },
  ] })
  vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: '2026-09-03', to: '2026-10-02', database_bytes: 2_100_000_000, database_series: [
    { day: '2026-09-03', bytes: 2_000_000_000 }, { day: '2026-09-04', bytes: null }, { day: '2026-10-02', bytes: 2_100_000_000 },
  ], projects: [
    stats(4),
    stats(5, { last_received_at: null, totals: { views: 0, events: 0, measures: 0 }, size: null }),
    stats(3),
  ] })
  vi.spyOn(endpoints, 'projectActivity').mockResolvedValue({ projects: [
    { project_id: 4, last_event_day: '2026-10-02', new_submissions: 3, forms: 2, draft_forms: 1 },
    { project_id: 5, last_event_day: null, new_submissions: 0, forms: 0, draft_forms: 0 },
  ] })
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [{ project_id: 4, label: 'web', key: 'ak_1', state: 'active' }] })
  vi.spyOn(endpoints, 'limits').mockResolvedValue({ limits: [
    { group: 'retention', name: 'Event files', setting: 'RETENTION_DAYS', value: 7, default: 400, unit: 'days', description: 'the event files are kept this long' },
    { group: 'retention', name: 'Archived items', setting: 'RETENTION_ARCHIVED_DAYS', value: 0, default: 30, unit: 'days', zero: 'kept forever', description: 'deleted this long after archiving' },
    { group: 'ingest', name: 'Request body', value: 262144, unit: 'bytes', description: 'a larger request is refused with 413' },
  ], events_30d: { count: 48210, from: '2026-09-04', to: '2026-10-03' } })
})

function renderPage() {
  return renderWithProviders(<MemoryRouter><Projects /></MemoryRouter>)
}

describe('Projects', () => {
  it('shows a card per active project with its usage, and no size anywhere', async () => {
    renderPage()
    const card = await screen.findByRole('article', { name: 'econumo.com' })
    expect(within(card).getByText('2 min ago')).toBeInTheDocument()
    expect(within(card).getByText(/7 events/)).toBeInTheDocument()
    expect(within(card).getByText('1 active key')).toBeInTheDocument()
    expect(await screen.findByText('2 projects')).toBeInTheDocument()
    expect(screen.queryByText(/MB|GB|on disk/)).not.toBeInTheDocument()
  })

  it('sums the events of every project by month over six months, the last one so far', async () => {
    const usage = vi.spyOn(endpoints, 'usage')
    renderPage()
    const line = await screen.findByText(/^Events by month:/)
    expect(line).toHaveTextContent('Events by month: Oct 21 so far')
    expect(within(line).getByTitle('21 events')).toBeInTheDocument()
    expect(usage).toHaveBeenCalledWith(expect.objectContaining({ from: expect.stringMatching(/^\d{4}-\d{2}-01$/) }))
  })

  it("links a project's forms, its drafts and new submissions to its Forms tab", async () => {
    renderPage()
    const card = await screen.findByRole('article', { name: 'econumo.com' })
    const link = await within(card).findByRole('link', { name: '2 forms · 1 draft · 3 new submissions' })
    expect(link).toHaveAttribute('href', '/projects/4/forms')
  })

  it('leaves the forms line out for a project with no forms', async () => {
    renderPage()
    await screen.findByRole('link', { name: /2 forms/ })
    const card = screen.getByRole('article', { name: 'quiet.dev' })
    expect(within(card).queryByRole('link', { name: /form/ })).not.toBeInTheDocument()
  })

  it('says a project never sent anything instead of failing', async () => {
    renderPage()
    const card = await screen.findByRole('article', { name: 'quiet.dev' })
    expect(within(card).getByText('Nothing received yet')).toBeInTheDocument()
  })

  it('shows dashes, not zeros, while stats and keys load, and no count before projects', async () => {
    vi.spyOn(endpoints, 'usage').mockReturnValue(new Promise(() => {}))
    vi.spyOn(endpoints, 'keys').mockReturnValue(new Promise(() => {}))
    renderPage()
    const card = await screen.findByRole('article', { name: 'econumo.com' })
    expect(within(card).queryByText(/0 events/)).not.toBeInTheDocument()
    expect(within(card).queryByText(/0 active keys/)).not.toBeInTheDocument()
  })

  it('links the whole card to its project, and leaves the origins to the project page', async () => {
    renderPage()
    const card = await screen.findByRole('article', { name: 'econumo.com' })
    expect(within(card).getByRole('link', { name: 'econumo.com' })).toHaveAttribute('href', '/projects/4')
    expect(within(card).queryByText(/econumo\.com\//)).not.toBeInTheDocument()
    expect(within(card).queryByText('No origins')).not.toBeInTheDocument()
  })

  it('shows the live cards in the list order, each sortable', async () => {
    renderPage()
    await screen.findByRole('article', { name: 'econumo.com' })
    expect(screen.getAllByRole('article').map((a) => a.getAttribute('aria-label'))).toEqual(['econumo.com', 'quiet.dev'])
    for (const name of ['econumo.com', 'quiet.dev']) {
      expect(screen.getByRole('article', { name }).parentElement).toHaveAttribute('aria-roledescription', 'sortable')
    }
  })

  it('does not drag cards in reporting dev, which takes no writes', async () => {
    vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ ...dashboardsList(), dev: true })
    renderPage()
    await screen.findByRole('article', { name: 'econumo.com' })
    await vi.waitFor(() =>
      expect(screen.getByRole('article', { name: 'econumo.com' }).parentElement).not.toHaveAttribute('aria-roledescription')
    )
  })

  it('leaves archived projects to the Archive page', async () => {
    renderPage()
    await screen.findByRole('article', { name: 'econumo.com' })
    expect(screen.queryByRole('article', { name: 'legacy' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Archived/ })).not.toBeInTheDocument()
  })

  describe('the ingest switch', () => {
    const banner = 'Ingest is disabled on this server: new events and form submissions are refused.'

    it('says ingest is disabled at the top of the page, under the header, above the cards', async () => {
      vi.spyOn(endpoints, 'limits').mockResolvedValue({ limits: [], ingest_disabled: true })
      renderPage()
      const notice = await screen.findByText(banner)
      expect(notice).toHaveAttribute('role', 'status')
      const heading = screen.getByRole('heading', { name: 'Projects' })
      const card = await screen.findByRole('article', { name: 'econumo.com' })
      expect(heading.compareDocumentPosition(notice) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      expect(notice.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    })

    it.each([
      ['off', { limits: [], ingest_disabled: false }],
      ['not reported (an older server)', { limits: [] }],
    ])('says nothing when the switch is %s', async (_name, answer) => {
      vi.spyOn(endpoints, 'limits').mockResolvedValue(answer)
      renderPage()
      await screen.findByRole('article', { name: 'econumo.com' })
      await waitFor(() => expect(endpoints.limits).toHaveBeenCalled())
      expect(screen.queryByText(banner)).not.toBeInTheDocument()
    })
  })

  it('creates a project and shows its key and snippet once', async () => {
    const user = userEvent.setup()
    create.mockResolvedValue({ project_id: 9, key: 'ak_new', snippet: '<script src="…"></script>' })
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'New project' }))
    await user.type(screen.getByLabelText('Name'), 'shop')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    expect(create).toHaveBeenCalledWith({ name: 'shop', allowed_origins: [] })
    expect(await screen.findByText('ak_new')).toBeInTheDocument()
    expect(screen.getByText('<script src="…"></script>')).toBeInTheDocument()
  })

  it('says why the projects did not load, with Retry, instead of a blank page', async () => {
    const user = userEvent.setup()
    const projects = vi.spyOn(endpoints, 'projects').mockRejectedValueOnce(new Error('registry unavailable'))
    renderPage()
    expect(await screen.findByText(/Couldn't load projects\. registry unavailable/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('article', { name: 'econumo.com' })).toBeInTheDocument()
    expect(projects).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(/Couldn't load/)).not.toBeInTheDocument()
  })

  it('shows one error line above the grid when stats fail, not dashes and skeletons forever', async () => {
    const user = userEvent.setup()
    const stats = vi.spyOn(endpoints, 'usage').mockRejectedValueOnce(new Error('stats exploded'))
    const { container } = renderPage()
    expect(await screen.findByText(/Couldn't load usage\. stats exploded/)).toBeInTheDocument()
    const card = screen.getByRole('article', { name: 'econumo.com' })
    expect(card.querySelector('[data-slot="skeleton"]')).toBeNull()
    expect(container.querySelectorAll('[role="alert"]')).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await within(screen.getByRole('article', { name: 'econumo.com' })).findByText(/7 events/)).toBeInTheDocument()
    // The cards' 30 days, not the months line's own query.
    expect(stats.mock.calls.filter(([q]) => q.from === undefined)).toHaveLength(2)
    expect(screen.queryByText(/Couldn't load/)).not.toBeInTheDocument()
  })

  it('shows the keys error when only keys fail', async () => {
    vi.spyOn(endpoints, 'keys').mockRejectedValueOnce(new Error('keys exploded'))
    renderPage()
    expect(await screen.findByText(/Couldn't load keys\. keys exploded/)).toBeInTheDocument()
  })
})
