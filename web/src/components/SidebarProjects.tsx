import { useQuery } from '@tanstack/react-query'
import { ChevronRightIcon, FolderIcon } from 'lucide-react'
import { Link, useLocation } from 'react-router'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import {
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSkeleton,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
} from '@/components/ui/sidebar'
import { useStoredState } from '@/hooks/use-stored-state'
import type { Project } from '@/lib/api'
import { rangeParams, SETUP_ID, tabPath } from '@/lib/project-tabs'
import { projectsQuery, projectTabsQuery } from '@/lib/queries'

/** Whether the project list under "Projects" is open; absent means open. */
export const LIST_KEY = 'twillingate.sidebar.projects'
/** The projects whose dashboards are listed, by id; absent means none. */
export const OPEN_KEY = 'twillingate.sidebar.open_projects'

interface Props {
  /** Closes the drawer on phones once a link is followed. */
  onNavigate: () => void
  /** The sidebar is down to icons: the list is hidden, so the "Projects" entry stands for any project page. */
  iconOnly: boolean
  /** The entry's class, shared with the other top-level entries. */
  className: string
}

/**
 * "Projects" (a link to the list) and, under it, every live project in the
 * list's order, each opening to its dashboard tabs, so a project's
 * dashboard is one click away. Whether the list is open, and which
 * projects are, stay in localStorage on this device. The projects are asked
 * for only while the list is open, and a project's tabs only while it is.
 * Down to icons, only the "Projects" link shows.
 */
export default function SidebarProjects({ onNavigate, iconOnly, className }: Props) {
  const { pathname } = useLocation()
  const [listStored, setListStored] = useStoredState(LIST_KEY, (v) => (typeof v === 'boolean' ? v : null))
  const listOpen = listStored ?? true
  const [openStored, setOpenStored] = useStoredState(OPEN_KEY, parseIds)
  const openIds = openStored ?? []
  const setProjectOpen = (id: number, open: boolean) => {
    const rest = openIds.filter((x) => x !== id)
    const next = open ? [...rest, id] : rest
    setOpenStored(next.length > 0 ? next : null)
  }
  const onProject = pathname.startsWith('/projects/')

  return (
    <Collapsible asChild open={listOpen} onOpenChange={setListStored}>
      <SidebarMenuItem>
        <SidebarMenuButton
          asChild
          isActive={pathname === '/projects' || (onProject && (!listOpen || iconOnly))}
          tooltip="Projects"
          className={className}
        >
          <Link to="/projects" onClick={onNavigate}>
            <FolderIcon />
            <span>Projects</span>
          </Link>
        </SidebarMenuButton>
        <CollapsibleTrigger asChild>
          <SidebarMenuAction aria-label="Show projects" className="data-[state=open]:rotate-90">
            <ChevronRightIcon />
          </SidebarMenuAction>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <ProjectList openIds={openIds} onOpenChange={setProjectOpen} onNavigate={onNavigate} />
        </CollapsibleContent>
      </SidebarMenuItem>
    </Collapsible>
  )
}

function ProjectList({
  openIds,
  onOpenChange,
  onNavigate,
}: {
  openIds: number[]
  onOpenChange: (id: number, open: boolean) => void
  onNavigate: () => void
}) {
  const { data, error } = useQuery(projectsQuery)
  if (error) return <Note>Could not load the projects</Note>
  if (!data) return <SidebarMenuSkeleton className="mx-3.5 h-7" />
  const projects = (data.projects ?? []).filter((p) => !p.archived)
  if (projects.length === 0) return <Note>No projects yet</Note>
  return (
    <SidebarMenuSub aria-label="Projects">
      {projects.map((p) => (
        <ProjectEntry
          key={p.project_id}
          project={p}
          open={openIds.includes(p.project_id)}
          onOpenChange={(open) => onOpenChange(p.project_id, open)}
          onNavigate={onNavigate}
        />
      ))}
    </SidebarMenuSub>
  )
}

/**
 * One project: its name links to its Setup tab, the chevron opens its
 * dashboard tabs. On the project's own pages the links keep the range, as
 * its tab bar does. The name is active on the project unless the open list
 * already marks the dashboard on screen.
 */
function ProjectEntry({
  project,
  open,
  onOpenChange,
  onNavigate,
}: {
  project: Project
  open: boolean
  onOpenChange: (open: boolean) => void
  onNavigate: () => void
}) {
  const { pathname, search } = useLocation()
  const id = project.project_id
  const here = pathname.startsWith(`/projects/${id}/`)
  const range = here ? rangeParams(new URLSearchParams(search)).toString() : ''
  const shown = here ? Number(/^\/projects\/\d+\/dashboards\/(\d+)/.exec(pathname)?.[1] ?? NaN) : NaN
  // The tab bar's own query; a minute's staleness spares a request per page.
  const tabsQ = useQuery({ ...projectTabsQuery(id), enabled: open, staleTime: 60_000 })
  const tabs = tabsQ.data?.tabs
  const marked = open && !!tabs?.some((t) => t.dashboard_id === shown)

  return (
    <Collapsible asChild open={open} onOpenChange={onOpenChange}>
      <SidebarMenuSubItem>
        <SidebarMenuSubButton asChild isActive={here && !marked} className="pr-7">
          <Link to={tabPath(id, SETUP_ID, range)} onClick={onNavigate} title={project.name}>
            <span>{project.name}</span>
          </Link>
        </SidebarMenuSubButton>
        <CollapsibleTrigger asChild>
          <button
            type="button"
            aria-label={`${project.name} dashboards`}
            className="absolute top-1 right-1 flex size-5 items-center justify-center rounded-md text-sidebar-foreground/70 ring-sidebar-ring outline-hidden transition-transform hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 data-[state=open]:rotate-90 [&>svg]:size-4"
          >
            <ChevronRightIcon />
          </button>
        </CollapsibleTrigger>
        <CollapsibleContent>
          {tabsQ.error ? (
            <Note>Could not load the dashboards</Note>
          ) : !tabs ? (
            <SidebarMenuSkeleton className="mx-3.5 h-7" />
          ) : tabs.length === 0 ? (
            <Note>No dashboards</Note>
          ) : (
            <SidebarMenuSub aria-label={`${project.name} dashboards`}>
              {tabs.map((t) => (
                <SidebarMenuSubItem key={t.dashboard_id}>
                  <SidebarMenuSubButton asChild isActive={t.dashboard_id === shown}>
                    <Link to={tabPath(id, t.dashboard_id, range)} onClick={onNavigate} title={t.title}>
                      <span>{t.title}</span>
                    </Link>
                  </SidebarMenuSubButton>
                </SidebarMenuSubItem>
              ))}
            </SidebarMenuSub>
          )}
        </CollapsibleContent>
      </SidebarMenuSubItem>
    </Collapsible>
  )
}

function Note({ children }: { children: string }) {
  return <p className="mx-3.5 px-2.5 py-1 text-xs text-sidebar-foreground/55 group-data-[collapsible=icon]:hidden">{children}</p>
}

function parseIds(v: unknown): number[] | null {
  return Array.isArray(v) && v.every((x) => Number.isInteger(x)) ? (v as number[]) : null
}
