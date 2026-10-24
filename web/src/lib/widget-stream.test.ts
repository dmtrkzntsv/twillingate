import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'
import { ApiError, endpoints, type Widget, type WidgetData, type WidgetDataQuery, type WidgetStreamItem } from './api'
import { loadDashboard, refreshDashboard, widgetQuery } from './widget-query'
import { STREAM_MAX } from './widget-stream'

function widget(id: number, extra: Partial<Widget> = {}): Widget {
  return {
    widget_id: id,
    dashboard_id: 1,
    name: `w${id}`,
    component: 'stat',
    width: 3,
    height: 3,
    props: {},
    source: { type: 'query', content: '{}' },
    follows_project: true,
    follows_range: true,
    ...extra,
  }
}

function answer(id: number, extra: Partial<WidgetData> = {}): WidgetData {
  return {
    widget_id: id,
    source_type: 'query',
    data: { columns: [{ name: 'value', type: 'number' }], rows: [[String(id)]] },
    ...extra,
  } as WidgetData
}

const params: WidgetDataQuery = { project_id: 1, from: '2026-10-01', to: '2026-10-07' }

/** A stream the test writes to, opened by each call of `endpoints.widgetStream`. */
interface Opened {
  items: WidgetStreamItem[]
  signal?: AbortSignal
  send: (text: string) => void
  close: () => void
}

let opened: Opened[]
let getSpy: MockInstance<typeof endpoints.widgetData>

beforeEach(() => {
  opened = []
  vi.spyOn(endpoints, 'widgetStream').mockImplementation(async (items, signal) => {
    const enc = new TextEncoder()
    let ctrl!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({ start: (c) => void (ctrl = c) })
    // As fetch does: aborting the request breaks its body off.
    signal?.addEventListener('abort', () => ctrl.error(signal.reason))
    opened.push({
      items,
      signal,
      send: (text) => ctrl.enqueue(enc.encode(text)),
      close: () => ctrl.close(),
    })
    return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
  })
  getSpy = vi.spyOn(endpoints, 'widgetData').mockImplementation(async (id) => answer(id, { source_type: 'sql' }))
})

afterEach(() => {
  vi.restoreAllMocks()
})

function event(id: number, body: object): string {
  return `event: widget\ndata: ${JSON.stringify({ widget_id: id, ...body })}\n\n`
}

const done = 'event: done\ndata: {}\n\n'

function client() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function keyOf(w: Widget, p: WidgetDataQuery = params) {
  return widgetQuery(w, p).queryKey
}

async function streamOpened(n = 1) {
  await vi.waitFor(() => expect(opened).toHaveLength(n))
}

describe('loadDashboard', () => {
  it('asks once for every widget and fills each query as its event arrives', async () => {
    const c = client()
    const ws = [widget(1), widget(2)]
    const loading = loadDashboard(
      c,
      ws.map((w) => ({ widget: w, params })),
      {}
    )
    await streamOpened()
    expect(opened[0].items).toEqual([
      { widget_id: 1, ...params },
      { widget_id: 2, ...params },
    ])
    opened[0].send(event(2, { data: answer(2) }))
    await vi.waitFor(() => expect(c.getQueryData(keyOf(ws[1]))).toEqual(answer(2)))
    // The first is still waiting: nothing waits for the whole stream.
    expect(c.getQueryData(keyOf(ws[0]))).toBeUndefined()
    expect(c.getQueryState(keyOf(ws[0]))?.fetchStatus).toBe('fetching')
    opened[0].send(': keepalive\n\n' + event(1, { data: answer(1) }) + done)
    await loading
    expect(c.getQueryData(keyOf(ws[0]))).toEqual(answer(1))
    expect(getSpy).not.toHaveBeenCalled()
  })

  it("fails a refused widget's query as its GET would, and only that one", async () => {
    const c = client()
    const ws = [widget(1), widget(2)]
    const loading = loadDashboard(c, ws.map((w) => ({ widget: w, params })))
    await streamOpened()
    opened[0].send(event(1, { error: { code: 'not_found', message: 'widget 1 is archived' } }) + event(2, { data: answer(2) }) + done)
    await loading
    const err = c.getQueryState(keyOf(ws[0]))?.error
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 404, code: 'not_found', message: 'widget 1 is archived' })
    expect(c.getQueryData(keyOf(ws[1]))).toEqual(answer(2))
  })

  it('loads on its own GET a widget the stream ended without, or answered malformed', async () => {
    const c = client()
    const ws = [widget(1), widget(2), widget(3)]
    const loading = loadDashboard(c, ws.map((w) => ({ widget: w, params })))
    await streamOpened()
    opened[0].send(event(1, { data: answer(1) }) + `event: widget\ndata: {"widget_id":2,"data":nope}\n\n`)
    opened[0].close()
    await loading
    expect(getSpy.mock.calls.map(([id]) => id).sort()).toEqual([2, 3])
    expect(getSpy).toHaveBeenCalledWith(3, params)
    expect(c.getQueryData(keyOf(ws[2]))).toEqual(answer(3, { source_type: 'sql' }))
    expect(c.getQueryData(keyOf(ws[0]))).toEqual(answer(1))
  })

  it('loads every widget on its own GET when the stream fails', async () => {
    vi.mocked(endpoints.widgetStream).mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const c = client()
    const ws = [widget(1), widget(2)]
    await loadDashboard(c, ws.map((w) => ({ widget: w, params })))
    expect(getSpy).toHaveBeenCalledTimes(2)
    expect(c.getQueryData(keyOf(ws[0]))).toEqual(answer(1, { source_type: 'sql' }))
  })

  it('stops on abort: the stream closes, nothing falls back, and a late event lands nowhere', async () => {
    const c = client()
    const ws = [widget(1), widget(2)]
    const controller = new AbortController()
    const loading = loadDashboard(
      c,
      ws.map((w) => ({ widget: w, params })),
      { signal: controller.signal }
    )
    await streamOpened()
    opened[0].send(event(1, { data: answer(1) }))
    await vi.waitFor(() => expect(c.getQueryData(keyOf(ws[0]))).toEqual(answer(1)))
    controller.abort()
    await loading
    expect(opened[0].signal?.aborted).toBe(true)
    // Widget 2 went back to what it showed before: nothing, and no error.
    expect(c.getQueryState(keyOf(ws[1]))).toMatchObject({ data: undefined, error: null, fetchStatus: 'idle' })
    expect(getSpy).not.toHaveBeenCalled()
  })

  it('never puts an answer of an old range under the new one', async () => {
    const c = client()
    const w = widget(1)
    const next = { ...params, from: '2026-10-08', to: '2026-10-14' }
    const first = loadDashboard(c, [{ widget: w, params }, { widget: widget(2), params }])
    await streamOpened()
    const second = loadDashboard(c, [{ widget: w, params: next }, { widget: widget(2), params: next }])
    await streamOpened(2)
    opened[1].send(event(1, { data: answer(1, { cached_at: 'new' }) }) + event(2, { data: answer(2) }) + done)
    await second
    // The old stream answers last: its answer is the old range's, and stays there.
    opened[0].send(event(1, { data: answer(1, { cached_at: 'old' }) }) + event(2, { data: answer(2) }) + done)
    await first
    expect(c.getQueryData(keyOf(w, next))).toMatchObject({ cached_at: 'new' })
    expect(c.getQueryData(keyOf(w))).toMatchObject({ cached_at: 'old' })
  })

  it(`splits more than ${STREAM_MAX} widgets into streams of ${STREAM_MAX}, side by side`, async () => {
    const c = client()
    const ws = Array.from({ length: 2 * STREAM_MAX + 2 }, (_, i) => widget(i + 1))
    const loading = loadDashboard(c, ws.map((w) => ({ widget: w, params })))
    await streamOpened(3)
    expect(opened.map((o) => o.items.length)).toEqual([STREAM_MAX, STREAM_MAX, 2])
    for (const o of opened) {
      o.send(o.items.map((i) => event(i.widget_id, { data: answer(i.widget_id) })).join('') + done)
    }
    await loading
    expect(ws.every((w) => c.getQueryData(keyOf(w)) !== undefined)).toBe(true)
    expect(getSpy).not.toHaveBeenCalled()
  })

  it('sends fresh when asked to', async () => {
    const c = client()
    const loading = loadDashboard(c, [{ widget: widget(1), params }, { widget: widget(2), params }], { fresh: true })
    await streamOpened()
    expect(opened[0].items.every((i) => i.fresh === true)).toBe(true)
    opened[0].send(event(1, { data: answer(1) }) + event(2, { data: answer(2) }) + done)
    await loading
  })

  it('asks the GET route for a lone widget', async () => {
    const c = client()
    await loadDashboard(c, [{ widget: widget(7), params }], { fresh: true })
    expect(opened).toHaveLength(0)
    expect(getSpy).toHaveBeenCalledWith(7, { ...params, fresh: true })
  })

  it('streams a widget once per request: another selection of it goes on its own', async () => {
    const c = client()
    const next = { ...params, project_id: 2 }
    const loading = loadDashboard(c, [
      { widget: widget(1), params },
      { widget: widget(2), params },
      { widget: widget(1), params: next },
    ])
    await streamOpened()
    expect(opened[0].items.map((i) => i.widget_id)).toEqual([1, 2])
    await vi.waitFor(() => expect(getSpy).toHaveBeenCalledWith(1, next))
    opened[0].send(event(1, { data: answer(1) }) + event(2, { data: answer(2) }) + done)
    await loading
  })
})

describe('cards loading together', () => {
  it("make one stream request and no GET: each card's query joins it", async () => {
    const c = client()
    const ws = [widget(1), widget(2), widget(3, { source: { type: 'sql', content: 'SELECT 1' } })]
    const loads = ws.map((w) => c.fetchQuery(widgetQuery(w, params)))
    await streamOpened()
    opened[0].send(ws.map((w) => event(w.widget_id, { data: answer(w.widget_id) })).join('') + done)
    await Promise.all(loads)
    expect(endpoints.widgetStream).toHaveBeenCalledTimes(1)
    expect(getSpy).not.toHaveBeenCalled()
  })

  it('refetch together without fresh when invalidated, as auto-refresh does', async () => {
    const c = client()
    const ws = [widget(1), widget(2)]
    const stops = ws.map((w) => {
      c.setQueryData(keyOf(w), answer(w.widget_id, { cached_at: 'before' }))
      return new QueryObserver(c, widgetQuery(w, params)).subscribe(() => {})
    })
    const refetching = c.invalidateQueries({ queryKey: ['widget'] })
    await streamOpened()
    expect(opened[0].items).toEqual(ws.map((w) => ({ widget_id: w.widget_id, ...params })))
    opened[0].send(event(1, { data: answer(1) }) + event(2, { data: answer(2) }) + done)
    await refetching
    expect(c.getQueryData(keyOf(ws[1]))).toEqual(answer(2))
    expect(getSpy).not.toHaveBeenCalled()
    stops.forEach((stop) => stop())
  })

  it("leave a remote table's own view on its GET", async () => {
    const c = client()
    const table = widget(1, { component: 'table', props: { mode: 'remote' } })
    await Promise.all([
      c.fetchQuery(widgetQuery(table, params, false, { offset: 100 })),
      c.fetchQuery(widgetQuery(widget(2), params)),
    ])
    expect(opened).toHaveLength(0)
    expect(getSpy).toHaveBeenCalledWith(1, { ...params, offset: 100 })
    expect(getSpy).toHaveBeenCalledWith(2, params)
  })
})

describe('refreshDashboard', () => {
  it('refreshes every shown default view through one fresh stream', async () => {
    const c = client()
    const ws = [widget(1), widget(2)]
    // The cards on screen: one active query each, showing an answer.
    const unsubscribe = ws.map((w) => {
      c.setQueryData(keyOf(w), answer(w.widget_id, { cached_at: 'before' }))
      return new QueryObserver(c, widgetQuery(w, params)).subscribe(() => {})
    })
    const refreshing = refreshDashboard(c, ws, () => params)
    await streamOpened()
    expect(opened[0].items).toEqual(ws.map((w) => ({ widget_id: w.widget_id, ...params, fresh: true })))
    opened[0].send(event(1, { data: answer(1) }) + event(2, { data: answer(2) }) + done)
    await refreshing
    expect(c.getQueryData(keyOf(ws[0]))).toEqual(answer(1))
    unsubscribe.forEach((u) => u())
  })
})
