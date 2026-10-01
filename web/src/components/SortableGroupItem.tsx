import type { ReactNode } from 'react'
import { useSortable } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { SidebarMenuButton, SidebarMenuItem } from '@/components/ui/sidebar'
import { cn } from '@/lib/utils'

interface Props {
  groupId: number
  title: string
  isActive: boolean
  className: string
  /** True while a dropped order waits for the server's. */
  disabled: boolean
  /** The entry's link (its icon and title inside). */
  link: ReactNode
  /** The entry's "…" menu, outside the drag handle so it opens on a click. */
  menu: ReactNode
}

/**
 * A "Yours" sidebar entry that drags to a new place among the user groups
 * (D14). The whole item moves; the link is the handle. The link stays a
 * link: it takes dnd-kit's description attributes and listeners but not
 * its button role, tab index or `aria-disabled` (the sidebar button
 * styles that as greyed out and unclickable).
 */
export default function SortableGroupItem({ groupId, title, isActive, className, disabled, link, menu }: Props) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({
    id: groupId,
    disabled,
  })

  return (
    <SidebarMenuItem
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn(isDragging && 'z-10')}
    >
      <SidebarMenuButton
        asChild
        ref={setActivatorNodeRef}
        isActive={isActive}
        tooltip={title}
        className={className}
        aria-roledescription={attributes['aria-roledescription']}
        aria-describedby={attributes['aria-describedby']}
        {...listeners}
      >
        {link}
      </SidebarMenuButton>
      {menu}
    </SidebarMenuItem>
  )
}
