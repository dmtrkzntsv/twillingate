import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, endpoints, type Widget, type WidgetData } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import WidgetCard from './WidgetCard'

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
    expect(localStorage.getItem('twillingate.widget.3.42.sort')).toBe('{"column":"value","dir":"desc"}')
    localStorage.clear()
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
    expect(await screen.findByRole('img', { name: "Couldn't refresh: database is locked" })).toBeInTheDocument()
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
    const spy = vi
      .spyOn(endpoints, 'widgetData')
      .mockResolvedValue(sqlAnswer({ refresh_after: new Date(Date.now() + HOUR).toISOString() }))
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
      })
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
