import type { CSSProperties } from 'react'
import { Skeleton } from '@/components/ui/skeleton'
import type { Widget } from '@/lib/api'

// Fixed, not random: the same widget loads in the same shape every time.
const WIDTHS = [92, 74, 66, 58, 51, 45, 40, 34, 30, 26, 22, 19, 16, 14]
const HEIGHTS = [46, 62, 55, 78, 70, 88, 64, 72, 94, 80, 58, 68]
// A rolling skyline, as a clip-path over one frost block.
const RIDGE = 'polygon(0 62%, 9% 48%, 18% 54%, 29% 34%, 40% 42%, 51% 22%, 62% 38%, 72% 30%, 83% 46%, 92% 26%, 100% 34%, 100% 100%, 0 100%)'

/**
 * A widget's placeholder while its data loads: the rough shape of what is
 * coming (a number, a chart, rows, cells), so the grid does not jump when
 * it arrives. Unknown components get a plain block.
 */
export default function WidgetSkeleton({ widget }: { widget: Pick<Widget, 'component' | 'props'> }) {
  return (
    <div role="status" aria-label="Loading" className="flex h-full min-h-0 flex-col overflow-hidden p-1">
      <Shape component={widget.component} donut={widget.props.donut === true} />
    </div>
  )
}

function Shape({ component, donut }: { component: string | null; donut: boolean }) {
  switch (component) {
    case 'stat':
      return (
        <div className="flex h-full flex-col justify-center gap-3 p-1">
          <Skeleton className="h-8 w-28" />
          <Skeleton className="h-5 w-14 rounded-full" />
        </div>
      )
    case 'line':
    case 'area':
    case 'combo':
    case 'scatter':
      return (
        <div className="relative flex h-full flex-col justify-between py-2 pl-9">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-px w-full rounded-none opacity-80" />
          ))}
          <Skeleton className="absolute inset-y-2 left-9 right-0 rounded-none opacity-70" style={{ clipPath: RIDGE }} />
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="absolute left-0 h-2.5 w-6" style={{ top: `${8 + i * 33}%` }} />
          ))}
        </div>
      )
    case 'bar':
      return (
        <div className="flex h-full items-end gap-[3%] px-1 pt-3 pb-1">
          {HEIGHTS.map((h, i) => (
            <Skeleton key={i} className="flex-1 rounded-b-none" style={{ height: `${h}%` }} />
          ))}
        </div>
      )
    case 'funnel':
      return (
        <div className="flex h-full flex-col justify-center gap-2 px-1">
          {[100, 72, 50, 34, 22].map((w, i) => (
            <Skeleton key={i} className="h-6" style={{ width: `${w}%` }} />
          ))}
        </div>
      )
    case 'bar_list':
    case 'table':
      return (
        <div className="flex flex-col gap-2 pt-1">
          {component === 'table' && <Skeleton className="mb-1 h-3 w-1/3" />}
          {WIDTHS.map((w, i) => (
            <div key={i} className="flex items-center gap-3">
              <Skeleton className="h-6" style={{ width: `${w * 0.8}%` }} />
              <Skeleton className="ml-auto h-3 w-8 shrink-0" />
            </div>
          ))}
        </div>
      )
    case 'pie':
    case 'radial':
    case 'radar':
      return (
        <div className="flex h-full items-center justify-center p-2">
          <Skeleton
            className="aspect-square h-full max-h-56 max-w-full rounded-full"
            style={donut ? ring : undefined}
          />
        </div>
      )
    case 'heatmap':
    case 'calendar':
      return (
        <div className="grid h-full grid-cols-[repeat(auto-fill,minmax(14px,1fr))] content-start gap-1">
          {Array.from({ length: 168 }, (_, i) => (
            <Skeleton key={i} className="aspect-square rounded-[3px]" style={{ opacity: 0.45 + ((i * 37) % 11) / 20 }} />
          ))}
        </div>
      )
    case 'treemap':
      return (
        <div className="grid h-full grid-cols-4 grid-rows-3 gap-1">
          <Skeleton className="col-span-2 row-span-3" />
          <Skeleton className="col-span-2 row-span-2" />
          <Skeleton />
          <Skeleton />
        </div>
      )
    case 'markdown':
      return (
        <div className="flex flex-col gap-2.5 pt-1">
          {[96, 88, 92, 60].map((w, i) => (
            <Skeleton key={i} className="h-3" style={{ width: `${w}%` }} />
          ))}
        </div>
      )
    default:
      return <Skeleton className="h-full w-full" />
  }
}

/** A ring rather than a disc, for the pie's donut. */
const ring: CSSProperties = {
  mask: 'radial-gradient(circle, transparent 42%, #000 43%)',
  WebkitMask: 'radial-gradient(circle, transparent 42%, #000 43%)',
}
