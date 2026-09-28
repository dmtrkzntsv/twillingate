import { ChartColumnIcon, LayoutDashboardIcon, LogOutIcon } from 'lucide-react'
import { Link } from 'react-router'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
} from '@/components/ui/sidebar'
import type { DashboardInfo } from '@/lib/api'
import { currentAuthState, logout } from '@/lib/auth'
import IcebergLogo from './IcebergLogo'

interface Props {
  /** Every dashboard, in sidebar order: system ones, then the user's. */
  dashboards: DashboardInfo[]
  currentId: number
}

/**
 * Reports (the system dashboards, as one entry) and Yours (the user's
 * live dashboards). Icons only at 640–1023px, a drawer on phones (D37).
 * Log out shows only when the app holds a credential: reporting dev's
 * open mode has none to forget.
 */
export default function AppSidebar({ dashboards, currentId }: Props) {
  const { isMobile, setOpenMobile } = useSidebar()
  const live = dashboards.filter((d) => !d.archived_at)
  const reports = live.filter((d) => d.owner === 'system')
  const yours = live.filter((d) => d.owner === 'user')
  const close = () => {
    if (isMobile) setOpenMobile(false)
  }

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild tooltip="twillingate" className="hover:bg-transparent active:bg-transparent">
              <Link to="/" onClick={close}>
                <span className="size-8 shrink-0 overflow-hidden rounded-[22%] shadow-[0_6px_16px_-6px_rgba(0,0,0,0.7)] ring-1 ring-white/15">
                  <IcebergLogo className="size-full" />
                </span>
                <span className="truncate text-base font-semibold tracking-tight text-[#f8fcff]">twillingate</span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        {reports.length > 0 && (
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    asChild
                    isActive={reports.some((d) => d.dashboard_id === currentId)}
                    tooltip="Reports"
                    className={item}
                  >
                    <Link to={`/dashboards/${reports[0].dashboard_id}`} onClick={close}>
                      <ChartColumnIcon />
                      <span>Reports</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
        <SidebarGroup>
          <SidebarGroupLabel className="text-sidebar-foreground/60">Yours</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {yours.map((d) => (
                <SidebarMenuItem key={d.dashboard_id}>
                  <SidebarMenuButton asChild isActive={d.dashboard_id === currentId} tooltip={d.title} className={item}>
                    <Link to={`/dashboards/${d.dashboard_id}`} onClick={close}>
                      <LayoutDashboardIcon />
                      <span>{d.title}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
            {yours.length === 0 && (
              <p className="px-2 py-1 text-xs text-sidebar-foreground/55 group-data-[collapsible=icon]:hidden">
                None yet. Ask your agent to make one.
              </p>
            )}
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      {currentAuthState().kind !== 'none' && (
        <SidebarFooter>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton tooltip="Log out" onClick={logOut}>
                <LogOutIcon />
                <span>Log out</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
      )}
      <SidebarRail />
    </Sidebar>
  )
}

/** The open entry: its icon in the logo's sky blue. */
const item = 'data-[active=true]:[&>svg]:text-sidebar-primary'

// A full page load rather than an in-app route, so the query cache (the
// last user's dashboards and data) goes with the credentials.
function logOut() {
  logout()
  window.location.assign('/app/login')
}
