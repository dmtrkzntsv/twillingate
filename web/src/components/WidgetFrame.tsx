import type { ReactNode } from 'react'
import { Card } from '@/components/ui/card'

interface Props {
  title?: string | null
  /** Beside the title, e.g. the "partial" badge. */
  badge?: ReactNode
  /** Top-right controls, e.g. refresh. */
  actions?: ReactNode
  /** How many 28px controls `actions` holds at most (1 to 3): the title leaves room for them. */
  slots?: number
  /** A control in the bottom-right corner, e.g. the resize handle. */
  corner?: ReactNode
  children: ReactNode
}

const titleRoom = ['', 'pr-8', 'pr-16', 'pr-24']

/** A widget's card: the title row, the body, and controls in the corners. */
export default function WidgetFrame({ title, badge, actions, slots = 1, corner, children }: Props) {
  return (
    <Card
      data-slot="widget-card"
      className="relative h-full min-w-0 gap-1.5 overflow-hidden p-3.5 shadow-[0_1px_2px_rgb(11_31_54/0.04),0_6px_16px_-10px_rgb(11_31_54/0.14)] dark:shadow-none"
    >
      {(title || badge) && (
        <div className={`flex min-h-7 min-w-0 items-center gap-2 ${actions ? titleRoom[slots] : ''}`}>
          {title && <h3 className="truncate text-sm font-medium text-muted-foreground">{title}</h3>}
          {badge}
        </div>
      )}
      <div data-slot="widget-body" className="relative min-h-0 flex-1 overflow-auto">
        {children}
      </div>
      {actions && <div className="absolute top-2.5 right-2.5 flex items-center">{actions}</div>}
      {corner}
    </Card>
  )
}
