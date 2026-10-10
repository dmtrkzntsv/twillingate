import { useEffect, useId, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { useSortable } from '@dnd-kit/sortable'
import { GripVerticalIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { WidgetSize } from '@/hooks/use-widget-actions'
import type { Widget, WidgetDataQuery } from '@/lib/api'
import { COLUMNS, GAP_PX, ROW_PX, span } from '@/lib/grid'
import type { ShareContext } from '@/lib/share'
import { cn } from '@/lib/utils'
import { cellStyle } from './LayoutGrid'
import WidgetCard from './WidgetCard'

interface Props {
  widget: Widget
  params: WidgetDataQuery
  idle: boolean
  share?: ShareContext
  /** The grid's width: it sets the spans and how far a column is. */
  gridPx: number
  /** The card drags to a new place by its grip. */
  movable: boolean
  /** A dropped order waits for the server's: the grip stays, but nothing drags until it is back. */
  busy: boolean
  /** The card resizes by its corner; absent, it has no corner. */
  onResize?: (dashboardId: number, id: number, size: WidgetSize) => Promise<boolean>
}

/** The most rows and columns a widget spans (validate.go's checkSize). */
const MAX_ROWS = 12

/** How long the arrow keys' last change waits before it is saved. */
const KEY_SAVE_MS = 600

/**
 * One widget's place on the grid. On the user's own dashboard the card
 * drags by its grip (Space picks it up from the keyboard, as in every
 * sortable list here) and resizes by its bottom-right corner, a column
 * and a row at a time; the arrow keys resize it from the corner too. The
 * new size shows as it changes and is saved when the pointer lets go (or
 * the keys pause), then stays until the server's comes back, or snaps
 * back if it is refused.
 */
export default function WidgetCell({ widget, params, idle, share, gridPx, movable, busy, onResize }: Props) {
  const id = widget.widget_id
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, isDragging, isOver, activeIndex, index } = useSortable({
    id,
    disabled: !movable || busy,
  })
  const label = widget.title ?? widget.name

  const stored = { width: widget.width, height: widget.height }
  const storedKey = `${stored.width}x${stored.height}`
  // The size being chosen (a pointer down on the corner, or keys pressed).
  const [draft, setDraft] = useState<WidgetSize | null>(null)
  // The last size sent, kept over the stored one until the server's has
  // reached the props. Its save resolving is not enough: the hook resolves
  // after the refetch, but the cache hands the new widgets to the page a
  // tick later, and the old size would show for a frame in between. So a
  // save that succeeded is only marked `settled`, and dropped in render
  // once the stored size is the one asked for or has moved on from what it
  // was at that moment. Dropping it when the stored size merely changed
  // would let an older save's refetch show its size on the way to this
  // one, so it is the newest save's own settling that counts. If the
  // refetch itself fails, the asked size stays until a later one brings
  // the server's, which is what the save just made it.
  const [saving, setSaving] = useState<{ size: WidgetSize; token: object; settled: boolean; settledAt?: string } | null>(null)
  if (saving?.settled) {
    if (saving.settledAt === undefined) {
      const arrived = stored.width === saving.size.width && stored.height === saving.size.height
      setSaving(arrived ? null : { ...saving, settledAt: storedKey })
    } else if (saving.settledAt !== storedKey) {
      setSaving(null)
    }
  }
  const shown = saving?.size ?? stored
  const size = draft ?? shown

  // Against the size shown, not the stored one: a resize back to the
  // stored size while another is on its way is a change too, and must be
  // sent, or the one on its way would win.
  const save = (next: WidgetSize) => {
    if (!onResize || (next.width === shown.width && next.height === shown.height)) return
    const token = {}
    setSaving({ size: next, token, settled: false })
    // A save settling touches only its own entry, never a newer one's: a
    // refusal snaps back to the stored size at once, a success waits for
    // the server's to arrive (above).
    void onResize(widget.dashboard_id, id, next).then((ok) =>
      setSaving((s) => (s?.token !== token ? s : ok ? { ...s, settled: true } : null))
    )
  }

  const resizer = useResizer(size, gridPx, onResize !== undefined, setDraft, save)
  const describedBy = useId()
  const target = isOver && !isDragging && activeIndex >= 0
  const columns = span(size.width, gridPx)

  return (
    <div
      ref={setNodeRef}
      data-slot="widget-cell"
      data-span={columns}
      className={cn('relative min-w-0 rounded-xl', isDragging && 'opacity-40', draft && 'ring-2 ring-primary/50')}
      style={cellStyle(columns, size.height)}
    >
      <WidgetCard
        widget={widget}
        params={params}
        idle={idle}
        share={share}
        grip={
          movable && (
            <span className="hover-reveal" data-open={isDragging || undefined}>
              <Button
                ref={setActivatorNodeRef}
                variant="ghost"
                size="icon"
                className="size-7 cursor-grab touch-none active:cursor-grabbing"
                {...attributes}
                {...listeners}
                aria-label={`Move ${label}`}
              >
                <GripVerticalIcon />
              </Button>
            </span>
          )
        }
        corner={
          onResize && (
            <>
              {/* Within the card's 14px padding, at its very corner: any
                  larger, it would cover the body's last pixels, a table's
                  next-page button or the scrollbars' corner. */}
              <button
                type="button"
                aria-label={`Resize ${label}`}
                aria-describedby={describedBy}
                data-open={draft ? true : undefined}
                className="hover-reveal absolute right-0 bottom-0 flex size-3.5 cursor-nwse-resize touch-none items-center justify-center rounded-sm text-muted-foreground hover:text-foreground"
                {...resizer}
              >
                <svg viewBox="0 0 10 10" className="size-2.5" aria-hidden>
                  <path d="M9 2 2 9M9 5.5 5.5 9" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
                </svg>
              </button>
              <span id={describedBy} className="sr-only">
                {`${size.width} of ${COLUMNS} columns wide, ${size.height} rows tall. Drag, or use the arrow keys, to resize.`}
              </span>
            </>
          )
        }
      />
      {onResize && (
        // Beside the card, not in it, so the card's re-renders leave it
        // alone; it exists before its text does, or nothing is announced.
        <span role="status" aria-live="polite" className="sr-only">
          {draft && `${label}: ${draft.width} of ${COLUMNS} columns wide, ${draft.height} rows tall`}
        </span>
      )}
      {draft && (
        <span className="pointer-events-none absolute right-7 bottom-1.5 rounded bg-foreground px-1.5 py-0.5 text-xs text-background tabular-nums">
          {size.width} × {size.height}
        </span>
      )}
      {target && (
        // In the gap on the side the dragged card lands: after this one when
        // it comes from before it, else before.
        <div
          aria-hidden
          className={cn(
            'pointer-events-none absolute inset-y-0 w-1 rounded-full bg-primary',
            activeIndex < index ? '-right-2' : '-left-2'
          )}
        />
      )}
    </div>
  )
}

/**
 * The corner's pointer and key handlers. A pointer drag changes the size
 * by the columns and rows it has crossed (rounded, so half a column is
 * enough) and saves it on release; a cancelled pointer drops it. Each
 * arrow key changes the width (left, right) or the height (up, down) by
 * one, saved once the keys pause or the corner loses focus; Escape drops
 * what is not saved yet. A corner that goes away (`enabled` false) drops
 * what it was choosing.
 */
function useResizer(
  size: WidgetSize,
  gridPx: number,
  enabled: boolean,
  setDraft: (size: WidgetSize | null) => void,
  save: (size: WidgetSize) => void
) {
  const start = useRef<{ x: number; y: number; size: WidgetSize } | null>(null)
  const keyed = useRef<WidgetSize | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  // The corner can go while it is held (the grid narrowing below the full
  // width): no pointer will let go of it then, so the size it was showing
  // would stay on the card for good.
  useEffect(() => {
    if (enabled) return
    clearTimeout(timer.current)
    start.current = null
    keyed.current = null
    setDraft(null)
  }, [enabled, setDraft])

  // A column is its share of the grid less the gaps, plus one gap.
  const columnPx = (gridPx + GAP_PX) / COLUMNS
  const rowPx = ROW_PX + GAP_PX
  const fit = (from: WidgetSize, dx: number, dy: number): WidgetSize => ({
    width: clamp(from.width + dx, 1, COLUMNS),
    height: clamp(from.height + dy, 1, MAX_ROWS),
  })
  const dragged = (e: PointerEvent) => {
    const s = start.current!
    return fit(s.size, Math.round((e.clientX - s.x) / columnPx), Math.round((e.clientY - s.y) / rowPx))
  }
  const flushKeys = () => {
    clearTimeout(timer.current)
    const next = keyed.current
    keyed.current = null
    setDraft(null)
    if (next) save(next)
  }

  return {
    onPointerDown: (e: PointerEvent<HTMLButtonElement>) => {
      if (e.button !== 0) return
      e.preventDefault()
      e.currentTarget.setPointerCapture?.(e.pointerId)
      start.current = { x: e.clientX, y: e.clientY, size }
      setDraft(size)
    },
    onPointerMove: (e: PointerEvent<HTMLButtonElement>) => {
      if (start.current) setDraft(dragged(e))
    },
    onPointerUp: (e: PointerEvent<HTMLButtonElement>) => {
      if (!start.current) return
      const next = dragged(e)
      start.current = null
      setDraft(null)
      save(next)
    },
    onPointerCancel: () => {
      start.current = null
      setDraft(null)
    },
    onKeyDown: (e: KeyboardEvent<HTMLButtonElement>) => {
      const step = ARROWS[e.key]
      if (e.key === 'Escape' && keyed.current) {
        e.preventDefault()
        clearTimeout(timer.current)
        keyed.current = null
        setDraft(null)
        return
      }
      if (!step) return
      e.preventDefault()
      keyed.current = fit(keyed.current ?? size, step[0], step[1])
      setDraft(keyed.current)
      clearTimeout(timer.current)
      timer.current = setTimeout(flushKeys, KEY_SAVE_MS)
    },
    onBlur: () => {
      if (keyed.current) flushKeys()
    },
  }
}

const ARROWS: Record<string, [number, number]> = {
  ArrowLeft: [-1, 0],
  ArrowRight: [1, 0],
  ArrowUp: [0, -1],
  ArrowDown: [0, 1],
}

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n))
}
