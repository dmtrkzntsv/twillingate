import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { DashboardInfo } from '@/lib/api'

interface Props {
  /** The live system dashboards, in sort-key order. */
  reports: DashboardInfo[]
  currentId: number
  onSelect: (dashboardId: number) => void
}

/**
 * The system reports as tabs (D36): a row on wide screens that scrolls
 * sideways when it runs out of room, and a select on phones.
 */
export default function ReportTabs({ reports, currentId, onSelect }: Props) {
  const value = String(currentId)
  const select = (v: string) => onSelect(Number(v))

  return (
    <>
      <Tabs value={value} onValueChange={select} className="hidden h-full min-w-0 self-stretch sm:flex">
        {/* As tall as the bar, so the active tab's underline is not clipped by the scroller. */}
        <div className="flex h-full items-center overflow-x-auto overflow-y-hidden [scrollbar-width:none]">
          <TabsList variant="line" aria-label="Reports" className="w-max">
            {reports.map((d) => (
              <TabsTrigger key={d.dashboard_id} value={String(d.dashboard_id)} className="flex-none px-3">
                {d.title}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
      </Tabs>
      <Select value={value} onValueChange={select}>
        <SelectTrigger size="sm" aria-label="Report" className="min-w-36 sm:hidden">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper" align="start">
          {reports.map((d) => (
            <SelectItem key={d.dashboard_id} value={String(d.dashboard_id)}>
              {d.title}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </>
  )
}
