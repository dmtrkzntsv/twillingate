import { useEffect, useState } from 'react'
import {
  closestCorners,
  getFirstCollision,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type Announcements,
  type ClientRect,
  type DndContextProps,
  type DragEndEvent,
  type KeyboardCoordinateGetter,
  type Modifier,
  type UniqueIdentifier,
} from '@dnd-kit/core'
import { arrayMove, sortableKeyboardCoordinates } from '@dnd-kit/sortable'
import { reorder } from '@/lib/arrange'

export interface Reorder {
  /** The ids in the order to show: the dropped order while its move is in flight, else `ids`. */
  order: number[]
  /** True while a dropped order waits for the server's; dragging is off until then. */
  busy: boolean
  /** Props for the `DndContext` around the sortable items. */
  context: Pick<DndContextProps, 'sensors' | 'modifiers' | 'accessibility' | 'onDragStart' | 'onDragEnd' | 'onDragCancel'>
}

/**
 * Drag to reorder `ids` along one axis (D14), or in a grid with `'xy'`. A pointer drag starts after
 * 6px, so a click still navigates or selects. The keyboard picks an item
 * up with Space only, since Enter must keep following a link and
 * selecting a tab; arrows move it, Space, Enter or Tab drops it, Escape
 * cancels (dnd-kit's own screen-reader instructions say the same).
 *
 * On a drop, `onMove(id, to)` gets `to` as `reorder` computes it, and the
 * dropped order shows until the props carry the server's (D15). When
 * `onMove` resolves false (refused, already toasted), the order snaps
 * back at once rather than waiting for a refetch that returns the same
 * list. While a dropped order shows, `busy` turns dragging off, so the
 * next drop's `to` is always against the order the server has.
 * Screen readers hear the items by `titleOf`, not by id.
 */
export function useReorder(
  ids: number[],
  onMove: (id: number, to: number) => Promise<boolean>,
  axis: 'x' | 'y' | 'xy',
  titleOf: (id: number) => string
): Reorder {
  const key = ids.join(',')
  const [dropped, setDropped] = useState<{ key: string; order: number[] } | null>(null)
  // New props mean the server's order (or someone else's change) arrived:
  // it replaces whatever was dropped.
  if (dropped && dropped.key !== key) setDropped(null)
  const shown = dropped?.key === key ? dropped.order : null

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: axis === 'xy' ? gridKeyboardCoordinates : sortableKeyboardCoordinates,
      keyboardCodes,
    })
  )
  useEffect(() => releaseClicks, [])

  const onDragEnd = ({ active, over }: DragEndEvent) => {
    releaseClicksSoon()
    if (!over) return
    const id = Number(active.id)
    const move = reorder(ids, id, Number(over.id))
    if (!move) return
    const mine = { key, order: arrayMove(ids, ids.indexOf(id), move.to) }
    setDropped(mine)
    // A late refusal clears only its own drop, never a newer one.
    void onMove(id, move.to).then((ok) => {
      if (!ok) setDropped((d) => (d === mine ? null : d))
    })
  }

  return {
    order: shown ?? ids,
    busy: shown !== null,
    context: {
      sensors,
      modifiers: axis === 'xy' ? [] : [axis === 'x' ? alongX : alongY],
      accessibility: { announcements: announcements(titleOf, ids) },
      onDragStart: swallowClicks,
      onDragEnd,
      onDragCancel: releaseClicksSoon,
    },
  }
}

/** What a screen reader hears during a drag of `order`: titles and positions rather than ids (D14). */
export function announcements(titleOf: (id: number) => string, order: number[]): Announcements {
  const title = (id: UniqueIdentifier) => titleOf(Number(id))
  const at = (id: UniqueIdentifier) => `position ${order.indexOf(Number(id)) + 1} of ${order.length}`
  return {
    onDragStart: ({ active }) => `Picked up ${title(active.id)}, ${at(active.id)}.`,
    onDragOver: ({ active, over }) =>
      over ? `${title(active.id)} moved to ${at(over.id)}.` : `${title(active.id)} is outside the list.`,
    onDragEnd: ({ active, over }) =>
      over ? `${title(active.id)} dropped at ${at(over.id)}.` : `${title(active.id)} dropped where it was.`,
    onDragCancel: ({ active }) => `Moving ${title(active.id)} was cancelled.`,
  }
}

const keyboardCodes = { start: ['Space'], cancel: ['Escape'], end: ['Space', 'Enter', 'Tab'] }

/**
 * Where an arrow key takes an item in a grid of items of different sizes
 * that stay in place while one moves (WidgetGrid). The next item is the
 * one dnd-kit's sortable getter picks, measured from the item the moving
 * one is over with their top-left corners lined up, as that getter
 * leaves them; the moving item is then centred on it. Lined up by a
 * corner, as that getter does for items that shift out of the way, a
 * wide item's centre fell nearer the item beyond, which the grid's
 * closest-centre collision then took: one arrow skipped an item.
 */
export const gridKeyboardCoordinates: KeyboardCoordinateGetter = (event, { context }) => {
  const ahead = AHEAD[event.code]
  if (!ahead) return undefined
  event.preventDefault()
  const { active, collisionRect, droppableRects, droppableContainers, over } = context
  if (!active || !collisionRect) return undefined
  const at = (over && droppableRects.get(over.id)) ?? collisionRect
  const from: ClientRect = {
    ...collisionRect,
    left: at.left,
    top: at.top,
    right: at.left + collisionRect.width,
    bottom: at.top + collisionRect.height,
  }
  const candidates = droppableContainers.getEnabled().filter((entry) => {
    const rect = droppableRects.get(entry.id)
    return !entry.disabled && rect !== undefined && ahead(from, rect)
  })
  const collisions = closestCorners({
    active,
    collisionRect: from,
    droppableRects,
    droppableContainers: candidates,
    pointerCoordinates: null,
  })
  let id = getFirstCollision(collisions, 'id')
  if (id === over?.id && collisions.length > 1) id = collisions[1].id
  const rect = id == null ? undefined : droppableRects.get(id)
  if (!rect) return undefined
  return {
    x: rect.left + (rect.width - collisionRect.width) / 2,
    y: rect.top + (rect.height - collisionRect.height) / 2,
  }
}

/** Whether `to` lies ahead of `from` for each arrow key, as dnd-kit's sortable getter decides it. */
const AHEAD: Record<string, (from: ClientRect, to: ClientRect) => boolean> = {
  ArrowDown: (from, to) => from.top < to.top,
  ArrowUp: (from, to) => from.top > to.top,
  ArrowLeft: (from, to) => from.left > to.left,
  ArrowRight: (from, to) => from.left < to.left,
}

const alongX: Modifier = ({ transform }) => ({ ...transform, y: 0 })
const alongY: Modifier = ({ transform }) => ({ ...transform, x: 0 })

// The click that ends a pointer drag lands on the dragged item. dnd-kit
// stops its propagation, so React never sees it, but a sidebar entry is an
// <a>: its default would still follow the href with a full page load.
// Swallow clicks from the drag's start until just after it ends (dnd-kit
// waits 50ms the same way).
function swallow(e: MouseEvent) {
  e.preventDefault()
  e.stopPropagation()
}

let releasing: ReturnType<typeof setTimeout> | undefined

function swallowClicks() {
  clearTimeout(releasing)
  window.addEventListener('click', swallow, true)
}

// Also the unmount cleanup, which cancels a pending release so no timer
// outlives the list.
function releaseClicks() {
  clearTimeout(releasing)
  window.removeEventListener('click', swallow, true)
}

function releaseClicksSoon() {
  clearTimeout(releasing)
  releasing = setTimeout(releaseClicks, 50)
}
