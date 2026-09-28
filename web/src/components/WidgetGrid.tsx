import { memo } from 'react'
import type { Widget, WidgetDataQuery } from '@/lib/api'
import LayoutGrid from './LayoutGrid'
import WidgetCard from './WidgetCard'

interface Props {
  widgets: Widget[]
  /** The data query for one widget: what it follows of the page's selection. */
  paramsFor: (widget: Widget) => WidgetDataQuery
  /** Show what is cached, but load nothing (the page is about to change). */
  idle?: boolean
}

/** A dashboard's widgets on the grid, in order. */
function WidgetGrid({ widgets, paramsFor, idle = false }: Props) {
  return (
    <LayoutGrid
      cells={widgets.map((widget) => ({
        key: widget.widget_id,
        width: widget.width,
        height: widget.height,
        node: <WidgetCard widget={widget} params={paramsFor(widget)} idle={idle} />,
      }))}
    />
  )
}

// The page re-renders on a clock (for "data as of" and refresh); the grid
// only needs to when its widgets or their parameters change.
export default memo(WidgetGrid)
