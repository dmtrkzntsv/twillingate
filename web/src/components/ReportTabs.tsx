import type { ReactNode } from 'react'
import { DndContext, closestCenter } from '@dnd-kit/core'
import { SortableContext, horizontalListSortingStrategy } from '@dnd-kit/sortable'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useReorder } from '@/hooks/use-reorder'
import type { DashboardTab } from '@/lib/api'
import SortableTab from './SortableTab'

/** One tab: a dashboard, or a project's form (Forms page). */
export interface TabItem {
  /** Unique among the tabs, and never `currentId` when none is current. */
  id: number
  title: string
  /** Before the title: a draft form's icon. */
  icon?: ReactNode
  /** After the title: a form's count of new submissions. */
  badge?: ReactNode
  /**
   * The tab's accessible name and the phone select's text, when the icon
   * or badge says something the title does not ("contact (draft)").
   */
  label?: string
}

/** A dashboard group's members as tabs. */
export function dashboardTabs(tabs: DashboardTab[]): TabItem[] {
  return tabs.map((t) => ({ id: t.dashboard_id, title: t.title }))
}

interface Props {
  /** A dashboard group's live members, in order (tabs D18, D21), or a project's forms. */
  tabs: TabItem[]
  currentId: number
  onSelect: (id: number) => void
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
  /** After the row on wide screens, beside the select on phones (a project's "+", the Forms page's Find a person). */
  trailing?: ReactNode
}

/**
 * A dashboard group's tabs (D36, tabs D21): a row on wide screens that
 * scrolls sideways when it runs out of room, and a select on phones. Shown
 * for every dashboard, a group of one tab included, system or user. With
 * `sortable` and `onMove`, the row's tabs drag to a new order (D14);
 * phones reorder from the header menu's Move left/right instead. When
 * `currentId` is none of the tabs (a project's Settings or Forms page) no
 * tab is selected, and the phone select shows "Tabs". A project's Forms
 * page shows its forms through the same row, each with its draft icon and
 * new count (`TabItem`).
 */
export default function ReportTabs({ tabs, currentId, onSelect, sortable = false, onMove, trailing }: Props) {
  const value = tabs.some((t) => t.id === currentId) ? String(currentId) : ''
  const select = (v: string) => onSelect(Number(v))
  const byId = new Map(tabs.map((t) => [t.id, t]))
  const { order, busy, context } = useReorder(
    tabs.map((t) => t.id),
    onMove ?? noMove,
    'x',
    (id) => byId.get(id)?.title ?? String(id)
  )
  const trigger = 'flex-none px-3 after:rounded-full after:bg-primary! data-[state=active]:text-foreground'

  const list = (
    <TabsList variant="line" aria-label="Tabs" className="w-max">
      {sortable
        ? order.map((id) => {
            const t = byId.get(id)!
            return (
              <SortableTab key={id} id={id} label={t.label} className={trigger} movable={onMove !== undefined} disabled={busy} onSelect={onSelect}>
                {content(t)}
              </SortableTab>
            )
          })
        : tabs.map((t) => (
            <TabsTrigger key={t.id} value={String(t.id)} aria-label={t.label} className={trigger}>
              {content(t)}
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
      {/* An empty list would open as a blank menu (a project with no tabs). */}
      {tabs.length > 0 && (
        <Select value={value} onValueChange={select}>
          {/* Shrinks to the row, clipping a long title, so the row never scrolls sideways on a phone. */}
          <SelectTrigger size="sm" aria-label="Tab" className="min-w-36 shrink *:data-[slot=select-value]:min-w-0 sm:hidden">
            <SelectValue placeholder="Tabs" />
          </SelectTrigger>
          <SelectContent position="popper" align="start">
            {tabs.map((t) => (
              <SelectItem key={t.id} value={String(t.id)}>
                {t.label ?? t.title}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      {trailing}
    </>
  )
}

const noMove = async () => false

/** A tab's face: its icon, title and badge. */
function content(t: TabItem): ReactNode {
  return (
    <>
      {t.icon}
      {t.title}
      {t.badge}
    </>
  )
}
