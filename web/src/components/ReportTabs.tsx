import { DndContext, closestCenter } from '@dnd-kit/core'
import { SortableContext, horizontalListSortingStrategy } from '@dnd-kit/sortable'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useReorder } from '@/hooks/use-reorder'
import type { DashboardTab } from '@/lib/api'
import SortableTab from './SortableTab'

interface Props {
  /** A dashboard group's live members, in order (tabs D18, D21). */
  tabs: DashboardTab[]
  currentId: number
  onSelect: (dashboardId: number) => void
  /**
   * A user group's tabs: rendered as sortable whether or not a move is
   * allowed right now, so the tab list is not rebuilt (and focus lost)
   * when the page freezes (D14).
   */
  sortable?: boolean
  /**
   * Moves tab `id` to index `to` (D14, D15); given only while a move is
   * allowed, which needs `sortable`. Resolves false when the move was
   * refused, which snaps the tab back.
   */
  onMove?: (id: number, to: number) => Promise<boolean>
}

/**
 * A dashboard group's tabs (D36, tabs D21): a row on wide screens that
 * scrolls sideways when it runs out of room, and a select on phones. Shown
 * for any group with more than one live member, system or user. With
 * `sortable` and `onMove`, the row's tabs drag to a new order (D14);
 * phones reorder from the header menu's Move left/right instead.
 */
export default function ReportTabs({ tabs, currentId, onSelect, sortable = false, onMove }: Props) {
  const value = String(currentId)
  const select = (v: string) => onSelect(Number(v))
  const byId = new Map(tabs.map((t) => [t.dashboard_id, t]))
  const { order, busy, context } = useReorder(
    tabs.map((t) => t.dashboard_id),
    onMove ?? noMove,
    'x',
    (id) => byId.get(id)?.title ?? String(id)
  )
  const trigger = 'flex-none px-3 after:rounded-full after:bg-primary! data-[state=active]:text-foreground'

  const list = (
    <TabsList variant="line" aria-label="Tabs" className="w-max">
      {sortable
        ? order.map((id) => (
            <SortableTab
              key={id}
              id={id}
              title={byId.get(id)!.title}
              className={trigger}
              movable={onMove !== undefined}
              disabled={busy}
              onSelect={onSelect}
            />
          ))
        : tabs.map((d) => (
            <TabsTrigger key={d.dashboard_id} value={String(d.dashboard_id)} className={trigger}>
              {d.title}
            </TabsTrigger>
          ))}
    </TabsList>
  )

  return (
    <>
      {/* Manual activation: arrowing across the tabs only moves focus, so it
          does not open (and save a selection on) every tab on the way. */}
      <Tabs value={value} onValueChange={select} activationMode="manual" className="hidden h-full min-w-0 self-stretch sm:flex">
        {/* As tall as the bar, so the active tab's underline is not clipped by the scroller. */}
        <div className="flex h-full items-center overflow-x-auto overflow-y-hidden [scrollbar-width:none]">
          {sortable ? (
            <DndContext collisionDetection={closestCenter} {...context}>
              <SortableContext items={order} strategy={horizontalListSortingStrategy}>
                {list}
              </SortableContext>
            </DndContext>
          ) : (
            list
          )}
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

const noMove = async () => false
