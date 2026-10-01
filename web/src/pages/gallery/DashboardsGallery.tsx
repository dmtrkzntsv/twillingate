import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import TemplateMenu from '@/components/TemplateMenu'
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
 * `/gallery/dashboards`: every system group as a template, one row each
 * (D17). The row opens the group's first tab; its "…" menu duplicates the
 * whole group, and an opened tab's own "…" menu duplicates just that tab.
 * Reporting dev, which takes no writes, shows no menu.
 */
export default function DashboardsGallery() {
  const { data } = useQuery(dashboardsQuery)
  const dashboards = data?.dashboards ?? []
  const writable = data?.dev !== true
  const groups = systemGroups(dashboards)

  return (
    <GalleryLayout
      title="Templates"
      description="The dashboards that ship with twillingate. Open one to look at it, or duplicate it as a dashboard of your own: the whole thing from its … menu here, or one tab from the … menu on that tab."
      sections={sections}
    >
      <section id="system" aria-labelledby="system-heading" className="flex scroll-mt-16 flex-col gap-3">
        <h2 id="system-heading" className="text-base font-semibold">
          System
        </h2>
        <ul className="flex flex-col gap-2">
          {groups.map((g) => {
            const first = g.members[0]
            const n = g.members.length
            return (
              // The title's link stretches over the whole row, so the row
              // opens the template; the menu sits above it.
              <li key={g.groupId} className="relative flex items-center gap-3 rounded-lg border p-3 hover:bg-accent/40">
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <h3 className="truncate text-sm font-medium">
                    <Link to={`/dashboards/${first.dashboard_id}`} className="after:absolute after:inset-0 after:rounded-lg">
                      {first.title}
                    </Link>
                  </h3>
                  {n > 1 && (
                    <span className="truncate text-xs text-muted-foreground">
                      {n} tabs · {g.members.map((m) => m.title).join(', ')}
                    </span>
                  )}
                </div>
                {writable && (
                  <div className="relative">
                    <TemplateMenu first={first} />
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      </section>
    </GalleryLayout>
  )
}
