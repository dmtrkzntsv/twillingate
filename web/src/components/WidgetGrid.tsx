import { memo, useRef } from 'react'
import { useElementWidth } from '@/hooks/use-element-width'
import type { Widget, WidgetDataQuery } from '@/lib/api'
import { span } from '@/lib/grid'
import WidgetCard from './WidgetCard'

interface Props {
  widgets: Widget[]
  /** The data query for one widget: what it follows of the page's selection. */
  paramsFor: (widget: Widget) => WidgetDataQuery
  /** Show what is cached, but load nothing (the page is about to change). */
  idle?: boolean
}

/**
 * The 12-column grid (D10, D37): 40px rows, 12px gaps, widgets in order.
 * Only the column spans adapt to the grid's width; rows keep their height
 * and there is no `dense` packing, so a narrow grid is the same list
 * wrapped sooner.
 */
function WidgetGrid({ widgets, paramsFor, idle = false }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const width = useElementWidth(ref)

  return (
    <div ref={ref} data-slot="widget-grid" className="grid auto-rows-[40px] grid-cols-12 gap-3">
      {widgets.map((widget) => {
        const columns = span(widget.width, width)
        return (
          <div
            key={widget.widget_id}
            data-span={columns}
            className="min-w-0"
            style={{ gridColumn: `span ${columns} / span ${columns}`, gridRow: `span ${widget.height} / span ${widget.height}` }}
          >
            <WidgetCard widget={widget} params={paramsFor(widget)} idle={idle} />
          </div>
        )
      })}
    </div>
  )
}

// The page re-renders on a clock (for "data as of" and refresh); the grid
// only needs to when its widgets or their parameters change.
export default memo(WidgetGrid)
