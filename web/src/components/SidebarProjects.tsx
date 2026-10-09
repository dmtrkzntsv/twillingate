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
import { rangeParams } from '@/lib/project-tabs'
import { projectsQuery } from '@/lib/queries'

/** Whether the project list under "Projects" is open; absent means open. */
export const LIST_KEY = 'twillingate.sidebar.projects'

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
 * list's order, each a link to the project. Whether the list is open stays
 * in localStorage on this device; the projects are asked for only while it
 * is. Down to icons, only the "Projects" link shows.
 */
export default function SidebarProjects({ onNavigate, iconOnly, className }: Props) {
  const { pathname } = useLocation()
  const [listStored, setListStored] = useStoredState(LIST_KEY, (v) => (typeof v === 'boolean' ? v : null))
  const listOpen = listStored ?? true
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
          <ProjectList onNavigate={onNavigate} />
        </CollapsibleContent>
      </SidebarMenuItem>
    </Collapsible>
  )
}

function ProjectList({ onNavigate }: { onNavigate: () => void }) {
  const { pathname, search } = useLocation()
  const { data, error } = useQuery(projectsQuery)
  if (error) return <Note>Could not load the projects</Note>
  if (!data) return <SidebarMenuSkeleton className="mx-3.5 h-7" />
  const projects = (data.projects ?? []).filter((p) => !p.archived)
  if (projects.length === 0) return <Note>No projects yet</Note>
  return (
    <SidebarMenuSub aria-label="Projects">
      {projects.map((p) => {
        // On the project's own pages the link keeps the range, as its tab bar does.
        const here = pathname.startsWith(`/projects/${p.project_id}/`)
        const range = here ? rangeParams(new URLSearchParams(search)).toString() : ''
        return (
          <SidebarMenuSubItem key={p.project_id}>
            <SidebarMenuSubButton asChild isActive={here}>
              <Link to={`/projects/${p.project_id}${range ? `?${range}` : ''}`} onClick={onNavigate} title={p.name}>
                <span>{p.name}</span>
              </Link>
            </SidebarMenuSubButton>
          </SidebarMenuSubItem>
        )
      })}
    </SidebarMenuSub>
  )
}

function Note({ children }: { children: string }) {
  return <p className="mx-3.5 px-2.5 py-1 text-xs text-sidebar-foreground/55 group-data-[collapsible=icon]:hidden">{children}</p>
}
