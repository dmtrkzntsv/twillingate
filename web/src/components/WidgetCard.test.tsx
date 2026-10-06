import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { QueryClientProvider } from '@tanstack/react-query'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ApiError, endpoints, type PageInfo, type Widget, type WidgetData, type WidgetDataQuery } from '@/lib/api'
import type { Filter } from '@/lib/table-view'
import { renderWithProviders } from '@/test/render'
import type { ShareContext } from './WidgetCard'
import WidgetCard from './WidgetCard'

vi.mock('@/lib/capture', () => ({ captureCard: vi.fn(), downloadBlob: vi.fn() }))
import { captureCard, downloadBlob } from '@/lib/capture'

const HOUR = 3_600_000

function statWidget(over: Partial<Widget> = {}): Widget {
  return {
    widget_id: 42,
    dashboard_id: 1,
    name: 'visitors',
    component: 'stat',
    title: 'Visitors',
    width: 3,
    height: 3,
    props: {},
    source: { type: 'sql', content: 'SELECT 1 AS value' },
    follows_project: true,
    follows_range: true,
    ...over,
  }
}

function sqlAnswer(over: Partial<WidgetData> = {}, rows: string[][] = [['12345']], truncated = false): WidgetData {
  return {
    widget_id: 42,
    source_type: 'sql',
    project_id: 7,
    from: '2026-09-20',
    to: '2026-09-26',
    cached_at: new Date(Date.now() - HOUR).toISOString(),
    refresh_after: new Date(Date.now() - 1000).toISOString(),
    removed: false,
    data: { columns: ['value'], rows, truncated },
    ...over,
  }
}

const params = { project_id: 7, from: '2026-09-20', to: '2026-09-26' }

function renderCard(widget: Widget = statWidget()) {
  return renderWithProviders(<WidgetCard widget={widget} params={params} />)
}

afterEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
})

describe('WidgetCard', () => {
  it('shows a skeleton while loading', () => {
    vi.spyOn(endpoints, 'widgetData').mockReturnValue(new Promise(() => {}))
    const { container } = renderCard()
    expect(container.querySelector('[data-slot=skeleton]')).toBeInTheDocument()
  })

  it('renders the component with its data, asking for the selection', async () => {
    const spy = vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer())
    renderCard()
    expect(await screen.findByText('12.3K')).toBeInTheDocument()
    expect(screen.getByText('Visitors')).toBeInTheDocument()
    expect(spy).toHaveBeenCalledWith(42, params)
  })

  it('keeps what a viewer changes under the dashboard and widget ids', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer({}, [['12'], ['30']]))
    renderCard(statWidget({ component: 'table', dashboard_id: 3 }))
    await user.click(await screen.findByRole('button', { name: /value/ }))
    expect(JSON.parse(localStorage.getItem('twillingate.widget.3.42.view')!)).toMatchObject({
      filters: [],
      sort: { column: 'value', dir: 'desc' },
    })
  })

  it('says "No data for this range" on an empty result', async () => {
    vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer({}, []))
    renderCard()
    expect(await screen.findByText('No data for this range')).toBeInTheDocument()
  })

  // Pins the fix for a whole-page crash: a query matching nothing is meant
  // to encode as rows: [] (internal/shared/readsql), but a `null` from an
  // older server or an untested path must not crash the render either.
  it('treats a null rows as empty rather than throwing', async () => {
    const answer = sqlAnswer()
    answer.data = { ...(answer.data as { columns: string[]; rows: string[][]; truncated: boolean }), rows: null as unknown as string[][] }
    vi.spyOn(endpoints, 'widgetData').mockResolvedValue(answer)
    renderCard()
    expect(await screen.findByText('No data for this range')).toBeInTheDocument()
  })

  it('says "Component removed" when the API answers removed', async () => {
    vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer({ removed: true, data: null }))
    renderCard()
    expect(await screen.findByText('Component removed')).toBeInTheDocument()
  })

  it('says "Component removed" without asking when the widget has no component', () => {
    const spy = vi.spyOn(endpoints, 'widgetData')
    renderCard(statWidget({ component: null }))
    expect(screen.getByText('Component removed')).toBeInTheDocument()
    expect(spy).not.toHaveBeenCalled()
  })

  it('says "Component removed" without asking when the registry has no such component', () => {
    const spy = vi.spyOn(endpoints, 'widgetData')
    renderCard(statWidget({ component: 'sparkle' }))
    expect(screen.getByText('Component removed')).toBeInTheDocument()
    expect(spy).not.toHaveBeenCalled()
  })

  it('asks for nothing while idle', () => {
    const spy = vi.spyOn(endpoints, 'widgetData')
    renderWithProviders(<WidgetCard widget={statWidget()} params={params} idle />)
    expect(spy).not.toHaveBeenCalled()
  })

  it('keeps the rendered data when a refresh fails, and says so quietly', async () => {
    vi.spyOn(endpoints, 'widgetData')
      .mockResolvedValueOnce(sqlAnswer())
      .mockRejectedValue(new ApiError(500, 'database is locked', 'internal'))
    renderCard()
    await userEvent.click(await screen.findByRole('button', { name: 'Refresh Visitors' }))
    expect(await screen.findByRole('button', { name: "Couldn't refresh: database is locked" })).toBeInTheDocument()
    expect(screen.getByText('12.3K')).toBeInTheDocument()
    expect(screen.queryByText("Couldn't load")).not.toBeInTheDocument()
  })

  it('says "Query no longer runs" on a 400, with the message folded', async () => {
    vi.spyOn(endpoints, 'widgetData').mockRejectedValue(new ApiError(400, 'no such column: visitors', 'invalid'))
    renderCard()
    expect(await screen.findByText('Query no longer runs')).toBeInTheDocument()
    expect(screen.queryByText('no such column: visitors')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /details/i }))
    expect(screen.getByText('no such column: visitors')).toBeInTheDocument()
  })

  it('says "Couldn\'t load" on a 5xx and retries on request', async () => {
    const spy = vi
      .spyOn(endpoints, 'widgetData')
      .mockRejectedValueOnce(new ApiError(500, 'boom', 'internal'))
      .mockResolvedValue(sqlAnswer())
    renderCard()
    expect(await screen.findByText("Couldn't load")).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('12.3K')).toBeInTheDocument()
    expect(spy).toHaveBeenCalledTimes(2)
  })

  it('says "Couldn\'t load" on a network failure', async () => {
    vi.spyOn(endpoints, 'widgetData').mockRejectedValue(new TypeError('Failed to fetch'))
    renderCard()
    expect(await screen.findByText("Couldn't load")).toBeInTheDocument()
  })

  it('marks a truncated result as partial', async () => {
    vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer({}, [['1']], true))
    renderCard()
    expect(await screen.findByText('partial: narrow the range or group the query')).toBeInTheDocument()
  })

  it('disables the refresh icon before refresh_after, and ignores clicks', async () => {
    const spy = vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer({ refresh_after: new Date(Date.now() + HOUR).toISOString() }))
    renderCard()
    await screen.findByText('12.3K')
    await userEvent.click(screen.getByRole('button', { name: 'Refresh Visitors' }))
    expect(spy).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'Refresh Visitors' })).toHaveAttribute('aria-disabled', 'true')
  })

  it('enables the refresh icon after refresh_after and asks for fresh data', async () => {
    const spy = vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer())
    renderCard()
    await screen.findByText('12.3K')
    const button = screen.getByRole('button', { name: 'Refresh Visitors' })
    expect(button).not.toHaveAttribute('aria-disabled', 'true')
    await userEvent.click(button)
    await waitFor(() => expect(spy).toHaveBeenCalledWith(42, { ...params, fresh: true }))
  })

  it('has no refresh icon on a markdown card', async () => {
    vi.spyOn(endpoints, 'widgetData').mockResolvedValue({
      widget_id: 43,
      source_type: 'md',
      removed: false,
      data: { markdown: '# Read me' },
    })
    renderCard(
      statWidget({
        widget_id: 43,
        component: 'markdown',
        title: undefined,
        source: { type: 'md', content: '# Read me' },
        follows_project: false,
        follows_range: false,
      }),
    )
    expect(await screen.findByRole('heading', { name: 'Read me' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Refresh/ })).not.toBeInTheDocument()
  })

  it('says "Nothing to show" for blank markdown, which has no range', async () => {
    vi.spyOn(endpoints, 'widgetData').mockResolvedValue({
      widget_id: 43,
      source_type: 'md',
      removed: false,
      data: { markdown: '  ' },
    })
    renderCard(statWidget({ widget_id: 43, component: 'markdown', source: { type: 'md', content: '' } }))
    expect(await screen.findByText('Nothing to show')).toBeInTheDocument()
  })
})

// A remote table as the server answers it: the page of rows, with the counts in `page`.
const ATTR_COLUMNS = ['Attribute', 'Value', 'Count']
const ATTR_ROWS = [
  ['$os', 'iOS', '412'],
  ['plan', 'pro', '205'],
]
const VIEW_KEY = 'twillingate.widget.1.42.view'
const PLAN: Filter = { column: 'Attribute', op: 'in', value: ['plan'] }

function remoteWidget(): Widget {
  return statWidget({ component: 'table', title: 'Attributes', props: { mode: 'remote' } })
}

function remoteAnswer(q: WidgetDataQuery, rows: string[][] = ATTR_ROWS, page: Partial<PageInfo> = {}): WidgetData {
  return sqlAnswer({
    data: { columns: ATTR_COLUMNS, rows, truncated: false },
    page: { offset: q.offset ?? 0, limit: 1000, matched: rows.length, total: rows.length, filters: [], ...page },
  })
}

function distinctAnswer(rows: string[][]): WidgetData {
  return sqlAnswer({
    data: { columns: ['value', 'rows'], rows, truncated: false },
    page: { offset: 0, limit: 1000, matched: rows.length, total: 9, filters: [] },
  })
}

function storeView(filters: Filter[], sort: { column: string; dir: 'asc' | 'desc' } | null = null) {
  localStorage.setItem(VIEW_KEY, JSON.stringify({ filters, sort }))
}

function cardIn(client: ReturnType<typeof renderWithProviders>['client'], widget: Widget, p: WidgetDataQuery) {
  return (
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <WidgetCard widget={widget} params={p} />
      </TooltipProvider>
    </QueryClientProvider>
  )
}

describe('WidgetCard with a remote table', () => {
  it('asks for the first page with no view, then sends a new filter as JSON on the first page', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) =>
      q.distinct
        ? distinctAnswer([
            ['$os', '12'],
            ['plan', '7'],
          ])
        : remoteAnswer(q),
    )
    renderCard(remoteWidget())
    expect(await screen.findByText('iOS')).toBeInTheDocument()
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][1]).toEqual(params)

    await user.click(screen.getByRole('button', { name: 'Filter' }))
    expect(await screen.findByRole('option', { name: /plan/ })).toHaveTextContent('7')
    expect(spy).toHaveBeenLastCalledWith(42, { ...params, distinct: 'Attribute' })
    await user.click(screen.getByRole('option', { name: /plan/ }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(spy).toHaveBeenLastCalledWith(42, { ...params, filters: JSON.stringify([PLAN]) }))
    expect(spy.mock.lastCall![1]).not.toHaveProperty('offset')
  })

  it('goes back to the first page, keeping the filters, when the range changes', async () => {
    const user = userEvent.setup()
    storeView([PLAN])
    const spy = vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) => remoteAnswer(q, ATTR_ROWS, { matched: 2500 }))
    const { client, rerender } = renderCard(remoteWidget())
    await screen.findByText('1–1,000 of 2,500')
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    await waitFor(() => expect(spy).toHaveBeenLastCalledWith(42, { ...params, filters: JSON.stringify([PLAN]), offset: 1000 }))
    await screen.findByText('1,001–2,000 of 2,500')

    const later = { ...params, from: '2026-09-21' }
    rerender(cardIn(client, remoteWidget(), later))
    await waitFor(() => expect(spy).toHaveBeenLastCalledWith(42, { ...later, filters: JSON.stringify([PLAN]) }))
    expect(spy.mock.calls.filter(([, q]) => q.from === later.from).every(([, q]) => q.offset === undefined)).toBe(true)
    await screen.findByText('1–1,000 of 2,500')

    // Back to the first range: its first page, not the one left there.
    rerender(cardIn(client, remoteWidget(), params))
    expect(await screen.findByText('1–1,000 of 2,500')).toBeInTheDocument()
    expect(screen.queryByText('1,001–2,000 of 2,500')).not.toBeInTheDocument()
  })

  it("shows the skeleton, not the last project's rows, while another project loads", async () => {
    vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) =>
      q.project_id === 8 ? new Promise<never>(() => {}) : remoteAnswer(q),
    )
    const { client, container, rerender } = renderCard(remoteWidget())
    await screen.findByText('iOS')
    rerender(cardIn(client, remoteWidget(), { ...params, project_id: 8 }))
    await waitFor(() => expect(screen.queryByText('iOS')).not.toBeInTheDocument())
    expect(container.querySelector('[data-slot=skeleton]')).toBeInTheDocument()
  })

  it("says the query no longer runs, without the last project's rows, when another project's is refused", async () => {
    vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) => {
      if (q.project_id === 8) throw new ApiError(400, 'no such view: v_gone', 'invalid')
      return remoteAnswer(q)
    })
    const { client, rerender } = renderCard(remoteWidget())
    await screen.findByText('iOS')
    rerender(cardIn(client, remoteWidget(), { ...params, project_id: 8 }))
    expect(await screen.findByText('Query no longer runs')).toBeInTheDocument()
    expect(screen.queryByText('iOS')).not.toBeInTheDocument()
  })

  it('refreshes with the filters, sort and page on screen', async () => {
    const user = userEvent.setup()
    storeView([PLAN], { column: 'Count', dir: 'desc' })
    const spy = vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) => remoteAnswer(q, ATTR_ROWS, { matched: 2500 }))
    renderCard(remoteWidget())
    await screen.findByText('1–1,000 of 2,500')
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    await screen.findByText('1,001–2,000 of 2,500')
    await user.click(screen.getByRole('button', { name: 'Refresh Attributes' }))
    await waitFor(() =>
      expect(spy).toHaveBeenLastCalledWith(42, {
        ...params,
        filters: JSON.stringify([PLAN]),
        sort: 'Count:desc',
        offset: 1000,
        fresh: true,
      }),
    )
  })

  it('keeps the rows and shows the refusal under the bar when the server refuses a view', async () => {
    const user = userEvent.setup()
    storeView([PLAN])
    vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) => {
      if (q.sort) throw new ApiError(400, 'sort: "Count" cannot be sorted here; pick another column', 'invalid')
      return remoteAnswer(q)
    })
    renderCard(remoteWidget())
    await screen.findByText('iOS')
    await user.click(screen.getByRole('button', { name: /Count/ }))
    expect(await screen.findByText(/cannot be sorted here/)).toHaveClass('text-destructive')
    expect(screen.getByText('iOS')).toBeInTheDocument()
    expect(screen.queryByText('Query no longer runs')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Attribute in plan' })).toHaveAttribute('aria-invalid', 'true')
  })

  it('says no rows match a filter, not that the range is empty', async () => {
    storeView([PLAN])
    vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) => remoteAnswer(q, [], { matched: 0, total: 40 }))
    renderCard(remoteWidget())
    expect(await screen.findByText('No rows match these filters')).toBeInTheDocument()
    expect(screen.queryByText('No data for this range')).not.toBeInTheDocument()
  })

  it('stops sending a stored filter on a column the answer does not have', async () => {
    const gone: Filter = { column: 'Platform', op: '=', value: 'web' }
    storeView([gone, PLAN])
    // As the server does: a filter on a column the query lacks is refused.
    const spy = vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) => {
      if (q.filters?.includes('Platform')) throw new ApiError(400, 'filters: no column "Platform"', 'invalid')
      return remoteAnswer(q)
    })
    renderCard(remoteWidget())
    await waitFor(() => expect(spy).toHaveBeenLastCalledWith(42, { ...params, filters: JSON.stringify([PLAN]) }))
    expect(await screen.findByText('iOS')).toBeInTheDocument()
    expect(screen.queryByText('Query no longer runs')).not.toBeInTheDocument()
    // Kept, greyed, and still stored.
    expect(screen.getByRole('button', { name: 'Platform = web' }).closest('[data-stale]')).not.toBeNull()
    expect(JSON.parse(localStorage.getItem(VIEW_KEY)!).filters).toEqual([gone, PLAN])
  })

  it('takes the sort a viewer stored before filters existed, once', async () => {
    localStorage.setItem('twillingate.widget.1.42.sort', JSON.stringify({ column: 'Count', dir: 'desc' }))
    const spy = vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) => remoteAnswer(q))
    renderCard(remoteWidget())
    await screen.findByText('iOS')
    expect(spy).toHaveBeenLastCalledWith(42, { ...params, sort: 'Count:desc' })
    expect(localStorage.getItem('twillingate.widget.1.42.sort')).toBeNull()
    expect(JSON.parse(localStorage.getItem(VIEW_KEY)!)).toMatchObject({ filters: [], sort: { column: 'Count', dir: 'desc' } })
  })

  it("never sends a local table's view to the server", async () => {
    const user = userEvent.setup()
    storeView([PLAN])
    const spy = vi.spyOn(endpoints, 'widgetData').mockImplementation(async (_, q) => remoteAnswer(q))
    renderCard(statWidget({ component: 'table', title: 'Attributes' }))
    // Filtered in the browser: the $os row is not shown.
    expect(await screen.findByText('pro')).toBeInTheDocument()
    expect(screen.queryByText('iOS')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Count/ }))
    await user.click(screen.getByRole('button', { name: 'Refresh Attributes' }))
    await waitFor(() => expect(spy).toHaveBeenCalledTimes(2))
    expect(spy.mock.calls.map(([, q]) => q)).toEqual([params, { ...params, fresh: true }])
  })

  describe('widget menu', () => {
    const shareCtx: ShareContext = { projectId: 7, projectName: 'blog', from: '2026-09-20', to: '2026-09-26', writable: true }
    const withShare = (share?: ShareContext) => renderWithProviders(<WidgetCard widget={statWidget()} params={params} share={share} />)

    it('offers Share… and Download PNG when writable', async () => {
      const user = userEvent.setup()
      vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer())
      withShare(shareCtx)
      await screen.findByText('12.3K')
      await user.click(screen.getByRole('button', { name: 'Widget actions' }))
      expect(await screen.findByRole('menuitem', { name: 'Share…' })).toBeEnabled()
      expect(screen.getByRole('menuitem', { name: 'Download PNG' })).toBeEnabled()
    })

    it('offers only Download PNG in read-only mode', async () => {
      const user = userEvent.setup()
      vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer())
      withShare({ ...shareCtx, writable: false })
      await screen.findByText('12.3K')
      await user.click(screen.getByRole('button', { name: 'Widget actions' }))
      expect(await screen.findByRole('menuitem', { name: 'Download PNG' })).toBeInTheDocument()
      expect(screen.queryByRole('menuitem', { name: 'Share…' })).not.toBeInTheDocument()
    })

    it('has no menu without a share context', async () => {
      vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer())
      withShare(undefined)
      await screen.findByText('12.3K')
      expect(screen.queryByRole('button', { name: 'Widget actions' })).not.toBeInTheDocument()
    })

    it('waits for data before offering either action', async () => {
      const user = userEvent.setup()
      vi.spyOn(endpoints, 'widgetData').mockReturnValue(new Promise(() => {}))
      withShare(shareCtx)
      await user.click(screen.getByRole('button', { name: 'Widget actions' }))
      expect(await screen.findByRole('menuitem', { name: 'Share…' })).toHaveAttribute('aria-disabled', 'true')
      expect(screen.getByRole('menuitem', { name: 'Download PNG' })).toHaveAttribute('aria-disabled', 'true')
    })

    it('captures the card and downloads the 2x image', async () => {
      const user = userEvent.setup()
      const image2x = new Blob(['2x'])
      vi.mocked(captureCard).mockResolvedValue({ image: new Blob(['1x']), image2x })
      vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer())
      withShare(shareCtx)
      await screen.findByText('12.3K')
      await user.click(screen.getByRole('button', { name: 'Widget actions' }))
      await user.click(await screen.findByRole('menuitem', { name: 'Download PNG' }))
      await waitFor(() => expect(downloadBlob).toHaveBeenCalledWith(image2x, 'visitors-2026-09-20-2026-09-26.png'))
      expect(captureCard).toHaveBeenCalledTimes(1)
      // The card for the capture is gone again.
      expect(document.querySelector('[data-share-card]')).toBeNull()
    })

    it('says so when the capture fails', async () => {
      const user = userEvent.setup()
      vi.mocked(captureCard).mockRejectedValue(new Error('boom'))
      vi.mocked(downloadBlob).mockClear()
      vi.spyOn(endpoints, 'widgetData').mockResolvedValue(sqlAnswer())
      withShare(shareCtx)
      await screen.findByText('12.3K')
      await user.click(screen.getByRole('button', { name: 'Widget actions' }))
      await user.click(await screen.findByRole('menuitem', { name: 'Download PNG' }))
      await waitFor(() => expect(document.querySelector('[data-share-card]')).toBeNull())
      expect(downloadBlob).not.toHaveBeenCalled()
    })
  })
})
