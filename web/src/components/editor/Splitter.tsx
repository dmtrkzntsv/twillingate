import { useRef, type CSSProperties } from 'react'
import { cn } from '@/lib/utils'

interface Props {
  /** `vertical`: a line between columns, dragged sideways; `horizontal`: a line between rows, dragged up and down. */
  orientation: 'vertical' | 'horizontal'
  label: string
  value: number
  min: number
  max: number
  /** How far one arrow key press moves it. */
  step: number
  /** How many units of `value` one pixel of drag is. */
  perPx: number
  onChange(value: number): void
  /** A double click puts it back where it started. */
  onReset(): void
  /** Where it sits, as its container lays it out. */
  style: CSSProperties
  className?: string
}

const clamp = (v: number, min: number, max: number) => Math.min(max, Math.max(min, v))

/**
 * A line between two panes that drags to resize them: by pointer, or from
 * the keyboard with the arrow keys once focused (Home and End go to the
 * ends). The hit area is wider than the line it draws.
 */
export default function Splitter({ orientation, label, value, min, max, step, perPx, onChange, onReset, style, className }: Props) {
  const drag = useRef<{ from: number; start: number } | null>(null)
  const vertical = orientation === 'vertical'
  const at = (e: React.PointerEvent) => (vertical ? e.clientX : e.clientY)

  return (
    <div
      role="separator"
      aria-orientation={orientation}
      aria-label={label}
      aria-valuenow={Math.round(value)}
      aria-valuemin={min}
      aria-valuemax={max}
      tabIndex={0}
      style={style}
      className={cn(
        'group absolute z-20 flex touch-none items-center justify-center outline-none select-none',
        vertical ? 'w-2 -translate-x-1/2 cursor-col-resize' : 'h-2 -translate-y-1/2 cursor-row-resize',
        className
      )}
      onPointerDown={(e) => {
        if (e.button !== 0) return
        e.preventDefault()
        e.currentTarget.setPointerCapture?.(e.pointerId)
        drag.current = { from: at(e), start: value }
      }}
      onPointerMove={(e) => {
        if (!drag.current) return
        onChange(clamp(drag.current.start + (at(e) - drag.current.from) * perPx, min, max))
      }}
      onPointerUp={() => (drag.current = null)}
      onPointerCancel={() => (drag.current = null)}
      onDoubleClick={onReset}
      onKeyDown={(e) => {
        const back = vertical ? 'ArrowLeft' : 'ArrowUp'
        const forward = vertical ? 'ArrowRight' : 'ArrowDown'
        const next =
          e.key === back ? value - step : e.key === forward ? value + step : e.key === 'Home' ? min : e.key === 'End' ? max : null
        if (next === null) return
        e.preventDefault()
        onChange(clamp(next, min, max))
      }}
    >
      {/* The line itself: the border it sits on, drawn in the ring color while held or focused. */}
      <span
        className={cn(
          'bg-transparent transition-colors group-hover:bg-ring/60 group-focus-visible:bg-ring group-active:bg-ring',
          vertical ? 'h-full w-0.5' : 'h-0.5 w-full'
        )}
      />
    </div>
  )
}
