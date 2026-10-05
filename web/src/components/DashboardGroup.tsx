import type { ReactNode } from 'react'
import { LayersIcon } from 'lucide-react'
import { Link } from 'react-router'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

export interface GroupRow {
  dashboard_id: number
  title: string
  /** The line under the title. */
  detail: string
  /** Greys the title out (a live tab on the Archive page). */
  muted?: boolean
  action?: ReactNode
}

/** A group of one: a single bordered row, titled by its dashboard, linking to it. */
export function LoneDashboard({ row }: { row: GroupRow }) {
  return (
    <li className="flex items-center justify-between gap-3 rounded-lg border p-3">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <h3 className="truncate font-medium">
          <Link to={`/dashboards/${row.dashboard_id}`} className="underline-offset-2 hover:underline">
            {row.title}
          </Link>
        </h3>
        <span className="text-xs text-muted-foreground">{row.detail}</span>
      </div>
      {row.action}
    </li>
  )
}

interface Props {
  /** The group's name: what the sidebar calls it. */
  title: string
  /** The badge beside the title ("3 tabs · 2 archived"). */
  meta: string
  /** What acts on the whole group, at the right of the header. */
  action?: ReactNode
  /** Every tab, in order, each linking to itself. */
  rows: GroupRow[]
}

/**
 * A dashboard group of two or more tabs, shown as one card: a header with
 * the group's name and what acts on all of it, then a row per tab with
 * what acts on that tab alone. The Archive page and the Dashboards gallery
 * both list groups this way.
 */
export function DashboardGroup({ title, meta, action, rows }: Props) {
  return (
    <li aria-label={title} className="flex flex-col rounded-lg border">
      <div className="flex items-center justify-between gap-3 border-b bg-muted/40 p-3">
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <LayersIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <h3 className="truncate font-medium">{title}</h3>
          <Badge variant="outline" className="text-muted-foreground">
            {meta}
          </Badge>
        </div>
        {action}
      </div>
      <ul className="flex flex-col divide-y">
        {rows.map((r) => (
          <li key={r.dashboard_id} className="flex items-center justify-between gap-3 py-2 pr-3 pl-9">
            <div className="flex min-w-0 flex-1 flex-col gap-0.5">
              <Link
                to={`/dashboards/${r.dashboard_id}`}
                className={cn('truncate text-sm underline-offset-2 hover:underline', r.muted ? 'text-muted-foreground' : 'font-medium')}
              >
                {r.title}
              </Link>
              <span className="text-xs text-muted-foreground">{r.detail}</span>
            </div>
            {r.action}
          </li>
        ))}
      </ul>
    </li>
  )
}
