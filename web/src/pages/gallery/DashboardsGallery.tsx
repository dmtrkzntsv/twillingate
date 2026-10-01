import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardInfo } from '@/lib/api'
import { dashboardsQuery } from '@/lib/queries'
import GalleryLayout from './GalleryLayout'

const sections = [{ id: 'system', label: 'System' }]

interface SystemGroup {
  groupId: number
  members: DashboardInfo[]
}

/**
 * Every system dashboard grouped by `group_id`, in list order, archived
 * ones included: a template is a template whether or not it is in the
 * sidebar right now — archive state belongs to the Archive page, not here
 * (D17).
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
 * `/gallery/dashboards`: every system group as a template, to open and
 * look at or copy a tab from as a dashboard of your own (D17). Its
 * buttons wait while one action runs, so a double click makes one copy,
 * not two; reporting dev, which takes no writes, shows none.
 */
export default function DashboardsGallery() {
  const { data } = useQuery(dashboardsQuery)
  const dashboards = data?.dashboards ?? []
  const writable = data?.dev !== true
  const { duplicate, pending } = useDashboardActions()
  const groups = systemGroups(dashboards)

  return (
    <GalleryLayout
      title="Templates"
      description="The dashboards that ship with twillingate. Open one to look at it, or copy a tab as a dashboard of your own."
      sections={sections}
    >
      <section id="system" aria-labelledby="system-heading" className="flex scroll-mt-16 flex-col gap-3">
        <h2 id="system-heading" className="text-base font-semibold">
          System
        </h2>
        <ul className="flex flex-col gap-3">
          {groups.map((g) => {
            const first = g.members[0]
            return (
              <li key={g.groupId} className="flex flex-col gap-2 rounded-lg border p-3">
                {/* A heading, not a span, so a lone-tab group (its tab
                    repeats this same title as a link) can still be told
                    apart by an agent or a test. */}
                <h3 className="text-sm font-medium">{first.title}</h3>
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
                        <Button variant="ghost" size="sm" disabled={pending} onClick={() => void duplicate(tab)}>
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
    </GalleryLayout>
  )
}
