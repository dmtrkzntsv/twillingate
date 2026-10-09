import { useEffect, useRef, useState, type PointerEvent } from 'react'
import { ChevronRightIcon, LayoutGridIcon, LibraryIcon, ShapesIcon } from 'lucide-react'
import { Link, useLocation } from 'react-router'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { SidebarMenuButton, SidebarMenuItem, useSidebar } from '@/components/ui/sidebar'

interface Props {
  /** Closes the drawer on phones once a link is followed. */
  onNavigate: () => void
  /** The entry's class, shared with the other top-level entries. */
  className: string
}

/** How long the menu waits before closing once the mouse leaves it, so it survives the gap between entry and menu. */
const CLOSE_DELAY = 150

const PAGES = [
  { to: '/gallery/components', label: 'Components', icon: ShapesIcon },
  { to: '/gallery/dashboards', label: 'Dashboards', icon: LayoutGridIcon },
]

/**
 * "Gallery": one entry whose menu, to its right, holds the components
 * playground and the Dashboards gallery of templates (D17). A mouse opens
 * it by hovering the entry and keeps it open while over either; a click,
 * a tap or the keyboard opens it as any menu. On phones the menu opens
 * above the entry, inside the drawer. The entry is active on any gallery
 * page.
 */
export default function SidebarGallery({ onNavigate, className }: Props) {
  const { pathname } = useLocation()
  const { isMobile } = useSidebar()
  const [open, setOpen] = useState(false)
  // Whether the mouse opened the menu, so closing it does not focus the entry.
  const byHover = useRef(false)
  const closing = useRef<ReturnType<typeof setTimeout>>(undefined)
  const trigger = useRef<HTMLButtonElement>(null)
  useEffect(() => () => clearTimeout(closing.current), [])

  const enter = (e: PointerEvent) => {
    if (e.pointerType !== 'mouse') return
    clearTimeout(closing.current)
    if (!open) byHover.current = true
    setOpen(true)
  }
  const leave = (e: PointerEvent) => {
    if (e.pointerType !== 'mouse') return
    clearTimeout(closing.current)
    closing.current = setTimeout(() => setOpen(false), CLOSE_DELAY)
  }
  const change = (next: boolean) => {
    clearTimeout(closing.current)
    if (next) byHover.current = false
    setOpen(next)
  }

  return (
    <SidebarMenuItem>
      <DropdownMenu open={open} onOpenChange={change} modal={false}>
        <DropdownMenuTrigger asChild>
          <SidebarMenuButton
            ref={trigger}
            isActive={pathname.startsWith('/gallery')}
            className={className}
            onPointerEnter={enter}
            onPointerLeave={leave}
            onPointerDown={(e) => {
              // A mouse already opened it on the way in: a click keeps it
              // open rather than toggling it shut (see onPointerDownOutside).
              if (e.pointerType === 'mouse' && open) e.preventDefault()
            }}
          >
            <LibraryIcon />
            <span>Gallery</span>
            <ChevronRightIcon className="ml-auto group-data-[collapsible=icon]:hidden" />
          </SidebarMenuButton>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          side={isMobile ? 'top' : 'right'}
          align={isMobile ? 'start' : 'end'}
          className="min-w-44"
          onPointerEnter={enter}
          onPointerLeave={leave}
          // A press on the entry is the entry's to handle: Radix would
          // otherwise close the menu as pressed outside it.
          onPointerDownOutside={(e) => {
            if (trigger.current?.contains(e.target as Node)) e.preventDefault()
          }}
          onCloseAutoFocus={(e) => {
            if (byHover.current) e.preventDefault()
          }}
        >
          <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">Gallery</DropdownMenuLabel>
          {PAGES.map(({ to, label, icon: Icon }) => {
            const here = pathname.startsWith(to)
            return (
              <DropdownMenuItem key={to} asChild className={here ? 'font-medium' : undefined}>
                <Link to={to} onClick={onNavigate} aria-current={here ? 'page' : undefined}>
                  <Icon />
                  {label}
                </Link>
              </DropdownMenuItem>
            )
          })}
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuItem>
  )
}
