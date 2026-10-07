import type { ReactNode } from 'react'
import { useSortable } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { cn } from '@/lib/utils'

interface Props {
  id: number
  /** False in reporting dev, which takes no writes: the card neither drags nor says it is sortable. */
  movable: boolean
  /** True while a dropped order waits for the server's. */
  disabled: boolean
  children: ReactNode
}

/**
 * A project card that drags to a new place on the Projects page. The card
 * keeps its link as the one focus stop: this wrapper takes dnd-kit's
 * description attributes and its pointer and key listeners, never its
 * button role or tab index, so Space on the focused link picks the card
 * up (`useReorder`) and Enter still opens the project.
 */
export default function SortableProjectCard({ id, movable, disabled, children }: Props) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id, disabled: disabled || !movable })
  return (
    <div
      ref={setNodeRef}
      aria-roledescription={movable ? attributes['aria-roledescription'] : undefined}
      aria-describedby={movable ? attributes['aria-describedby'] : undefined}
      {...listeners}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn('grid min-w-0', isDragging && 'relative z-10 opacity-90 shadow-lg')}
    >
      {children}
    </div>
  )
}
