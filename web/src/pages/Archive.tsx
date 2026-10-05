import { useQuery } from '@tanstack/react-query'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import { DashboardGroup, LoneDashboard, type GroupRow } from '@/components/DashboardGroup'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardInfo } from '@/lib/api'
import { purgeDate } from '@/lib/arrange'
import { dashboardsQuery } from '@/lib/queries'
import { formatPurgeDate } from '@/lib/time'

interface Group {
  groupId: number
  /** Every member, live and archived, in list order. */
  members: DashboardInfo[]
}

/**
 * The user's dashboards grouped by `group_id`, in list order, keeping only
 * the groups with an archived member (D17a). A system group is hidden,
 * not archived, and comes back from the Dashboards gallery, so it is never
 * here.
 */
function archivedGroups(dashboards: DashboardInfo[]): Group[] {
  const groups = new Map<number, Group>()
  for (const d of dashboards) {
    if (d.owner !== 'user') continue
    const g = groups.get(d.group_id)
    if (g) g.members.push(d)
    else groups.set(d.group_id, { groupId: d.group_id, members: [d] })
  }
  return [...groups.values()].filter((g) => g.members.some((m) => m.archived_at))
}

/** What the sidebar names a group by: its first live member, else its first member. */
function groupTitle(g: Group): DashboardInfo {
  return g.members.find((m) => !m.archived_at) ?? g.members[0]
}

/**
 * `/archive`: every group of the user's with a dashboard out of the
 * sidebar, the only page that reaches one once it is archived (D17a). A
 * group of one is a single row; a larger group is a card listing its
 * tabs, the live ones too, so it reads as the dashboard it is in the
 * sidebar. Each archived tab restores on its own, and "Restore all"
 * brings back every archived tab at once. Its buttons wait while one
 * action runs; reporting dev, which takes no writes, shows none.
 */
export default function Archive() {
  const { data } = useQuery(dashboardsQuery)
  const dashboards = data?.dashboards ?? []
  const writable = data?.dev !== true
  const purgeDays = data?.purge_after_days
  const { restore, pending } = useDashboardActions()
  const groups = archivedGroups(dashboards)

  /** A tab's status line: archived (and when it goes) or still in the sidebar. */
  const status = (d: DashboardInfo) => {
    if (!d.archived_at) return 'in the sidebar'
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
    const rows: GroupRow[] = g.members.map((d) => ({
      ...d,
      detail: status(d),
      muted: !d.archived_at,
      action: d.archived_at && restoreButton('Restore', () => void restore(d.dashboard_id)),
    }))
    if (rows.length === 1) return <LoneDashboard key={g.groupId} row={rows[0]} />
    const whole = archived.length === g.members.length
    return (
      <DashboardGroup
        key={g.groupId}
        title={groupTitle(g).title}
        meta={`${g.members.length} tabs · ${whole ? 'all archived' : `${archived.length} archived`}`}
        action={archived.length > 1 && restoreButton('Restore all', () => void restore(archived[0].dashboard_id, true))}
        rows={rows}
      />
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
            {purgeDays ? ` They are deleted after ${purgeDays} days.` : ''} A hidden built-in dashboard comes back from Gallery › Dashboards.
          </p>
        </header>
        {groups.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing archived.</p>
        ) : (
          <ul className="flex flex-col gap-2">{groups.map(renderGroup)}</ul>
        )}
      </div>
    </AppShell>
  )
}
