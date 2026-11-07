import { useState } from 'react'
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
  /** The project's tabs, in order. */
  tabs: ProjectTab[]
  /** False while the tabs load or after they failed: neither the back button nor Add dashboard yet. */
  tabsLoaded: boolean
  /** Every dashboard, for the picker. */
  dashboards: DashboardInfo[]
  actions: ProjectTabActions
}

/**
 * The title of a project's Settings and Forms pages (project settings
 * tabs): in place of the name and its pencil, "← name", back to the
 * dashboard last opened here, else the first (D3), keeping the range. A
 * project with no dashboard offers "Add dashboard" there instead, the "+"
 * picker. A long name truncates on its one line.
 */
export default function ProjectBackHeader({ projectId, name, tabs, tabsLoaded, dashboards, actions }: Props) {
  const navigate = useNavigate()
  const [url] = useSearchParams()
  const [adding, setAdding] = useState(false)
  const search = rangeParams(url).toString()
  if (!tabsLoaded) return null

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
        <Button variant="outline" size="sm" onClick={() => setAdding(true)}>
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
