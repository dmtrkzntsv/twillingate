import { useState, type ReactNode } from 'react'
import { Separator } from '@/components/ui/separator'
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import { useMediaQuery } from '@/hooks/use-media-query'
import type { DashboardInfo } from '@/lib/api'
import AppSidebar from './AppSidebar'

interface Props {
  dashboards: DashboardInfo[]
  currentId: number
  children: ReactNode
}

const WIDE = '(min-width: 1024px)'

/**
 * The page around a dashboard: the sidebar, open on wide screens and
 * collapsed to icons below 1024px (it stays togglable either way).
 */
export default function AppShell({ dashboards, currentId, children }: Props) {
  const wide = useMediaQuery(WIDE)
  const [open, setOpen] = useState(wide)
  const [wasWide, setWasWide] = useState(wide)
  if (wide !== wasWide) {
    // Crossing the breakpoint resets the sidebar to that width's default.
    setWasWide(wide)
    setOpen(wide)
  }

  return (
    <SidebarProvider open={open} onOpenChange={setOpen}>
      <AppSidebar dashboards={dashboards} currentId={currentId} />
      <SidebarInset className="min-w-0">{children}</SidebarInset>
    </SidebarProvider>
  )
}

/** The bar across the top of the page: the sidebar toggle, then the report tabs or a label. */
export function TopBar({ children }: { children?: ReactNode }) {
  return (
    <header className="sticky top-0 z-20 flex h-12 shrink-0 items-center gap-2 border-b bg-background/95 px-3 backdrop-blur sm:px-4">
      <SidebarTrigger className="-ml-1" />
      <Separator orientation="vertical" className="mr-1 data-[orientation=vertical]:h-4" />
      {children}
    </header>
  )
}
