import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router'
import { endpoints, type WidgetShare } from '@/lib/api'
import { dashboardsList } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Shares from './Shares'

const setArchiveAfter = vi.fn()
const archive = vi.fn()
vi.mock('@/hooks/use-widget-share-actions', () => ({
  useWidgetShareActions: () => ({ pending: false, create: vi.fn(), setArchiveAfter, archive, restore: vi.fn() }),
}))

function share(id: string, over: Partial<WidgetShare> = {}): WidgetShare {
  return {
    id,
    url: `https://t.example.com/share/${id}`,
    image_url: `https://t.example.com/share/${id}.png`,
    image_2x_url: `https://t.example.com/share/${id}@2x.png`,
    widget_id: 7,
    dashboard_id: 3,
    dashboard_title: 'Launch week',
    project_id: 4,
    project_name: 'econumo.com',
    from: '2026-09-05',
    to: '2026-10-04',
    title: 'Visitors',
    created_at: '2026-10-05T09:30:00Z',
    archive_at: '2026-11-04T09:30:00Z',
    archived_at: null,
    caption_project: true,
    caption_range: true,
    ...over,
  }
}

const newer = share('0194a000-0000-7000-8000-000000000002', { title: 'Top pages', created_at: '2026-10-06T08:00:00Z', archive_at: null })
const older = share('0194a000-0000-7000-8000-000000000001')

function Where() {
  const l = useLocation()
  return <output data-testid="where">{l.pathname + l.search}</output>
}

function renderShares(path = '/shares') {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Shares />
      <Where />
    </MemoryRouter>
  )
}

beforeEach(() => {
  // The archive date shows its year only when it is not this one: "now" is
  // pinned in 2026, the fixtures' year. Only Date is faked, so the queries'
  // and userEvent's timers still run.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-06T12:00:00Z'))
  vi.restoreAllMocks()
  setArchiveAfter.mockReset()
  archive.mockReset()
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList())
  vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({ shares: [newer, older] })
})

afterEach(() => {
  vi.useRealTimers()
})

/** The body row holding `title`. */
async function row(title: string): Promise<HTMLElement> {
  return (await screen.findByText(title)).closest('tr')!
}

describe('Shares', () => {
  it('lists live shares as the API orders them, newest first', async () => {
    renderShares()
    await screen.findByText('Top pages')
    expect(endpoints.widgetShares).toHaveBeenCalledWith({ state: 'live' })
    const rows = screen.getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('Top pages')).toBeInTheDocument()
    expect(within(rows[1]).getByText('Visitors')).toBeInTheDocument()
  })

  it('shows the thumbnail, widget, dashboard link, project, range, date and archive choice', async () => {
    renderShares()
    const r = await row('Visitors')
    const img = within(r).getByRole('img')
    expect(img).toHaveAttribute('src', older.image_url)
    expect(img).toHaveAttribute('loading', 'lazy')
    const thumb = img.closest('a')!
    expect(thumb).toHaveAttribute('href', older.url)
    expect(thumb).toHaveAttribute('target', '_blank')
    expect(thumb).toHaveAttribute('rel', 'noopener')
    expect(within(r).getByRole('link', { name: 'Launch week' })).toHaveAttribute('href', '/dashboards/3')
    expect(within(r).getAllByText(/econumo\.com/).length).toBeGreaterThan(0)
    expect(within(r).getAllByText('Sep 5 – Oct 4, 2026').length).toBeGreaterThan(0)
    expect(within(r).getAllByText('Oct 5, 2026').length).toBeGreaterThan(0)
    // A dated share's select stands on its date, then offers the periods.
    const select = within(r).getAllByRole('combobox')[1]
    expect(select).toHaveDisplayValue('Nov 4')
    expect(within(select).getAllByRole('option').map((o) => o.textContent)).toEqual(['Nov 4', '1 week', '1 month', '3 months', '1 year', 'Project lifetime'])
    expect(within(r).getByText('archives Nov 4')).toBeInTheDocument()
  })

  it('adds the year to an archive date in another year', async () => {
    vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({ shares: [{ ...older, archive_at: '2027-01-04T09:30:00Z' }] })
    renderShares()
    const r = await row('Visitors')
    expect(within(r).getAllByRole('combobox')[1]).toHaveDisplayValue('Jan 4, 2027')
    expect(within(r).getByText('archives Jan 4, 2027')).toBeInTheDocument()
  })

  it('shows Project lifetime with no date note for a share that never archives', async () => {
    renderShares()
    const r = await row('Top pages')
    for (const select of within(r).getAllByRole('combobox')) expect(select).toHaveDisplayValue('Project lifetime')
    expect(within(r).queryByText(/^archives /)).not.toBeInTheDocument()
    expect(within(r).getAllByRole('option')).toHaveLength(10)
  })

  it('shows the copied title and project, with no dashboard link, once the widget is gone', async () => {
    vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({
      shares: [share('0194a000-0000-7000-8000-000000000003', { widget_id: null, dashboard_id: null, dashboard_title: null, title: 'Gone chart' })],
    })
    renderShares()
    const r = await row('Gone chart')
    expect(within(r).getAllByText(/econumo\.com/).length).toBeGreaterThan(0)
    expect(within(r).queryByRole('link', { name: 'Launch week' })).not.toBeInTheDocument()
    expect(within(r).getAllByRole('link')).toHaveLength(1)
  })

  it('changes the archive date from the select', async () => {
    renderShares()
    const r = await row('Visitors')
    await userEvent.selectOptions(within(r).getAllByRole('combobox')[1], '90d')
    expect(setArchiveAfter).toHaveBeenCalledWith(older.id, '90d')
  })

  it('changes it from the folded line too, which a phone shows instead of the column', async () => {
    renderShares()
    const r = await row('Visitors')
    const folded = r.querySelector('.xl\\:hidden') as HTMLElement
    await userEvent.selectOptions(within(folded).getByRole('combobox'), '365d')
    expect(setArchiveAfter).toHaveBeenCalledWith(older.id, '365d')
  })

  it('archives with no confirmation', async () => {
    renderShares()
    const r = await row('Visitors')
    await userEvent.click(within(r).getByRole('button', { name: 'Archive' }))
    expect(archive).toHaveBeenCalledWith(older.id)
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('has a copy button for the link and one for the embed code', async () => {
    renderShares()
    const r = await row('Visitors')
    // Told apart at a glance: a link icon and a code icon, not two copy icons.
    expect(within(r).getByRole('button', { name: 'Copy link' }).querySelector('svg')).toHaveClass('lucide-link')
    expect(within(r).getByRole('button', { name: 'Copy embed code' }).querySelector('svg')).toHaveClass('lucide-code')
  })

  it('narrows to one widget with ?widget=, and a chip clears the filter', async () => {
    renderShares('/shares?widget=7')
    await screen.findByText('Top pages')
    expect(endpoints.widgetShares).toHaveBeenCalledWith({ widget_id: 7, state: 'live' })
    const chip = screen.getByRole('button', { name: 'Widget 7 ×' })
    await userEvent.click(chip)
    await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent(/^\/shares$/))
    await waitFor(() => expect(endpoints.widgetShares).toHaveBeenLastCalledWith({ state: 'live' }))
    expect(screen.queryByRole('button', { name: 'Widget 7 ×' })).not.toBeInTheDocument()
  })

  it('ignores a ?widget= that is not a widget id', async () => {
    renderShares('/shares?widget=abc')
    await screen.findByText('Top pages')
    expect(endpoints.widgetShares).toHaveBeenCalledWith({ state: 'live' })
    expect(screen.queryByRole('button', { name: /^Widget/ })).not.toBeInTheDocument()
  })

  it('says how to share when there are none', async () => {
    vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({ shares: [] })
    renderShares()
    expect(await screen.findByText("No shared widgets. Share one from a widget's menu: Share…")).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('says so, with a retry, when the list does not load', async () => {
    vi.spyOn(endpoints, 'widgetShares').mockRejectedValue(new Error('boom'))
    renderShares()
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load shares. boom")
  })

  it('folds range, created and archive-after below xl into a line under the widget', async () => {
    renderShares()
    const r = await row('Visitors')
    const heads = screen.getAllByRole('columnheader')
    for (const name of ['Range', 'Created', 'Archive after']) {
      expect(heads.find((h) => h.textContent === name)).toHaveClass('hidden', 'xl:table-cell')
    }
    const cells = within(r).getAllByRole('cell')
    expect(cells.filter((c) => c.classList.contains('hidden') && c.classList.contains('xl:table-cell'))).toHaveLength(3)
    const folded = r.querySelector('.xl\\:hidden') as HTMLElement
    expect(folded).toHaveTextContent('Sep 5 – Oct 4, 2026')
    expect(folded).toHaveTextContent('Oct 5, 2026')
    expect(within(folded).getByRole('combobox')).toHaveDisplayValue('Nov 4')
  })

  it('shows the dashboard and the project on their own lines, with the full text in a title', async () => {
    const long = 'A dashboard with a very long name that will be cut short on a phone'
    vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({ shares: [share('0194a000-0000-7000-8000-000000000004', { dashboard_title: long, project_name: 'long.example.com' })] })
    renderShares()
    const r = await row('Visitors')
    const dash = within(r).getByRole('link', { name: long })
    expect(dash.parentElement).toHaveAttribute('title', long)
    expect(dash.parentElement).toHaveClass('truncate')
    const project = within(r).getByText('long.example.com')
    expect(project).toHaveAttribute('title', 'long.example.com')
    expect(project).toHaveClass('truncate')
    expect(project).not.toBe(dash.parentElement)
  })

  it('folds a never-archiving share to Project lifetime', async () => {
    renderShares()
    const r = await row('Top pages')
    expect(within(r.querySelector('.xl\\:hidden') as HTMLElement).getByRole('combobox')).toHaveDisplayValue('Project lifetime')
  })
})
