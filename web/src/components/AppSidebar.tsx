import { ChartColumnIcon, LayoutDashboardIcon } from 'lucide-react'
import { Link } from 'react-router'
import {
  Sidebar,
  SidebarContent,
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

interface Props {
  /** Every dashboard, in sidebar order: system ones, then the user's. */
  dashboards: DashboardInfo[]
  currentId: number
}

/**
 * Reports (the system dashboards, as one entry) and Yours (the user's
 * live dashboards). Icons only at 640–1023px, a drawer on phones (D37).
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
            <SidebarMenuButton size="lg" asChild tooltip="twillingate">
              <Link to="/" onClick={close}>
                <span className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
                  <ChartColumnIcon className="size-4" />
                </span>
                <span className="truncate font-semibold">twillingate</span>
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
          <SidebarGroupLabel>Yours</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {yours.map((d) => (
                <SidebarMenuItem key={d.dashboard_id}>
                  <SidebarMenuButton asChild isActive={d.dashboard_id === currentId} tooltip={d.title}>
                    <Link to={`/dashboards/${d.dashboard_id}`} onClick={close}>
                      <LayoutDashboardIcon />
                      <span>{d.title}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
            {yours.length === 0 && (
              <p className="px-2 py-1 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
                None yet — ask your agent to make one.
              </p>
            )}
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarRail />
    </Sidebar>
  )
}
