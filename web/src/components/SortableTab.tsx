import { useRef, type KeyboardEvent, type MouseEvent } from 'react'
import { useSortable } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

interface Props {
  id: number
  title: string
  className: string
  /** True while a dropped order waits for the server's. */
  disabled: boolean
  onSelect: (id: number) => void
}

/**
 * A user tab that drags to a new place in its group (D14). It stays a
 * Radix tab: role, roving tab stop and arrow-key focus are Radix's, and
 * it takes only dnd-kit's description attributes and its pointer and key
 * listeners, never its button role or tab index.
 *
 * Radix selects a tab on mouse down, which would open the tab a drag
 * starts on and freeze the page under the drag; here the mouse down is
 * cancelled and a click selects instead, which a drag's closing click
 * never reaches. Enter selects through Radix as before; Space picks the
 * tab up (`useReorder`). The click a key press sends after it is skipped,
 * so a keyboard selection does not select twice.
 */
export default function SortableTab({ id, title, className, disabled, onSelect }: Props) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id, disabled })
  const keyed = useRef(false)

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === 'Enter' || e.key === ' ') keyed.current = true
    listeners?.onKeyDown?.(e)
    // While this tab is being dragged the arrows move it, not the focus,
    // and Enter drops it rather than selecting: keep Radix out.
    if (isDragging) e.preventDefault()
  }
  const onClick = (e: MouseEvent<HTMLButtonElement>) => {
    if (keyed.current) {
      keyed.current = false
      return
    }
    e.currentTarget.focus()
    onSelect(id)
  }

  return (
    <TabsTrigger
      ref={setNodeRef}
      value={String(id)}
      aria-roledescription={attributes['aria-roledescription']}
      aria-describedby={attributes['aria-describedby']}
      onPointerDown={(e) => {
        keyed.current = false
        listeners?.onPointerDown?.(e)
      }}
      onMouseDown={(e) => e.preventDefault()}
      onKeyDown={onKeyDown}
      onClick={onClick}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn(className, isDragging && 'z-10')}
    >
      {title}
    </TabsTrigger>
  )
}
