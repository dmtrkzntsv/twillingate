import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { DashboardTab } from '@/lib/api'

interface Props {
  /** A dashboard group's live members, in order (tabs D18, D21). */
  tabs: DashboardTab[]
  currentId: number
  onSelect: (dashboardId: number) => void
}

/**
 * A dashboard group's tabs (D36, tabs D21): a row on wide screens that
 * scrolls sideways when it runs out of room, and a select on phones. Shown
 * for any group with more than one live member, system or user.
 */
export default function ReportTabs({ tabs, currentId, onSelect }: Props) {
  const value = String(currentId)
  const select = (v: string) => onSelect(Number(v))

  return (
    <>
      {/* Manual activation: arrowing across the tabs only moves focus, so it
          does not open (and save a selection on) every tab on the way. */}
      <Tabs value={value} onValueChange={select} activationMode="manual" className="hidden h-full min-w-0 self-stretch sm:flex">
        {/* As tall as the bar, so the active tab's underline is not clipped by the scroller. */}
        <div className="flex h-full items-center overflow-x-auto overflow-y-hidden [scrollbar-width:none]">
          <TabsList variant="line" aria-label="Tabs" className="w-max">
            {tabs.map((d) => (
              <TabsTrigger
                key={d.dashboard_id}
                value={String(d.dashboard_id)}
                className="flex-none px-3 after:rounded-full after:bg-primary! data-[state=active]:text-foreground"
              >
                {d.title}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
      </Tabs>
      <Select value={value} onValueChange={select}>
        <SelectTrigger size="sm" aria-label="Tab" className="min-w-36 sm:hidden">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper" align="start">
          {tabs.map((d) => (
            <SelectItem key={d.dashboard_id} value={String(d.dashboard_id)}>
              {d.title}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </>
  )
}
