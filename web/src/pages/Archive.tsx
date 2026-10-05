import { useQuery } from '@tanstack/react-query'
import { LayersIcon } from 'lucide-react'
import { Link } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardInfo } from '@/lib/api'
import { purgeDate } from '@/lib/arrange'
import { dashboardsQuery } from '@/lib/queries'
import { formatPurgeDate } from '@/lib/time'

interface Group {
  owner: DashboardInfo['owner']
  groupId: number
  /** Every member, live and archived, in list order. */
  members: DashboardInfo[]
}

/**
 * Every dashboard grouped by owner and `group_id`, in list order, keeping
 * only the groups with an archived member (D17a).
 */
function archivedGroups(dashboards: DashboardInfo[]): Group[] {
  const groups = new Map<string, Group>()
  for (const d of dashboards) {
    const key = `${d.owner}:${d.group_id}`
    const g = groups.get(key)
    if (g) g.members.push(d)
    else groups.set(key, { owner: d.owner, groupId: d.group_id, members: [d] })
  }
  return [...groups.values()].filter((g) => g.members.some((m) => m.archived_at))
}

/** What the sidebar names a group by: its first live member, else its first member. */
function groupTitle(g: Group): DashboardInfo {
  return g.members.find((m) => !m.archived_at) ?? g.members[0]
}

/**
 * `/archive`: every group with a dashboard out of the sidebar, user and
 * system, the only page that reaches one once it is archived (D17a). A
 * group of one is a single row; a larger group is a card listing its
 * tabs, the live ones too, so it reads as the dashboard it is in the
 * sidebar. A user tab restores on its own, and the card restores every
 * archived tab at once; a system group restores only whole (D1), so its
 * tabs have no button of their own. Its buttons wait while one action
 * runs; reporting dev, which takes no writes, shows none.
 */
export default function Archive() {
  const { data } = useQuery(dashboardsQuery)
  const dashboards = data?.dashboards ?? []
  const writable = data?.dev !== true
  const purgeDays = data?.purge_after_days
  const { restore, pending } = useDashboardActions()
  const groups = archivedGroups(dashboards)
  const yours = groups.filter((g) => g.owner === 'user')
  const system = groups.filter((g) => g.owner === 'system')

  /** A tab's status line: archived (and when it goes) or still in the sidebar. */
  const status = (d: DashboardInfo) => {
    if (!d.archived_at) return 'in the sidebar'
    if (d.owner === 'system') return 'archived · never deleted'
    const purged = purgeDate(d.archived_at, purgeDays)
    return purged ? `archived · deleted on ${formatPurgeDate(purged)}` : 'archived'
  }

  const restoreButton = (label: string, onClick: () => void) =>
    writable && (
      <Button variant="outline" size="sm" disabled={pending} onClick={onClick}>
        {label}
      </Button>
    )

  const renderGroup = (g: Group) => {
    const archived = g.members.filter((m) => m.archived_at)
    if (g.members.length === 1) {
      const d = g.members[0]
      return (
        <li key={g.groupId} className="flex items-center justify-between gap-3 rounded-lg border p-3">
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <Link to={`/dashboards/${d.dashboard_id}`} className="truncate font-medium underline-offset-2 hover:underline">
              {d.title}
            </Link>
            <span className="text-xs text-muted-foreground">{status(d)}</span>
          </div>
          {restoreButton('Restore', () =>
            void (g.owner === 'system' ? restore(d.dashboard_id, true) : restore(d.dashboard_id))
          )}
        </li>
      )
    }
    const title = groupTitle(g).title
    const whole = archived.length === g.members.length
    const meta = [`${g.members.length} tabs`, whole ? 'all archived' : `${archived.length} archived`].join(' · ')
    return (
      <li key={g.groupId} aria-label={title} className="flex flex-col rounded-lg border">
        <div className="flex items-center justify-between gap-3 border-b bg-muted/40 p-3">
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <LayersIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
            <span className="truncate font-medium">{title}</span>
            <Badge variant="outline" className="text-muted-foreground">
              {meta}
            </Badge>
          </div>
          {(g.owner === 'system' || archived.length > 1) &&
            restoreButton(g.owner === 'system' ? 'Restore group' : 'Restore all', () =>
              void restore(archived[0].dashboard_id, true)
            )}
        </div>
        <ul className="flex flex-col divide-y">
          {g.members.map((d) => (
            <li key={d.dashboard_id} className="flex items-center justify-between gap-3 py-2 pr-3 pl-9">
              <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                <Link
                  to={`/dashboards/${d.dashboard_id}`}
                  className={`truncate text-sm underline-offset-2 hover:underline ${d.archived_at ? 'font-medium' : 'text-muted-foreground'}`}
                >
                  {d.title}
                </Link>
                <span className="text-xs text-muted-foreground">{status(d)}</span>
              </div>
              {g.owner === 'user' && d.archived_at && restoreButton('Restore', () => void restore(d.dashboard_id))}
            </li>
          ))}
        </ul>
      </li>
    )
  }

  return (
    <AppShell dashboards={dashboards} currentId={0} readOnly={data?.dev === true}>
      <TopBar>
        <Crumbs items={[{ label: 'Archive' }]} />
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold tracking-tight">Archive</h1>
          <p className="max-w-prose text-sm text-muted-foreground">
            Archived dashboards are out of the sidebar. Restore one to put it back.
            {purgeDays ? ` Archived dashboards of your own are deleted after ${purgeDays} days.` : ''}
          </p>
        </header>
        {groups.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing archived.</p>
        ) : (
          <>
            {yours.length > 0 && (
              <section className="flex flex-col gap-3">
                <h2 className="text-base font-semibold">Yours</h2>
                <ul className="flex flex-col gap-2">{yours.map(renderGroup)}</ul>
              </section>
            )}
            {system.length > 0 && (
              <section className="flex flex-col gap-3">
                <h2 className="text-base font-semibold">System</h2>
                <ul className="flex flex-col gap-2">{system.map(renderGroup)}</ul>
              </section>
            )}
          </>
        )}
      </div>
    </AppShell>
  )
}
