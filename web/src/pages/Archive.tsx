import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardInfo } from '@/lib/api'
import { purgeDate } from '@/lib/arrange'
import { dashboardsQuery } from '@/lib/queries'
import { formatPurgeDate } from '@/lib/time'

interface SystemGroup {
  groupId: number
  members: DashboardInfo[]
}

/** Every system dashboard grouped by `group_id`, in list order (D17a). */
function systemGroups(dashboards: DashboardInfo[]): SystemGroup[] {
  const groups: SystemGroup[] = []
  for (const d of dashboards) {
    if (d.owner !== 'system') continue
    const last = groups.at(-1)
    if (last && last.groupId === d.group_id) last.members.push(d)
    else groups.push({ groupId: d.group_id, members: [d] })
  }
  return groups
}

/**
 * What an archived user dashboard's row names its group by: the first
 * still-live *other* member, else the group's literal first other member,
 * or undefined when it has none — excluding `d` itself, so a dashboard
 * never names its own group after itself when the whole group is archived
 * (D17a).
 */
function groupLabel(dashboards: DashboardInfo[], d: DashboardInfo): string | undefined {
  const others = dashboards.filter((m) => m.group_id === d.group_id && m.dashboard_id !== d.dashboard_id)
  if (others.length === 0) return undefined
  return (others.find((m) => !m.archived_at) ?? others[0]).title
}

/**
 * `/archive`: every dashboard out of the sidebar, user and system, each
 * with a Restore button — the only page that reaches one once it is
 * archived (D17a). Its buttons wait while one action runs; reporting dev,
 * which takes no writes, shows none.
 */
export default function Archive() {
  const { data } = useQuery(dashboardsQuery)
  const dashboards = data?.dashboards ?? []
  const writable = data?.dev !== true
  const purgeDays = data?.purge_after_days
  const { restore, pending } = useDashboardActions()
  const archivedUsers = dashboards.filter((d) => d.owner === 'user' && d.archived_at)
  const archivedSystemGroups = systemGroups(dashboards).filter((g) => g.members.some((m) => m.archived_at))
  const nothing = archivedUsers.length === 0 && archivedSystemGroups.length === 0

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
        {nothing ? (
          <p className="text-sm text-muted-foreground">Nothing archived.</p>
        ) : (
          <>
            {archivedUsers.length > 0 && (
              <section className="flex flex-col gap-3">
                <h2 className="text-base font-semibold">Yours</h2>
                <ul className="flex flex-col gap-2">
                  {archivedUsers.map((d) => {
                    const label = groupLabel(dashboards, d)
                    const purged = purgeDate(d.archived_at!, purgeDays)
                    const meta = [label && `in ${label}`, purged && `deleted on ${formatPurgeDate(purged)}`]
                      .filter(Boolean)
                      .join(' · ')
                    return (
                      <li key={d.dashboard_id} className="flex items-center justify-between gap-3 rounded-lg border p-3">
                        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                          <Link to={`/dashboards/${d.dashboard_id}`} className="truncate font-medium underline-offset-2 hover:underline">
                            {d.title}
                          </Link>
                          {meta && <span className="text-xs text-muted-foreground">{meta}</span>}
                        </div>
                        {writable && (
                          <Button variant="outline" size="sm" disabled={pending} onClick={() => void restore(d.dashboard_id)}>
                            Restore
                          </Button>
                        )}
                      </li>
                    )
                  })}
                </ul>
              </section>
            )}
            {archivedSystemGroups.length > 0 && (
              <section className="flex flex-col gap-3">
                <h2 className="text-base font-semibold">System</h2>
                <ul className="flex flex-col gap-2">
                  {archivedSystemGroups.map((g) => {
                    const first = g.members[0]
                    const meta = [g.members.length > 1 && `${g.members.length} tabs`, 'never deleted'].filter(Boolean).join(' · ')
                    return (
                      <li key={g.groupId} className="flex items-center justify-between gap-3 rounded-lg border p-3">
                        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                          <Link
                            to={`/dashboards/${first.dashboard_id}`}
                            className="truncate font-medium underline-offset-2 hover:underline"
                          >
                            {first.title}
                          </Link>
                          <span className="text-xs text-muted-foreground">{meta}</span>
                        </div>
                        {writable && (
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={pending}
                            onClick={() => void restore(first.dashboard_id, true)}
                          >
                            Restore
                          </Button>
                        )}
                      </li>
                    )
                  })}
                </ul>
              </section>
            )}
          </>
        )}
      </div>
    </AppShell>
  )
}
