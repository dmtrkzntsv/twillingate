import { useRef, useState } from 'react'
import { CalendarIcon } from 'lucide-react'
import type { DateRange } from 'react-day-picker'
import { Button } from '@/components/ui/button'
import { Calendar } from '@/components/ui/calendar'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { PHONE, useMediaQuery } from '@/hooks/use-media-query'
import { daysBetween, PRESETS, todayIn, type Preset } from '@/lib/ranges'
import SwitcherButton from './SwitcherButton'

export interface RangeValue {
  range: Preset
  from?: string
  to?: string
}

interface Props {
  value: RangeValue
  /** The instance timezone: "today" for the calendar's last selectable day. */
  timezone: string
  onChange: (value: RangeValue) => void
  className?: string
}

const MAX_DAYS = 365

/** `YYYY-MM-DD` as a local-midnight Date, for the calendar. */
function toDate(iso: string): Date {
  const [y, m, d] = iso.split('-').map(Number)
  return new Date(y, m - 1, d)
}

/** A calendar's local Date as `YYYY-MM-DD`. */
function toISO(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

function customLabel(from: string, to: string): string {
  const sameYear = from.slice(0, 4) === to.slice(0, 4)
  const fmt = (iso: string, year: boolean) =>
    toDate(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric', ...(year ? { year: 'numeric' } : {}) })
  return `${fmt(from, !sameYear)} – ${fmt(to, true)}`
}

function label(value: RangeValue): string {
  if (value.range === 'custom' && value.from && value.to) return customLabel(value.from, value.to)
  return PRESETS.find((p) => p.id === value.range)?.label ?? value.range
}

/**
 * Picks the date range: a preset, or "Custom…", which opens a range
 * calendar in a popover (a bottom sheet on phones).
 */
export default function RangeSwitcher({ value, timezone, onChange, className }: Props) {
  const phone = useMediaQuery(PHONE)
  const [picking, setPicking] = useState(false)
  // Choosing "Custom…" closes the menu, which would hand focus back to its
  // trigger and so dismiss the popover that has just opened.
  const keepFocus = useRef(false)
  const today = todayIn(timezone)
  const text = label(value)

  const apply = (from: string, to: string) => {
    setPicking(false)
    onChange({ range: 'custom', from, to })
  }
  const picker = (
    <RangePicker
      initial={value.range === 'custom' ? value : undefined}
      today={today}
      months={phone ? 1 : 2}
      onApply={apply}
    />
  )

  const menu = (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <SwitcherButton icon={<CalendarIcon />} aria-label={`Range: ${text}`} className={className}>
          {text}
        </SwitcherButton>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        className="min-w-44"
        onCloseAutoFocus={(e) => {
          if (keepFocus.current) e.preventDefault()
          keepFocus.current = false
        }}
      >
        <DropdownMenuRadioGroup value={value.range} onValueChange={(v) => onChange({ range: v as Preset })}>
          {PRESETS.filter((p) => p.id !== 'custom').map((p) => (
            <DropdownMenuRadioItem key={p.id} value={p.id}>
              {p.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onSelect={() => {
            keepFocus.current = true
            setPicking(true)
          }}
        >
          Custom…
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )

  if (phone) {
    return (
      <>
        {menu}
        <Sheet open={picking} onOpenChange={setPicking}>
          <SheetContent side="bottom" className="items-center pb-6">
            <SheetHeader className="w-full">
              <SheetTitle>Custom range</SheetTitle>
              <SheetDescription>Up to {MAX_DAYS} days.</SheetDescription>
            </SheetHeader>
            {picker}
          </SheetContent>
        </Sheet>
      </>
    )
  }
  return (
    <Popover open={picking} onOpenChange={setPicking}>
      <PopoverAnchor asChild>
        <div className="min-w-0">{menu}</div>
      </PopoverAnchor>
      <PopoverContent align="end" className="w-auto p-0">
        {picker}
      </PopoverContent>
    </Popover>
  )
}

interface PickerProps {
  initial?: RangeValue
  today: string
  months: number
  onApply: (from: string, to: string) => void
}

function RangePicker({ initial, today, months, onApply }: PickerProps) {
  const [selected, setSelected] = useState<DateRange | undefined>(
    initial?.from && initial.to ? { from: toDate(initial.from), to: toDate(initial.to) } : undefined
  )
  const from = selected?.from ? toISO(selected.from) : undefined
  const to = selected?.to ? toISO(selected.to) : from
  const days = from && to ? daysBetween(from, to) : 0
  const tooLong = days > MAX_DAYS

  return (
    <div className="flex flex-col">
      <Calendar
        mode="range"
        numberOfMonths={months}
        selected={selected}
        onSelect={setSelected}
        defaultMonth={selected?.from ?? toDate(today)}
        disabled={{ after: toDate(today) }}
      />
      <div className="flex items-center justify-between gap-3 border-t p-3">
        <span className={`text-xs ${tooLong ? 'text-destructive' : 'text-muted-foreground'}`}>
          {!from ? 'Pick the first day' : tooLong ? `At most ${MAX_DAYS} days` : customLabel(from, to!)}
        </span>
        <Button size="sm" disabled={!from || tooLong} onClick={() => onApply(from!, to!)}>
          Apply
        </Button>
      </div>
    </div>
  )
}
