import { useState, type ReactNode } from 'react'
import { Separator } from '@/components/ui/separator'
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import { useMediaQuery } from '@/hooks/use-media-query'
import type { DashboardInfo } from '@/lib/api'
import { appVersion } from '@/lib/version'
import AppSidebar from './AppSidebar'

interface Props {
  dashboards: DashboardInfo[]
  currentId: number
  /** Reporting dev: no menus and no dragging in the sidebar, since it takes no writes. */
  readOnly?: boolean
  children: ReactNode
}

const WIDE = '(min-width: 1024px)'

/**
 * The page around a dashboard: the sidebar, the page, and below it the
 * footer with the running version. The sidebar is open on wide screens and
 * collapsed to icons below 1024px (it stays togglable either way).
 */
export default function AppShell({ dashboards, currentId, readOnly = false, children }: Props) {
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
      <AppSidebar dashboards={dashboards} currentId={currentId} readOnly={readOnly} />
      <SidebarInset className="sky-wash min-w-0">
        {children}
        <Footer />
      </SidebarInset>
    </SidebarProvider>
  )
}

/** The bar across the top of the page: the sidebar toggle, then the group's tabs or a label. */
export function TopBar({ children }: { children?: ReactNode }) {
  return (
    <header className="sticky top-0 z-20 flex h-12 shrink-0 items-center gap-2 border-b border-border/70 bg-background/55 px-3 backdrop-blur-md sm:px-4">
      <SidebarTrigger className="-ml-1" />
      <Separator orientation="vertical" className="mr-1 data-[orientation=vertical]:h-4" />
      {children}
    </header>
  )
}

/** The running version, and a link to the project's site. */
function Footer() {
  return (
    <footer className="mt-auto flex flex-wrap items-center justify-center gap-x-2 px-3 py-4 text-xs text-muted-foreground">
      <span className="break-all">twillingate {appVersion()}</span>
      <span aria-hidden>·</span>
      <a href="https://twillingate.dev" target="_blank" rel="noopener" className="underline-offset-4 hover:text-foreground hover:underline">
        twillingate.dev
      </a>
    </footer>
  )
}
