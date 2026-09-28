import { useRef, type ReactNode } from 'react'
import { useElementWidth } from '@/hooks/use-element-width'
import { span } from '@/lib/grid'

export interface GridCell {
  key: string | number
  /** Columns out of 12 at full width; `span` narrows it on smaller grids. */
  width: number
  /** 40px rows. */
  height: number
  node: ReactNode
}

/**
 * The 12-column grid (D10, D37): 40px rows, 12px gaps, cells in order.
 * Only the column spans adapt to the grid's width; rows keep their height
 * and there is no `dense` packing, so a narrow grid is the same list
 * wrapped sooner.
 */
export default function LayoutGrid({ cells }: { cells: GridCell[] }) {
  const ref = useRef<HTMLDivElement>(null)
  const width = useElementWidth(ref)
  return (
    // auto-rows-[40px] and gap-3 are literal (Tailwind needs static class
    // names) but must stay in step with ROW_PX and GAP_PX in lib/grid.ts,
    // which rowsPx() uses to size cells that need an exact pixel height.
    <div ref={ref} data-slot="widget-grid" className="grid auto-rows-[40px] grid-cols-12 gap-3">
      {cells.map((cell) => {
        const columns = span(cell.width, width)
        return (
          <div
            key={cell.key}
            data-span={columns}
            className="min-w-0"
            style={{ gridColumn: `span ${columns} / span ${columns}`, gridRow: `span ${cell.height} / span ${cell.height}` }}
          >
            {cell.node}
          </div>
        )
      })}
    </div>
  )
}
