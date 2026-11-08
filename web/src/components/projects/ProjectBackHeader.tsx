import { useState, type ReactNode } from 'react'
import { ArrowLeftIcon, PlusIcon } from 'lucide-react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { Button } from '@/components/ui/button'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { DashboardInfo, ProjectTab } from '@/lib/api'
import { readLastTab, writeLastTab } from '@/lib/last-tab'
import { landingPath, rangeParams, tabPath } from '@/lib/project-tabs'
import AddTabDialog from './AddTabDialog'

interface Props {
  projectId: number
  name: string
  /** The name with its pencil: shown when there is no dashboard to go back to (yet), so the project can still be renamed. */
  title: ReactNode
  /** The project's tabs, in order. */
  tabs: ProjectTab[]
  /** False while the tabs load or after they failed: the name alone, with neither the back button nor Add dashboard. */
  tabsLoaded: boolean
  /** The tabs could not be read: the name with its pencil, so the project stays renamable. */
  tabsFailed?: boolean
  /** Every dashboard, for the picker. */
  dashboards: DashboardInfo[]
  actions: ProjectTabActions
}

/**
 * The title of a project's Settings and Forms pages (project settings
 * tabs): in place of the name and its pencil, "← name", back to the
 * dashboard last opened here, else the first (D3), keeping the range. A
 * project with no dashboard keeps its name and pencil, the only place left
 * to rename it, with "Add dashboard" beside them, the "+" picker. While
 * the tabs load the name shows alone, and after they failed with its pencil. A long name
 * truncates on its one line.
 */
export default function ProjectBackHeader({ projectId, name, title, tabs, tabsLoaded, tabsFailed = false, dashboards, actions }: Props) {
  const navigate = useNavigate()
  const [url] = useSearchParams()
  const [adding, setAdding] = useState(false)
  const search = rangeParams(url).toString()
  // After the tabs failed: the name with its pencil, the only rename left.
  // While they load: the name, plain; not the pencil, whose open editor
  // would be lost when the back button replaces it.
  if (tabsFailed) return title
  if (!tabsLoaded) return <h1 className="truncate text-xl font-semibold tracking-tight">{name}</h1>

  const add = async (id: number) => {
    const ok = await actions.add(projectId, id)
    if (ok) {
      writeLastTab(projectId, id)
      navigate(tabPath(projectId, id, search))
    }
    return ok
  }

  if (tabs.length === 0) {
    return (
      <>
        {title}
        <Button variant="outline" size="sm" className="shrink-0" onClick={() => setAdding(true)}>
          <PlusIcon />
          Add dashboard
        </Button>
        <AddTabDialog
          open={adding}
          onOpenChange={setAdding}
          dashboards={dashboards}
          tabs={tabs}
          pending={actions.pending}
          onAdd={add}
          onCreate={(title) => actions.create(projectId, title, search)}
        />
      </>
    )
  }
  return (
    <Button asChild variant="ghost" className="-ml-2 h-9 min-w-0 shrink justify-start px-2 has-[>svg]:px-2 text-xl font-semibold tracking-tight [&_svg:not([class*='size-'])]:size-5">
      <Link to={landingPath(projectId, tabs, readLastTab(projectId), search)}>
        <ArrowLeftIcon />
        <span className="sr-only">Back to the dashboards of</span>{' '}
        <span className="truncate">{name}</span>
      </Link>
    </Button>
  )
}
