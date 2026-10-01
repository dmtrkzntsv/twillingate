import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardInfo } from '@/lib/api'
import { purgeDate } from '@/lib/arrange'
import { dashboardsQuery } from '@/lib/queries'
import { formatPurgeDate } from '@/lib/time'
import GalleryLayout from './GalleryLayout'

const sections = [
  { id: 'system', label: 'System' },
  { id: 'archived', label: 'Archived' },
]

interface SystemGroup {
  groupId: number
  members: DashboardInfo[]
}

/**
 * Every system dashboard, archived ones included, grouped by `group_id` in
 * list order: the gallery's only view of a fully archived system group,
 * which `liveGroups` leaves out entirely (D17).
 */
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
 * (D17).
 */
function groupLabel(dashboards: DashboardInfo[], d: DashboardInfo): string | undefined {
  const others = dashboards.filter((m) => m.group_id === d.group_id && m.dashboard_id !== d.dashboard_id)
  if (others.length === 0) return undefined
  return (others.find((m) => !m.archived_at) ?? others[0]).title
}

/**
 * `/gallery/dashboards`: every system group, live or archived, with its
 * tabs to copy as a dashboard of your own, and every archived user
 * dashboard to restore — the only page that reaches a dashboard archived
 * out of the sidebar (D17, D18). Its buttons wait while one action runs,
 * so a double click makes one copy, not two; reporting dev, which takes
 * no writes, shows none.
 */
export default function DashboardsGallery() {
  const { data } = useQuery(dashboardsQuery)
  const dashboards = data?.dashboards ?? []
  const writable = data?.dev !== true
  const { duplicate, archive, restore, pending } = useDashboardActions()
  const groups = systemGroups(dashboards)
  const archivedUsers = dashboards.filter((d) => d.owner === 'user' && d.archived_at)

  return (
    <GalleryLayout
      title="Dashboards"
      description="The dashboards that ship with twillingate, and the ones you archived. Restore one to put it back in the sidebar, or copy a system tab as a dashboard of your own."
      sections={sections}
    >
      <section id="system" aria-labelledby="system-heading" className="flex scroll-mt-16 flex-col gap-3">
        <h2 id="system-heading" className="text-base font-semibold">
          System
        </h2>
        <ul className="flex flex-col gap-3">
          {groups.map((g) => {
            const first = g.members[0]
            const live = g.members.some((m) => !m.archived_at)
            return (
              <li key={g.groupId} className="flex flex-col gap-2 rounded-lg border p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    {/* A heading, not a span, so a lone-tab group (its tab
                        repeats this same title as a link) can still be
                        told apart by an agent or a test. */}
                    <h3 className="text-sm font-medium">{first.title}</h3>
                    <Badge variant={live ? 'secondary' : 'outline'}>{live ? 'In the sidebar' : 'Archived'}</Badge>
                  </div>
                  {writable && (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={pending}
                      onClick={() => void (live ? archive(first, { wholeGroup: true }) : restore(first.dashboard_id, true))}
                    >
                      {live ? 'Archive' : 'Restore'}
                    </Button>
                  )}
                </div>
                <ul className="flex flex-col gap-1">
                  {g.members.map((tab) => (
                    <li key={tab.dashboard_id} className="flex items-center justify-between gap-2 text-sm">
                      <Link
                        to={`/dashboards/${tab.dashboard_id}`}
                        className="min-w-0 flex-1 truncate underline-offset-2 hover:underline"
                      >
                        {tab.title}
                      </Link>
                      {writable && (
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={pending}
                          onClick={() => void duplicate(tab, { archiveSource: false })}
                        >
                          Copy as a dashboard
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              </li>
            )
          })}
        </ul>
      </section>
      <section id="archived" aria-labelledby="archived-heading" className="flex scroll-mt-16 flex-col gap-3">
        <h2 id="archived-heading" className="text-base font-semibold">
          Archived
        </h2>
        {archivedUsers.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing archived.</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {archivedUsers.map((d) => {
              const label = groupLabel(dashboards, d)
              const purged = purgeDate(d.archived_at!, data?.purge_after_days)
              const meta = [label && `in ${label}`, purged && `deleted on ${formatPurgeDate(purged)}`].filter(Boolean).join(' · ')
              return (
                <li key={d.dashboard_id} className="flex items-center justify-between gap-3 rounded-lg border p-3">
                  <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span className="truncate font-medium">{d.title}</span>
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
        )}
      </section>
    </GalleryLayout>
  )
}
