import { DndContext, closestCenter } from '@dnd-kit/core'
import { SortableContext, verticalListSortingStrategy } from '@dnd-kit/sortable'
import {
  ArchiveIcon,
  FolderIcon,
  LayoutDashboardIcon,
  LayoutGridIcon,
  LogOutIcon,
  PinIcon,
  ShapesIcon,
} from 'lucide-react'
import { Link, useLocation } from 'react-router'
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
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { useReorder } from '@/hooks/use-reorder'
import { liveGroups, moveGroupBody, type Group } from '@/lib/arrange'
import type { DashboardInfo } from '@/lib/api'
import { currentAuthState, logout } from '@/lib/auth'
import IcebergLogo from './IcebergLogo'
import SidebarGroupMenu from './SidebarGroupMenu'
import SortableGroupItem from './SortableGroupItem'

interface Props {
  /** Every dashboard, in sidebar order: system ones, then the user's. */
  dashboards: DashboardInfo[]
  currentId: number
  /** Reporting dev, which takes no writes: no "…" menus and no dragging. */
  readOnly?: boolean
}

/**
 * Projects (a link to the list) first, then "Dashboards": one entry per
 * dashboard group (tabs D20), the system groups pinned at the top with a
 * pin, then the user's. Then Archive (every archived dashboard, user or
 * system, D17a), and Gallery (the components playground and the
 * templates gallery, D17). Each entry links to its group's first live
 * member and is named by its title; it is active on any live member of
 * the group. The user's entries drag to a new order (D14, D15); pinned
 * ones do not, and since the sortable list holds only user groups,
 * nothing drops above them. Icons only at 640–1023px, a drawer on phones
 * (D37).
 * Log out shows only when the app holds a credential: reporting dev's
 * open mode has none to forget.
 */
export default function AppSidebar({ dashboards, currentId, readOnly = false }: Props) {
  const { isMobile, setOpenMobile } = useSidebar()
  const { pathname } = useLocation()
  const groups = liveGroups(dashboards)
  const system = groups.filter((g) => g.owner === 'system')
  const serverYours = groups.filter((g) => g.owner === 'user')
  const { move } = useDashboardActions()
  const { order, busy, context } = useReorder(
    serverYours.map((g) => g.groupId),
    // `busy` keeps a second drag off until the first's order is in the
    // props, so `to` and `serverYours` agree.
    async (groupId, to) => {
      const body = moveGroupBody(serverYours, groupId, to)
      const group = serverYours.find((g) => g.groupId === groupId)
      return body && group ? move(group.members[0].dashboard_id, body) : false
    },
    'y',
    (id) => serverYours.find((g) => g.groupId === id)?.members[0].title ?? String(id)
  )
  const yours = order.map((id) => serverYours.find((g) => g.groupId === id)!)
  const isActive = (g: Group) => g.members.some((m) => m.dashboard_id === currentId)
  const close = () => {
    if (isMobile) setOpenMobile(false)
  }
  const yourLink = (g: Group) => (
    <Link to={`/dashboards/${g.members[0].dashboard_id}`} onClick={close}>
      <LayoutDashboardIcon />
      <span>{g.members[0].title}</span>
    </Link>
  )

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
        {!readOnly && (
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild isActive={pathname.startsWith('/projects')} tooltip="Projects" className={item}>
                    <Link to="/projects" onClick={close}>
                      <FolderIcon />
                      <span>Projects</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
        <SidebarGroup>
          <SidebarGroupLabel className="text-sidebar-foreground/60">Dashboards</SidebarGroupLabel>
          <SidebarGroupContent className="flex flex-col gap-1">
            {system.length > 0 && (
              <SidebarMenu aria-label="Pinned dashboards">
                {system.map((g) => (
                  <SidebarMenuItem key={g.groupId}>
                    <SidebarMenuButton asChild isActive={isActive(g)} tooltip={`${g.members[0].title} (pinned)`} className={item}>
                      <Link to={`/dashboards/${g.members[0].dashboard_id}`} onClick={close}>
                        <PinIcon />
                        <span>{g.members[0].title}</span>
                      </Link>
                    </SidebarMenuButton>
                    {!readOnly && <SidebarGroupMenu group={g} userGroups={yours} currentId={currentId} />}
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            )}
            {readOnly ? (
              <SidebarMenu>
                {yours.map((g) => (
                  <SidebarMenuItem key={g.groupId}>
                    <SidebarMenuButton asChild isActive={isActive(g)} tooltip={g.members[0].title} className={item}>
                      {yourLink(g)}
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            ) : (
              <DndContext collisionDetection={closestCenter} {...context}>
                <SortableContext items={order} strategy={verticalListSortingStrategy}>
                  <SidebarMenu>
                    {yours.map((g) => (
                      <SortableGroupItem
                        key={g.groupId}
                        groupId={g.groupId}
                        title={g.members[0].title}
                        isActive={isActive(g)}
                        className={item}
                        disabled={busy}
                        link={yourLink(g)}
                        menu={<SidebarGroupMenu group={g} userGroups={yours} currentId={currentId} />}
                      />
                    ))}
                  </SidebarMenu>
                </SortableContext>
              </DndContext>
            )}
            {yours.length === 0 && (
              <p className="px-2 py-1 text-xs text-sidebar-foreground/55 group-data-[collapsible=icon]:hidden">
                {system.length > 0 ? 'None of your own yet. Ask your agent to make one.' : 'None yet. Ask your agent to make one.'}
              </p>
            )}
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton asChild isActive={pathname === '/archive'} tooltip="Archive" className={item}>
                  <Link to="/archive" onClick={close}>
                    <ArchiveIcon />
                    <span>Archive</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupLabel className="text-sidebar-foreground/60">Gallery</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton
                  asChild
                  isActive={pathname.startsWith('/gallery/components')}
                  tooltip="Components"
                  className={item}
                >
                  <Link to="/gallery/components" onClick={close}>
                    <ShapesIcon />
                    <span>Components</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
              <SidebarMenuItem>
                <SidebarMenuButton
                  asChild
                  isActive={pathname.startsWith('/gallery/dashboards')}
                  tooltip="Templates"
                  className={item}
                >
                  <Link to="/gallery/dashboards" onClick={close}>
                    <LayoutGridIcon />
                    <span>Templates</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
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
