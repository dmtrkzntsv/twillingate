import type { QueryClient } from '@tanstack/react-query'
import { ApiError, endpoints, type Widget, type WidgetData, type WidgetDataQuery, type WidgetStreamItem } from './api'
// widget-query asks loadWidget for each answer; each module calls the other
// only from inside functions, so the cycle is safe.
import { refreshWidget, shownViews, widgetQuery } from './widget-query'

/**
 * A dashboard's widgets load through one streamed request,
 * `POST /api/widgets/stream`, rather than one GET per card.
 *
 * Every widget query (widget-query.ts) asks `loadWidget` for its answer.
 * The requests made in the same turn of the event loop, which is every card
 * of a grid mounting or switching range together, a refresh of them all, or
 * the stale ones refetching on focus, go out as one stream of up to
 * `STREAM_MAX` widgets; a lone request goes to the GET route, as a newly
 * added widget does. Each answer settles its own query as its event
 * arrives, so a card leaves its skeleton then and not when the stream ends.
 * The answer settles the promise of the exact query that asked, so an event
 * of a range left behind can never land under the new range's key.
 */

/** The most widgets one stream request may carry (the route refuses more). */
export const STREAM_MAX = 64

/** One Server-Sent Event: its type (`message` when unnamed) and its data lines joined by newlines. */
export interface SSEEvent {
  event: string
  data: string
}

/**
 * Parses a Server-Sent Events body as it arrives, chunk by chunk: a line or
 * an event split across chunks waits for the rest, lines end in LF, CRLF or
 * CR, comments (`: keepalive`) and unknown fields are skipped, and an event
 * with no data line is not dispatched (as EventSource does).
 */
export class SSEParser {
  private buffer = ''
  private event = ''
  private data: string[] = []

  /** Feeds one chunk and returns the events it completes. */
  push(chunk: string): SSEEvent[] {
    this.buffer += chunk
    const out: SSEEvent[] = []
    let start = 0
    for (let i = 0; i < this.buffer.length; i++) {
      const c = this.buffer[i]
      if (c !== '\n' && c !== '\r') continue
      // A CR last in the chunk may be the first half of a CRLF: wait for the next one.
      if (c === '\r' && i === this.buffer.length - 1) break
      this.line(this.buffer.slice(start, i), out)
      if (c === '\r' && this.buffer[i + 1] === '\n') i++
      start = i + 1
    }
    this.buffer = this.buffer.slice(start)
    return out
  }

  /** The body has ended: a last line without its end is read, but an event without its blank line is dropped. */
  end(): SSEEvent[] {
    const out: SSEEvent[] = []
    if (this.buffer.length > 0) this.line(this.buffer.replace(/\r$/, ''), out)
    this.buffer = ''
    this.event = ''
    this.data = []
    return out
  }

  private line(line: string, out: SSEEvent[]) {
    if (line === '') {
      if (this.data.length > 0) out.push({ event: this.event || 'message', data: this.data.join('\n') })
      this.event = ''
      this.data = []
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon < 0 ? line : line.slice(0, colon)
    let value = colon < 0 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') this.event = value
    else if (field === 'data') this.data.push(value)
  }
}

/** One widget's event on the stream: its GET body, or the refusal the GET route would answer. */
export type StreamAnswer =
  | { widget_id: number; data: WidgetData }
  | { widget_id: number; error: { code: string; message: string } }

/** A `widget` event's data, or undefined when it is not one the route sends. */
function answerOf(data: string): StreamAnswer | undefined {
  try {
    const v = JSON.parse(data) as Record<string, unknown>
    if (typeof v?.widget_id !== 'number') return undefined
    if (v.data !== undefined && v.data !== null && typeof v.data === 'object') return v as StreamAnswer
    const err = v.error as { code?: unknown; message?: unknown } | undefined
    if (err && typeof err.code === 'string' && typeof err.message === 'string') return v as StreamAnswer
  } catch {
    // A malformed event is skipped; its widget falls back to its own GET.
  }
  return undefined
}

/**
 * Reads one stream request through, calling `onAnswer` per widget event as
 * it arrives. It resolves when `done` arrives or the body ends, and throws
 * when the request fails or the body breaks off.
 */
export async function readStream(
  items: WidgetStreamItem[],
  signal: AbortSignal,
  onAnswer: (answer: StreamAnswer) => void
): Promise<void> {
  const res = await endpoints.widgetStream(items, signal)
  if (!res.body) throw new Error('the widget stream has no body')
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  const parser = new SSEParser()
  const dispatch = (events: SSEEvent[]) => {
    for (const e of events) {
      if (e.event === 'done') return true
      if (e.event !== 'widget') continue
      const answer = answerOf(e.data)
      if (answer) onAnswer(answer)
    }
    return false
  }
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) {
        dispatch(parser.push(decoder.decode()))
        dispatch(parser.end())
        return
      }
      if (dispatch(parser.push(decoder.decode(value, { stream: true })))) return
    }
  } finally {
    // Done read or not, the body is let go (a no-op once it has ended).
    void reader.cancel().catch(() => {})
  }
}

/** The HTTP status the GET route answers with each refusal code. */
const statusOf: Record<string, number> = { invalid: 400, not_found: 404, conflict: 409, internal: 500 }

/** A streamed refusal as the error its GET would have thrown, so a card shows it the same way. */
export function streamError(error: { code: string; message: string }): ApiError {
  return new ApiError(statusOf[error.code] ?? 500, error.message, error.code)
}

interface Pending {
  item: WidgetStreamItem
  signal?: AbortSignal
  settled: boolean
  resolve: (data: WidgetData) => void
  reject: (error: unknown) => void
}

let queue: Pending[] = []
let scheduled = false

/**
 * One widget's answer, through the stream unless it is the only widget
 * asked for in this turn (see the top of this file). `signal` is the
 * query's: once it aborts, the answer is not waited for, and a stream whose
 * every widget has gone is closed, which stops its queries on the server.
 */
export function loadWidget(item: WidgetStreamItem, signal?: AbortSignal): Promise<WidgetData> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(signal.reason)
    queue.push({ item, signal, settled: false, resolve, reject })
    if (!scheduled) {
      scheduled = true
      setTimeout(flush, 0)
    }
  })
}

function flush() {
  const batch = queue.filter((p) => !p.signal?.aborted)
  for (const p of queue) if (p.signal?.aborted) p.reject(p.signal.reason)
  queue = []
  scheduled = false
  if (batch.length === 1) return get(batch[0])
  // A stream names each answer by widget only, so each stream carries a
  // widget once; another selection of the same widget goes into the next one.
  const streams: Pending[][] = []
  for (const p of batch) {
    const open = streams.find((s) => s.length < STREAM_MAX && !s.some((o) => o.item.widget_id === p.item.widget_id))
    if (open) open.push(p)
    else streams.push([p])
  }
  for (const s of streams) void (s.length === 1 ? get(s[0]) : stream(s))
}

/** A widget on its own GET: the lone one asked for, or one its stream left unanswered. */
function get(p: Pending): Promise<void> {
  const { widget_id, fresh, ...params } = p.item
  return endpoints.widgetData(widget_id, { ...params, ...(fresh ? { fresh } : {}) }).then(
    (data) => settle(p, () => p.resolve(data)),
    (error: unknown) => settle(p, () => p.reject(error))
  )
}

function settle(p: Pending, how: () => void) {
  if (p.settled) return
  p.settled = true
  how()
}

async function stream(pending: Pending[]): Promise<void> {
  const byId = new Map(pending.map((p) => [p.item.widget_id, p]))
  const controller = new AbortController()
  // A widget whose query is cancelled stops being waited for; the stream
  // closes once none is left.
  const leave = (p: Pending) => {
    settle(p, () => p.reject(p.signal?.reason))
    if (pending.every((o) => o.settled)) controller.abort()
  }
  for (const p of pending) p.signal?.addEventListener('abort', () => leave(p), { once: true })
  try {
    await readStream(
      pending.map((p) => p.item),
      controller.signal,
      (answer) => {
        const p = byId.get(answer.widget_id)
        if (!p) return
        if ('data' in answer) settle(p, () => p.resolve(answer.data))
        else settle(p, () => p.reject(streamError(answer.error)))
      }
    )
  } catch {
    // The stream failed: what it left unanswered loads on its own, below.
  }
  if (controller.signal.aborted) return
  for (const p of pending) if (!p.settled) void get(p)
}

/** What a dashboard reloads: each widget with the query its card shows. */
export interface DashboardItem {
  widget: Widget
  params: WidgetDataQuery
}

/**
 * Loads a dashboard's widgets into their queries through one stream (one
 * per 64 widgets): each answer, or refusal, lands in the query of its card
 * under this selection (`widgetQuery`'s key, default view), as it arrives.
 * `fresh` asks the server for answers at most REPORTING_REFRESH_MAX_AGE
 * old, as Refresh all and auto-refresh do. Aborting `signal` (the page left,
 * or its project or range changed) cancels the queries still waiting and
 * closes the stream; they go back to what they showed before.
 */
export async function loadDashboard(
  client: QueryClient,
  items: DashboardItem[],
  { fresh = false, signal }: { fresh?: boolean; signal?: AbortSignal } = {}
): Promise<PromiseSettledResult<WidgetData>[]> {
  if (signal?.aborted) return []
  const keys = items.map(({ widget, params }) => widgetQuery(widget, params).queryKey)
  const cancel = () => {
    for (const queryKey of keys) void client.cancelQueries({ queryKey, exact: true })
  }
  signal?.addEventListener('abort', cancel, { once: true })
  try {
    return await Promise.allSettled(
      items.map(({ widget, params }, i) =>
        client.fetchQuery({
          queryKey: keys[i],
          queryFn: ({ signal: own }) =>
            loadWidget({ ...streamParams(params), widget_id: widget.widget_id, ...(fresh ? { fresh } : {}) }, own),
          staleTime: 0,
        })
      )
    )
  } finally {
    signal?.removeEventListener('abort', cancel)
  }
}

/** The part of a widget's query a stream takes: its selection, never a remote table's view. */
export function streamParams(params: WidgetDataQuery): Omit<WidgetStreamItem, 'widget_id'> {
  // Only what is set, so a widget's own GET asks for exactly what its card would.
  const { project_id, from, to, fresh } = params
  return {
    ...(project_id !== undefined ? { project_id } : {}),
    ...(from !== undefined ? { from } : {}),
    ...(to !== undefined ? { to } : {}),
    ...(fresh ? { fresh } : {}),
  }
}

/**
 * Refreshes what the cards of these widgets show, with `fresh`: every
 * default view through one stream (`loadDashboard`), a remote table's own
 * page, filter or sort on its own GET, as its card's refresh does.
 */
export function refreshDashboard(
  client: QueryClient,
  widgets: Widget[],
  paramsFor: (widget: Widget) => WidgetDataQuery,
  signal?: AbortSignal
): Promise<unknown> {
  const streamed: DashboardItem[] = []
  const alone: Promise<unknown>[] = []
  for (const widget of widgets) {
    const params = paramsFor(widget)
    for (const view of shownViews(client, widget, params)) {
      if (view.filters === undefined && view.sort === undefined && view.offset === undefined) streamed.push({ widget, params })
      else alone.push(refreshWidget(client, widget, params, view))
    }
  }
  return Promise.allSettled([loadDashboard(client, streamed, { fresh: true, signal }), ...alone])
}
