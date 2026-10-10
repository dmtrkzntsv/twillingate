import { memo, useRef, useState } from 'react'
import {
  closestCenter,
  DndContext,
  DragOverlay,
  pointerWithin,
  useDndContext,
  type CollisionDetection,
} from '@dnd-kit/core'
import { SortableContext, type SortingStrategy } from '@dnd-kit/sortable'
import { useElementWidth } from '@/hooks/use-element-width'
import { useReorder } from '@/hooks/use-reorder'
import type { WidgetActions } from '@/hooks/use-widget-actions'
import type { Widget, WidgetDataQuery } from '@/lib/api'
import { afterAt } from '@/lib/arrange'
import { FULL_GRID_PX } from '@/lib/grid'
import type { ShareContext } from '@/lib/share'
import { gridClass } from './LayoutGrid'
import WidgetCell from './WidgetCell'
import WidgetFrame from './WidgetFrame'

interface Props {
  widgets: Widget[]
  /** The data query for one widget: what it follows of the page's selection. */
  paramsFor: (widget: Widget) => WidgetDataQuery
  /** Show what is cached, but load nothing (the page is about to change). */
  idle?: boolean
  /** The project and range to share under; absent with no project to share from. */
  share?: ShareContext
  /**
   * The user's own dashboard, while the page may write: each card drags to
   * a new place by its grip and resizes by its bottom-right corner, both
   * saved at once. Absent, the grid only shows.
   */
  arrange?: WidgetActions
}

/**
 * A dashboard's widgets on the grid, in order. The cards do not shift
 * while one is dragged, since they differ in size: the dragged card
 * follows the pointer as an outline, and a bar beside the card under it
 * shows the side it will land on.
 */
function WidgetGrid({ widgets, paramsFor, idle = false, share, arrange }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const gridPx = useElementWidth(ref)
  const ids = widgets.map((w) => w.widget_id)
  const byId = new Map(widgets.map((w) => [w.widget_id, w]))
  const titleOf = (id: number) => label(byId.get(id))
  const onMove = (id: number, to: number) => {
    const widget = byId.get(id)
    return arrange && widget ? arrange.move(widget.dashboard_id, id, afterAt(ids, id, to)) : Promise.resolve(false)
  }
  const { order, busy, context } = useReorder(ids, onMove, 'xy', titleOf)
  // A card redraws only when its props change, but paramsFor makes a new
  // object per call: equal queries are handed over as the same object, so
  // the grid redrawing (a drop, another selection) redraws only the cards
  // whose widget or query changed. A few small objects per selection shown.
  const [queries] = useState(() => new Map<string, WidgetDataQuery>())
  const paramsOf = (widget: Widget) => {
    const params = paramsFor(widget)
    const key = JSON.stringify(params)
    const same = queries.get(key)
    if (same) return same
    queries.set(key, params)
    return params
  }
  // Resizing needs the widths drawn as saved; moving works at every width.
  const resizable = gridPx >= FULL_GRID_PX

  return (
    // The same tree whether or not the cards can be arranged, so turning it
    // on or off (a page freezing as it is left) keeps every card's state.
    <DndContext collisionDetection={underPointer} {...context}>
      <SortableContext items={order} strategy={inPlace}>
        <div ref={ref} data-slot="widget-grid" className={gridClass}>
          {order.map((id) => {
            const widget = byId.get(id)
            if (!widget) return null
            return (
              <WidgetCell
                key={id}
                widget={widget}
                params={paramsOf(widget)}
                idle={idle}
                share={share}
                gridPx={gridPx}
                movable={arrange !== undefined}
                busy={busy}
                onResize={resizable ? arrange?.resize : undefined}
              />
            )
          })}
        </div>
      </SortableContext>
      <Moving titleOf={titleOf} />
    </DndContext>
  )
}

function label(widget: Widget | undefined): string {
  return widget ? (widget.title ?? widget.name) : ''
}

/** The cards stay where they are while one is dragged (see WidgetGrid). */
const inPlace: SortingStrategy = () => null

/**
 * The card under the pointer; with the pointer in a gap, or a drag by the
 * keyboard, the card whose center is closest.
 */
const underPointer: CollisionDetection = (args) => {
  const hits = pointerWithin(args)
  return hits.length > 0 ? hits : closestCenter(args)
}

/** The outline that follows the pointer while a card is dragged, its size the card's. */
function Moving({ titleOf }: { titleOf: (id: number) => string }) {
  const { active } = useDndContext()
  return (
    <DragOverlay>
      {active && (
        <div className="h-full cursor-grabbing opacity-90 shadow-lg">
          <WidgetFrame title={titleOf(Number(active.id))}>
            <div className="h-full rounded-md bg-muted/60" />
          </WidgetFrame>
        </div>
      )}
    </DragOverlay>
  )
}

// The page re-renders on a clock (for "data as of" and refresh); the grid
// only needs to when its widgets or their parameters change.
export default memo(WidgetGrid)
