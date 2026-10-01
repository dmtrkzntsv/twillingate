import { useEffect, useState } from 'react'
import {
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DndContextProps,
  type DragEndEvent,
  type Modifier,
} from '@dnd-kit/core'
import { arrayMove, sortableKeyboardCoordinates } from '@dnd-kit/sortable'
import { reorder } from '@/lib/arrange'

export interface Reorder {
  /** The ids in the order to show: the dropped order while its move is in flight, else `ids`. */
  order: number[]
  /** True while a dropped order waits for the server's; dragging is off until then. */
  busy: boolean
  /** Props for the `DndContext` around the sortable items. */
  context: Pick<DndContextProps, 'sensors' | 'modifiers' | 'onDragStart' | 'onDragEnd' | 'onDragCancel'>
}

/**
 * Drag to reorder `ids` along one axis (D14). A pointer drag starts after
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
 */
export function useReorder(ids: number[], onMove: (id: number, to: number) => Promise<boolean>, axis: 'x' | 'y'): Reorder {
  const key = ids.join(',')
  const [dropped, setDropped] = useState<{ key: string; order: number[] } | null>(null)
  // New props mean the server's order (or someone else's change) arrived:
  // it replaces whatever was dropped.
  if (dropped && dropped.key !== key) setDropped(null)
  const shown = dropped?.key === key ? dropped.order : null

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates, keyboardCodes })
  )
  useEffect(() => releaseClicks, [])

  const onDragEnd = ({ active, over }: DragEndEvent) => {
    releaseClicksSoon()
    if (!over) return
    const id = Number(active.id)
    const move = reorder(ids, id, Number(over.id))
    if (!move) return
    setDropped({ key, order: arrayMove(ids, ids.indexOf(id), move.to) })
    void onMove(id, move.to).then((ok) => {
      if (!ok) setDropped(null)
    })
  }

  return {
    order: shown ?? ids,
    busy: shown !== null,
    context: {
      sensors,
      modifiers: [axis === 'x' ? alongX : alongY],
      onDragStart: swallowClicks,
      onDragEnd,
      onDragCancel: releaseClicksSoon,
    },
  }
}

const keyboardCodes = { start: ['Space'], cancel: ['Escape'], end: ['Space', 'Enter', 'Tab'] }

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

function swallowClicks() {
  window.addEventListener('click', swallow, true)
}

function releaseClicks() {
  window.removeEventListener('click', swallow, true)
}

function releaseClicksSoon() {
  setTimeout(releaseClicks, 50)
}
