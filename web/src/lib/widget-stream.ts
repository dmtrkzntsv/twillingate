import { ApiError, endpoints, type WidgetData, type WidgetDataQuery, type WidgetStreamItem } from './api'
import { readSSE } from './sse'

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

/** The most GETs failed streams' widgets load with at once, every stream together. */
export const FALLBACK_GETS = 6

/** One widget's event on the stream: its GET body, or the refusal the GET route would answer. */
type StreamAnswer =
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
async function readStream(
  items: WidgetStreamItem[],
  signal: AbortSignal,
  onAnswer: (answer: StreamAnswer) => void
): Promise<void> {
  const res = await endpoints.widgetStream(items, signal)
  for await (const e of readSSE(res)) {
    if (e.event === 'done') return
    if (e.event !== 'widget') continue
    const answer = answerOf(e.data)
    if (answer) onAnswer(answer)
  }
}

/** The HTTP status the GET route answers with each refusal code. */
const statusOf: Record<string, number> = { invalid: 400, not_found: 404, conflict: 409, busy: 429, timeout: 504, internal: 500 }

/** A streamed refusal as the error its GET would have thrown, so a card shows it the same way. */
function streamError(error: { code: string; message: string }): ApiError {
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
  const leaves = pending.map((p) => () => leave(p))
  pending.forEach((p, i) => p.signal?.addEventListener('abort', leaves[i], { once: true }))
  let failed: unknown
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
  } catch (error) {
    // The stream failed: what it left unanswered loads on its own, below.
    failed = error
  } finally {
    // The stream is over; a widget left to its own GET below is cancelled by its query.
    pending.forEach((p, i) => p.signal?.removeEventListener('abort', leaves[i]))
  }
  if (controller.signal.aborted) return
  const left = pending.filter((p) => !p.settled)
  // Signed out or not allowed: each GET would be refused the same way, so
  // the widgets fail with the stream's error, for the app's sign-in
  // handling to take over.
  if (failed instanceof ApiError && (failed.status === 401 || failed.status === 403)) {
    for (const p of left) settle(p, () => p.reject(failed))
    return
  }
  await getAll(left)
}

/** Fallback GETs running now, across every stream, and those waiting for one of the FALLBACK_GETS. */
let fallbackRunning = 0
const fallbackWaiting: (() => void)[] = []

/** Runs `load` once fewer than FALLBACK_GETS fallback GETs run, across every stream. */
async function inPool(load: () => Promise<void>): Promise<void> {
  if (fallbackRunning >= FALLBACK_GETS) await new Promise<void>((r) => fallbackWaiting.push(r))
  else fallbackRunning++
  try {
    await load()
  } finally {
    // The slot passes straight to the next waiter, or is freed.
    const next = fallbackWaiting.shift()
    if (next) next()
    else fallbackRunning--
  }
}

/**
 * Loads each of pending on its own GET, through the one pool of
 * FALLBACK_GETS shared by every stream; a widget whose query was cancelled
 * meanwhile is rejected rather than asked for.
 */
async function getAll(pending: Pending[]): Promise<void> {
  await Promise.all(
    pending.map((p) =>
      inPool(async () => {
        if (p.signal?.aborted) return settle(p, () => p.reject(p.signal?.reason))
        await get(p)
      })
    )
  )
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

