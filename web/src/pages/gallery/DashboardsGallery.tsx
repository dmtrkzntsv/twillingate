import { useQuery } from '@tanstack/react-query'
import { DashboardGroup, LoneDashboard, type GroupRow } from '@/components/DashboardGroup'
import TemplateMenu from '@/components/TemplateMenu'
import type { DashboardInfo } from '@/lib/api'
import { groupName } from '@/lib/arrange'
import { dashboardsQuery } from '@/lib/queries'
import GalleryLayout from './GalleryLayout'

const sections = [{ id: 'system', label: 'System' }]

interface SystemGroup {
  groupId: number
  members: DashboardInfo[]
}

/**
 * Every system dashboard grouped by `group_id`, in list order, hidden
 * (archived) ones included: a template is a template whether or not it is
 * in the sidebar right now (D17).
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

const HIDDEN = 'hidden'

const widgets = (d: DashboardInfo) => `${d.widgets} ${d.widgets === 1 ? 'widget' : 'widgets'}`

/**
 * `/gallery/dashboards`: every system group as a template, shown as on the
 * Archive page (D17): a group of one is a row, a larger group a card with
 * its tabs. The group's "…" menu duplicates the whole group, and adds a
 * group hidden from the sidebar back to it; a tab's copies that tab to a
 * new dashboard. Reporting dev, which takes no writes, shows no menu.
 */
export default function DashboardsGallery() {
  const { data } = useQuery(dashboardsQuery)
  const dashboards = data?.dashboards ?? []
  const writable = data?.dev !== true
  const groups = systemGroups(dashboards)

  return (
    <GalleryLayout
      title="Dashboards"
      description="The dashboards that ship with twillingate. Open one to look at it, or make it your own: duplicate the whole dashboard from its … menu, or copy one tab to a new dashboard from that tab's … menu."
      sections={sections}
    >
      <section id="system" aria-labelledby="system-heading" className="flex scroll-mt-16 flex-col gap-3">
        <h2 id="system-heading" className="text-base font-semibold">
          System
        </h2>
        <ul className="flex flex-col gap-2">
          {groups.map((g) => {
            const first = g.members[0]
            // A system group is hidden (archived) only whole.
            const hidden = g.members.every((m) => m.archived_at)
            const groupMenu = writable && <TemplateMenu dashboard={first} wholeGroup hidden={hidden} />
            if (g.members.length === 1) {
              const detail = [widgets(first), hidden && HIDDEN].filter(Boolean).join(' · ')
              return <LoneDashboard key={g.groupId} row={{ ...first, detail, action: groupMenu }} />
            }
            const rows: GroupRow[] = g.members.map((d) => ({
              ...d,
              detail: widgets(d),
              action: writable && <TemplateMenu dashboard={d} />,
            }))
            const meta = [`${g.members.length} tabs`, hidden && HIDDEN].filter(Boolean).join(' · ')
            return <DashboardGroup key={g.groupId} title={groupName(g.members)} meta={meta} action={groupMenu} rows={rows} />
          })}
        </ul>
      </section>
    </GalleryLayout>
  )
}
