import { useEffect, useState, type FormEvent, type ReactElement, type ReactNode } from 'react'
import { ChevronLeftIcon, ChevronRightIcon, PlusIcon, XIcon } from 'lucide-react'
import { cn } from 'cn'
import { Button } from '@/components/ui/button'
import { Combobox, ComboboxEmpty, ComboboxInput, ComboboxItem, ComboboxList } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { OPS, type Filter, type FilterOp, type TableView } from '@/lib/table-view'

/** One value a column holds, with how many rows hold it; `capped` when the list was cut short. */
export interface ValueOption {
  value: string
  rows: number
  capped: boolean
}

export type OptionLoader = (column: string, filters: Filter[]) => Promise<ValueOption[]>

const OP_LABELS: Record<FilterOp, string> = { '=': '=', '!=': '≠', '<': '<', '>': '>', in: 'in', 'not in': 'not in' }

const isList = (op: FilterOp) => op === 'in' || op === 'not in'
const count = (n: number) => n.toLocaleString('en-US')

function chipText(f: Filter): string {
  const vs = Array.isArray(f.value) ? f.value : [f.value]
  const more = vs.length > 3 ? ` +${vs.length - 3}` : ''
  return `${f.column} ${OP_LABELS[f.op]} ${vs.slice(0, 3).join(', ')}${more}`
}

interface FilterBarProps {
  columns: string[]
  numeric: Set<string>
  view: TableView
  onView: (v: TableView) => void
  options: OptionLoader
  /** The server refused this view: shown under the bar, its chips marked until edited or removed. */
  error?: string
}

/** The view's filters as chips, each editable in a popover, above the table. */
export function FilterBar({ columns, numeric, view, onView, options, error }: FilterBarProps) {
  // Any change of filters returns to the first page.
  const setFilters = (filters: Filter[]) => onView({ ...view, filters, offset: 0 })
  const editor = (initial: Filter | undefined, index: number) => (close: () => void) => (
    <FilterEditor
      columns={columns}
      numeric={numeric}
      initial={initial}
      others={view.filters.filter((_, j) => j !== index)}
      options={options}
      onCancel={close}
      onApply={(f) => {
        setFilters(index < 0 ? [...view.filters, f] : view.filters.map((g, j) => (j === index ? f : g)))
        close()
      }}
    />
  )
  return (
    // Its own provider, so a table outside the app's (a test, the gallery) still gets tooltips.
    <TooltipProvider>
      <div className="flex shrink-0 flex-col gap-1 pb-1.5">
        <div className="flex flex-wrap items-center gap-1.5">
          {view.filters.map((f, i) => (
            <Chip
              key={i}
              text={chipText(f)}
              stale={!columns.includes(f.column)}
              // Any edit is a new view, which clears the refusal and with it the mark.
              invalid={error !== undefined && columns.includes(f.column)}
              editor={editor(f, i)}
              onRemove={() => setFilters(view.filters.filter((_, j) => j !== i))}
            />
          ))}
          <EditPopover
            trigger={
              <Button variant="ghost" size="sm" className="h-7 px-2 text-xs text-muted-foreground">
                <PlusIcon />
                Filter
              </Button>
            }
          >
            {editor(undefined, -1)}
          </EditPopover>
          {view.filters.length >= 2 && (
            <Button variant="ghost" size="sm" className="h-7 px-2 text-xs text-muted-foreground" onClick={() => setFilters([])}>
              Clear all
            </Button>
          )}
        </div>
        {error && <p className="text-xs text-destructive">{error}</p>}
      </div>
    </TooltipProvider>
  )
}

function EditPopover({ trigger, children }: { trigger: ReactElement; children: (close: () => void) => ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-3">
        {children(() => setOpen(false))}
      </PopoverContent>
    </Popover>
  )
}

interface ChipProps {
  text: string
  stale: boolean
  invalid: boolean
  editor: (close: () => void) => ReactNode
  onRemove: () => void
}

function Chip({ text, stale, invalid, editor, onRemove }: ChipProps) {
  const chip = (
    <span
      data-stale={stale || undefined}
      className={cn(
        'inline-flex h-7 max-w-full items-center rounded-md border bg-muted/40 text-xs',
        stale && 'border-dashed text-muted-foreground opacity-70',
        invalid && 'border-destructive'
      )}
    >
      <EditPopover
        trigger={
          <button
            type="button"
            aria-invalid={invalid || undefined}
            className="min-w-0 truncate rounded-l-md py-1 pl-2 pr-1 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          >
            {text}
          </button>
        }
      >
        {editor}
      </EditPopover>
      <button
        type="button"
        aria-label={`Remove filter ${text}`}
        onClick={onRemove}
        className="flex h-full items-center rounded-r-md px-1 text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
      >
        <XIcon aria-hidden className="size-3" />
      </button>
    </span>
  )
  if (!stale) return chip
  return (
    <Tooltip>
      <TooltipTrigger asChild>{chip}</TooltipTrigger>
      <TooltipContent>not in this table</TooltipContent>
    </Tooltip>
  )
}

interface EditorProps {
  columns: string[]
  numeric: Set<string>
  initial?: Filter
  /** The view's other filters: the value picker offers what this filter would add to them. */
  others: Filter[]
  options: OptionLoader
  onApply: (f: Filter) => void
  onCancel: () => void
}

function FilterEditor({ columns, numeric, initial, others, options, onApply, onCancel }: EditorProps) {
  const defaultOp = (c: string): FilterOp => (numeric.has(c) ? '>' : 'in')
  const [column, setColumn] = useState(initial?.column ?? columns[0])
  const [op, setOp] = useState<FilterOp>(initial?.op ?? defaultOp(column))
  const [list, setList] = useState<string[]>(Array.isArray(initial?.value) ? initial.value : [])
  const [text, setText] = useState(typeof initial?.value === 'string' ? initial.value : '')
  const [loaded, setLoaded] = useState<{ column: string; items: ValueOption[] | null } | null>(null)
  const ordered = op === '<' || op === '>'

  useEffect(() => {
    if (ordered) return
    let live = true
    options(column, others).then(
      (items) => live && setLoaded({ column, items }),
      () => live && setLoaded({ column, items: null })
    )
    return () => {
      live = false
    }
    // The other filters cannot change while the editor is open; a new array each render must not refetch.
  }, [column, ordered])

  const changeColumn = (next: string) => {
    if (op === defaultOp(column)) setOp(defaultOp(next))
    setColumn(next)
    setList([])
    setText('')
  }
  // Moving between a list and a single value keeps what was picked.
  const changeOp = (next: FilterOp) => {
    if (isList(next) && !isList(op) && text !== '') setList([text])
    if (!isList(next) && isList(op) && list.length > 0) setText(list[0])
    setOp(next)
  }

  const current = loaded?.column === column ? loaded : null
  // An empty cell never matches =, in, < or >, and every row matches != '', so it is never offered.
  const items = (current?.items ?? []).filter((o) => o.value !== '')
  const rows = new Map(items.map((o) => [o.value, o.rows]))
  const values = [...items.map((o) => o.value), ...list.filter((v) => !rows.has(v))]
  const capped = items.some((o) => o.capped)
  const value = isList(op) ? list : text
  const ready = isList(op) ? list.length > 0 : text !== ''

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (ready) onApply({ column, op, value })
  }

  const picker = (
    <ComboboxList>
      {(v: string) => (
        <ComboboxItem key={v} value={v}>
          <span className="min-w-0 truncate">{v}</span>
          {rows.has(v) && <span className="ml-auto text-xs text-muted-foreground tabular-nums">{count(rows.get(v)!)}</span>}
        </ComboboxItem>
      )}
    </ComboboxList>
  )
  const notes = (
    <>
      <ComboboxEmpty className="flex empty:hidden">
        {current ? (current.items ? 'No values' : "Couldn't load values") : 'Loading values…'}
      </ComboboxEmpty>
      {capped && <p className="px-2 py-1 text-xs text-muted-foreground">Showing the most frequent values</p>}
    </>
  )

  return (
    <form onSubmit={submit} className="flex flex-col gap-2">
      <div className="flex gap-2">
        <Select value={column} onValueChange={changeColumn}>
          <SelectTrigger aria-label="Column" size="sm" className="min-w-0 flex-1">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(columns.includes(column) ? columns : [...columns, column]).map((c) => (
              <SelectItem key={c} value={c}>
                {c}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={op} onValueChange={(v) => changeOp(v as FilterOp)}>
          <SelectTrigger aria-label="Operator" size="sm" className="w-24">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {OPS.map((o) => (
              <SelectItem key={o} value={o}>
                {OP_LABELS[o]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {ordered ? (
        <Input
          aria-label="Value"
          className="h-8"
          inputMode={numeric.has(column) ? 'decimal' : undefined}
          value={text}
          onChange={(e) => setText(e.target.value)}
          autoFocus
        />
      ) : isList(op) ? (
        // Inline, not in a popup of its own: a second portal inside the popover would count as outside it.
        <Combobox items={values} multiple value={list} onValueChange={setList} inline open>
          <ComboboxInput aria-label="Search values" placeholder="Search values" showTrigger={false} className="h-8" />
          <div className="max-h-56 overflow-y-auto">{picker}</div>
          {notes}
        </Combobox>
      ) : (
        <Combobox
          items={values}
          value={values.includes(text) ? text : null}
          onValueChange={(v: string | null) => v !== null && setText(v)}
          inputValue={text}
          onInputValueChange={setText}
          inline
          open
        >
          <ComboboxInput aria-label="Value" placeholder="Type or pick a value" showTrigger={false} className="h-8" />
          <div className="max-h-56 overflow-y-auto">{picker}</div>
          {notes}
        </Combobox>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={!ready}>
          Apply
        </Button>
      </div>
    </form>
  )
}

interface PageFooterProps {
  offset: number
  limit: number
  matched: number
  onOffset: (o: number) => void
  note?: string
}

/** `1–1,000 of 5,335` with previous and next, shown only when there is more than one page or a note. */
export function PageFooter({ offset, limit, matched, onOffset, note }: PageFooterProps) {
  const paged = matched > limit
  if (!paged && !note) return null
  return (
    <div className="flex shrink-0 items-center gap-2 border-t pt-1 text-xs text-muted-foreground">
      {note && <p className="min-w-0 flex-1">{note}</p>}
      {paged && (
        <div className="ml-auto flex items-center gap-1">
          <span className="tabular-nums">{`${count(offset + 1)}–${count(Math.min(offset + limit, matched))} of ${count(matched)}`}</span>
          <Button
            variant="ghost"
            size="icon"
            className="size-6"
            aria-label="Previous page"
            disabled={offset === 0}
            onClick={() => onOffset(Math.max(0, offset - limit))}
          >
            <ChevronLeftIcon />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="size-6"
            aria-label="Next page"
            disabled={offset + limit >= matched}
            onClick={() => onOffset(offset + limit)}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      )}
    </div>
  )
}
