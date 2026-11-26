import { useEffect, useId, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { ListFilterIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { PHONE, useMediaQuery } from '@/hooks/use-media-query'
import { endpoints, type Selector } from '@/lib/api'
import { formatValue } from '@/lib/format'
import { selectionLabel, valueLabel } from '@/lib/selectors'
import SwitcherButton from './SwitcherButton'

interface Props {
  dashboardId: number
  selector: Selector
  /** The values picked; none is All. */
  value: string[]
  /** The projects whose values are offered (selectorProjects); none loads nothing. */
  projectIds: number[]
  /** The range the values are counted over, YYYY-MM-DD. */
  from: string
  to: string
  onChange: (values: string[]) => void
  className?: string
}

/** Past this many values the checklist offers a search box. */
const SEARCH_PAST = 10

/** How long typing pauses before the search goes to the server. */
const SEARCH_DELAY_MS = 250

/**
 * Picks values of one of the dashboard's selectors: a button reading
 * "Deployment: All", "Deployment: cloud" or "Deployment: 2 selected" that
 * opens a checklist of the values with their counts, Clear at the top and
 * a search box past 10 values; a popover, a full-width bottom sheet on a
 * phone.
 */
export default function SelectorSwitcher({ className, ...props }: Props) {
  const phone = useMediaQuery(PHONE)
  const [open, setOpen] = useState(false)
  const text = selectionLabel(props.selector.label, props.value)
  const trigger = (
    <SwitcherButton icon={<ListFilterIcon />} aria-label={text} className={className} onClick={phone ? () => setOpen(true) : undefined}>
      {text}
    </SwitcherButton>
  )
  // The checklist mounts only while open, so the values load only then and
  // its search starts empty each time.
  if (phone) {
    return (
      <>
        {trigger}
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="bottom" className="max-h-[85vh] gap-0 pb-4">
            <SheetHeader className="pb-2">
              <SheetTitle className="truncate">{props.selector.label}</SheetTitle>
              <SheetDescription>Narrows the widgets that follow it. None picked is All.</SheetDescription>
            </SheetHeader>
            {open && <Checklist {...props} />}
          </SheetContent>
        </Sheet>
      </>
    )
  }
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent align="end" className="w-72 p-0" aria-label={props.selector.label}>
        {open && <Checklist {...props} />}
      </PopoverContent>
    </Popover>
  )
}

/** `value` once it has held still for `ms`. */
function useSettled(value: string, ms: number): string {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return settled
}

function Checklist({ dashboardId, selector, value, projectIds, from, to, onChange }: Omit<Props, 'className'>) {
  const id = useId()
  const [search, setSearch] = useState('')
  const settled = useSettled(search.trim(), SEARCH_DELAY_MS)
  const values = useQuery({
    queryKey: ['selector-values', dashboardId, selector.name, projectIds, from, to, settled],
    queryFn: () =>
      endpoints.selectorValues(dashboardId, selector.name, {
        project_id: projectIds,
        from,
        to,
        ...(settled ? { search: settled } : {}),
      }),
    enabled: projectIds.length > 0,
    // The list stays on screen while a search reloads it; a pick does not (picks are listed below).
    placeholderData: keepPreviousData,
  })
  // Once the box shows it stays, so a search that narrows the list to a
  // few values does not take the box away mid-typing.
  const [searchable, setSearchable] = useState(false)
  if (!searchable && (values.data?.values.length ?? 0) > SEARCH_PAST) setSearchable(true)

  // A value picked but not in the answer (still loading, or failed) stays listed, so it can be unticked.
  const listed: { value: string; count?: number }[] = values.data?.values ?? []
  const rows: { value: string; count?: number }[] = [...listed, ...value.filter((v) => !listed.some((l) => l.value === v)).map((v) => ({ value: v }))]
  const toggle = (v: string, on: boolean) => onChange(on ? [...value.filter((x) => x !== v), v] : value.filter((x) => x !== v))

  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex items-center justify-between gap-2 border-b px-3 py-2">
        <span className="min-w-0 truncate text-sm font-medium">{selectionLabel(selector.label, value)}</span>
        <Button variant="ghost" size="sm" className="h-7 shrink-0 px-2" disabled={value.length === 0} onClick={() => onChange([])}>
          Clear
        </Button>
      </div>
      {searchable && (
        <div className="border-b p-2">
          <Input
            type="search"
            aria-label={`Search ${selector.label}`}
            placeholder="Search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8"
          />
        </div>
      )}
      {rows.length > 0 && (
        <ul className="max-h-72 min-w-0 overflow-y-auto p-1">
          {rows.map((row, i) => (
            <li key={row.value} className="min-w-0">
              {/* The whole row is the checkbox's label, so all of it that highlights also ticks; the
                  checkbox is named by the value alone, not the count beside it. */}
              <label htmlFor={`${id}-${i}`} className="flex min-w-0 items-center gap-2 rounded-sm px-2 py-1.5 hover:bg-accent">
                <Checkbox
                  id={`${id}-${i}`}
                  aria-labelledby={`${id}-${i}-value`}
                  checked={value.includes(row.value)}
                  onCheckedChange={(c) => toggle(row.value, c === true)}
                />
                <span id={`${id}-${i}-value`} className="min-w-0 flex-1 truncate text-sm" title={row.value}>
                  {valueLabel(row.value)}
                </span>
                {row.count !== undefined && (
                  <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{formatValue(row.count)}</span>
                )}
              </label>
            </li>
          ))}
        </ul>
      )}
      {(rows.length === 0 || values.isError) && (
        <p className="px-3 py-2 text-sm text-muted-foreground">
          {projectIds.length === 0
            ? 'No project to read values from'
            : values.isError
              ? "Couldn't load the values"
              : values.isPending
                ? 'Loading values…'
                : settled
                  ? 'No value matches'
                  : 'No values in this range'}
        </p>
      )}
    </div>
  )
}
