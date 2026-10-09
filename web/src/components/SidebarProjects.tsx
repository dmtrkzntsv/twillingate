import { DndContext, closestCenter } from '@dnd-kit/core'
import { SortableContext, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
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
import { useProjectActions } from '@/hooks/use-project-actions'
import { useReorder } from '@/hooks/use-reorder'
import { useStoredState } from '@/hooks/use-stored-state'
import type { Project } from '@/lib/api'
import { afterAt } from '@/lib/arrange'
import { rangeParams } from '@/lib/project-tabs'
import { projectsQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'

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
 * list's order, each a link to the project that drags to a new place in
 * that order, the one the Projects page shows too. Whether the list is open stays
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
  const { move } = useProjectActions()
  const projects = (data?.projects ?? []).filter((p) => !p.archived)
  const ids = projects.map((p) => p.project_id)
  const { order, busy, context } = useReorder(
    ids,
    (id, to) => move(id, afterAt(ids, id, to)),
    'y',
    (id) => projects.find((p) => p.project_id === id)?.name ?? String(id)
  )
  if (error) return <Note>Could not load the projects</Note>
  if (!data) return <SidebarMenuSkeleton className="mx-3.5 h-7" />
  if (projects.length === 0) return <Note>No projects yet</Note>
  const byId = new Map(projects.map((p) => [p.project_id, p]))
  return (
    <DndContext collisionDetection={closestCenter} {...context}>
      <SortableContext items={order} strategy={verticalListSortingStrategy}>
        <SidebarMenuSub aria-label="Projects">
          {order.map((id) => byId.get(id)).filter((p) => p !== undefined).map((p) => {
            // On the project's own pages the link keeps the range, as its tab bar does.
            const here = pathname.startsWith(`/projects/${p.project_id}/`)
            const range = here ? rangeParams(new URLSearchParams(search)).toString() : ''
            return <SortableProject key={p.project_id} project={p} here={here} range={range} disabled={busy} onNavigate={onNavigate} />
          })}
        </SidebarMenuSub>
      </SortableContext>
    </DndContext>
  )
}

interface SortableProjectProps {
  project: Project
  here: boolean
  range: string
  /** True while a dropped order waits for the server's. */
  disabled: boolean
  onNavigate: () => void
}

/**
 * A project's entry, its link the drag handle. The link stays a link: it
 * takes dnd-kit's description attributes and listeners but not its button
 * role or tab index, so Space picks it up (`useReorder`) and Enter still
 * follows it.
 */
function SortableProject({ project, here, range, disabled, onNavigate }: SortableProjectProps) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({
    id: project.project_id,
    disabled,
  })
  return (
    <SidebarMenuSubItem
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn(isDragging && 'z-10')}
    >
      <SidebarMenuSubButton
        asChild
        ref={setActivatorNodeRef}
        isActive={here}
        aria-roledescription={attributes['aria-roledescription']}
        aria-describedby={attributes['aria-describedby']}
        {...listeners}
      >
        <Link to={`/projects/${project.project_id}${range ? `?${range}` : ''}`} onClick={onNavigate} title={project.name}>
          <span>{project.name}</span>
        </Link>
      </SidebarMenuSubButton>
    </SidebarMenuSubItem>
  )
}

function Note({ children }: { children: string }) {
  return <p className="mx-3.5 px-2.5 py-1 text-xs text-sidebar-foreground/55 group-data-[collapsible=icon]:hidden">{children}</p>
}
