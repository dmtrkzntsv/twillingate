import { DndContext, closestCenter } from '@dnd-kit/core'
import { SortableContext, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { useQuery } from '@tanstack/react-query'
import {
  ArchiveIcon,
  ChartColumnIcon,
  ChevronRightIcon,
  FolderIcon,
  LayoutDashboardIcon,
  LayoutGridIcon,
  LogOutIcon,
  ShapesIcon,
} from 'lucide-react'
import { useState } from 'react'
import { Link, useLocation } from 'react-router'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarRail,
  useSidebar,
} from '@/components/ui/sidebar'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { useReorder } from '@/hooks/use-reorder'
import { liveGroups, moveGroupBody, type Group } from '@/lib/arrange'
import type { DashboardInfo } from '@/lib/api'
import { currentAuthState, logout } from '@/lib/auth'
import { projectsQuery } from '@/lib/queries'
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
 * One entry per dashboard group (tabs D20): system groups first, then
 * "Yours" (the user's), Projects (a link to the list, opening to the
 * active projects), Archive (every archived dashboard, user or
 * system, D17a), and Gallery (the components playground and the
 * templates gallery, D17). Each entry links to its group's first live
 * member and is named by its title; it is active on any live member of
 * the group. "Yours" entries drag to a new order (D14, D15); system
 * entries do not, and since the sortable list holds only user groups,
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
        {system.length > 0 && (
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                {system.map((g) => (
                  <SidebarMenuItem key={g.groupId}>
                    <SidebarMenuButton asChild isActive={isActive(g)} tooltip={g.members[0].title} className={item}>
                      <Link to={`/dashboards/${g.members[0].dashboard_id}`} onClick={close}>
                        <ChartColumnIcon />
                        <span>{g.members[0].title}</span>
                      </Link>
                    </SidebarMenuButton>
                    {!readOnly && <SidebarGroupMenu group={g} userGroups={yours} currentId={currentId} />}
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
        <SidebarGroup>
          <SidebarGroupLabel className="text-sidebar-foreground/60">Yours</SidebarGroupLabel>
          <SidebarGroupContent>
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
                None yet. Ask your agent to make one.
              </p>
            )}
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              <ProjectsGroup pathname={pathname} close={close} />
            </SidebarMenu>
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

const PROJECTS_OPEN = 'twillingate.sidebar.projects'

/** "Projects": a link to the list, opening to the active projects; closed until opened, then remembered. */
function ProjectsGroup({ pathname, close }: { pathname: string; close: () => void }) {
  const { data } = useQuery(projectsQuery)
  const [open, setOpen] = useState(() => localStorage.getItem(PROJECTS_OPEN) === 'open')
  const toggle = (next: boolean) => {
    setOpen(next)
    if (next) localStorage.setItem(PROJECTS_OPEN, 'open')
    else localStorage.removeItem(PROJECTS_OPEN)
  }
  const active = (data?.projects ?? []).filter((p) => !p.archived)
  return (
    <Collapsible open={open} onOpenChange={toggle} asChild>
      <SidebarMenuItem>
        <SidebarMenuButton asChild isActive={pathname === '/projects'} tooltip="Projects" className={item}>
          <Link to="/projects" onClick={close}>
            <FolderIcon />
            <span>Projects</span>
          </Link>
        </SidebarMenuButton>
        {active.length > 0 && (
          <>
            <CollapsibleTrigger asChild>
              <SidebarMenuAction className="data-[state=open]:rotate-90" aria-label={open ? 'Hide projects' : 'Show projects'}>
                <ChevronRightIcon />
              </SidebarMenuAction>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <SidebarMenuSub>
                {active.map((p) => (
                  <SidebarMenuSubItem key={p.project_id}>
                    <SidebarMenuSubButton asChild isActive={pathname === `/projects/${p.project_id}`}>
                      <Link to={`/projects/${p.project_id}`} onClick={close}>
                        <span>{p.name}</span>
                      </Link>
                    </SidebarMenuSubButton>
                  </SidebarMenuSubItem>
                ))}
              </SidebarMenuSub>
            </CollapsibleContent>
          </>
        )}
      </SidebarMenuItem>
    </Collapsible>
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
