import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import type { ReceivedKey } from '@/lib/api'
import { receivedAttributesQuery } from '@/lib/queries'

const DAYS = [7, 14, 30] as const

/** The total in use if the dialog saved now: the server's, minus what it drops, plus what it adds. 0 max is no limit. */
export function budget(used: number, max: number, initial: string[], value: string[]) {
  const added = value.filter((k) => !initial.includes(k)).length
  const removed = initial.filter((k) => !value.includes(k)).length
  const next = used + added - removed
  return { used: next, over: max > 0 && added > 0 && next > max }
}

/** What a key received: its events and its most distinct values in a day, or "not received". `valuesCap` 0 is no cap. */
export function describeKey(r: ReceivedKey, valuesCap: number): string {
  if (r.events === 0) return 'not received'
  const values =
    r.max_values === null
      ? 'values counted tonight'
      : valuesCap > 0 && r.max_values > valuesCap
        ? `${r.max_values.toLocaleString()} values · folds past ${valuesCap}`
        : `${r.max_values.toLocaleString()} ${r.max_values === 1 ? 'value' : 'values'}`
  return `${r.events.toLocaleString()} events · ${values}`
}

function dayString(d: Date) {
  return d.toISOString().slice(0, 10)
}

/** Breakdowns: pick from the keys the project received (7/14/30 days), plus a key not received yet. */
export default function BreakdownsField({ projectId, initial, value, onChange, onProblem }: {
  projectId?: number
  /** The keys the project declares now (what Save would replace). */
  initial: string[]
  value: string[]
  onChange: (next: string[]) => void
  /** Null when the selection fits the budget, else why Save is disabled. */
  onProblem: (problem: string | null) => void
}) {
  const [days, setDays] = useState<(typeof DAYS)[number]>(30)
  const [text, setText] = useState('')
  const [refused, setRefused] = useState<string | null>(null)
  const to = new Date()
  const from = new Date(to.getTime() - (days - 1) * 86_400_000)
  const { data } = useQuery({
    ...receivedAttributesQuery({ project_id: projectId, from: dayString(from), to: dayString(to) }),
    placeholderData: keepPreviousData,
  })
  const listed: ReceivedKey[] = [...(data?.keys ?? [])]
  for (const k of value) if (!listed.some((r) => r.key === k)) listed.push({ key: k, events: 0, max_values: null, declared: false })
  const b = data ? budget(data.breakdowns_used, data.breakdowns_max, initial, value) : null
  const problem = b?.over ? `${b.used} of ${data!.breakdowns_max} breakdowns: over the limit (ATTRIBUTE_BREAKDOWNS_MAX)` : null
  useEffect(() => onProblem(problem), [problem])
  const toggle = (k: string, on: boolean) => onChange(on ? [...value, k] : value.filter((x) => x !== k))
  const add = () => {
    const k = text.trim()
    if (!k) return
    if (k.startsWith('$')) return setRefused('$ keys appear in the list once received')
    setRefused(null)
    setText('')
    if (!value.includes(k)) onChange([...value, k])
  }
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="flex w-full items-center justify-between gap-2 text-sm font-medium">
        <span>
          Breakdowns
          {b && <span className="font-normal text-muted-foreground"> · {data!.breakdowns_max > 0 ? `${b.used} of ${data!.breakdowns_max} in use` : `${b.used} in use`}</span>}
        </span>
        <ToggleGroup type="single" size="sm" value={String(days)} onValueChange={(v) => v && setDays(Number(v) as (typeof DAYS)[number])}>
          {DAYS.map((d) => <ToggleGroupItem key={d} value={String(d)}>{d} days</ToggleGroupItem>)}
        </ToggleGroup>
      </legend>
      {listed.length > 0 ? (
        <ul className="flex max-h-64 flex-col gap-1 overflow-y-auto rounded-md border p-2">
          {listed.map((r) => {
            const id = `breakdown-${r.key}`
            return (
              <li key={r.key} className="flex items-center gap-2">
                <Checkbox id={id} checked={value.includes(r.key)} onCheckedChange={(c) => toggle(r.key, c === true)} />
                <Label htmlFor={id} className="flex flex-1 items-baseline justify-between gap-2 font-normal">
                  <code className="text-sm">{r.key}</code>
                  <span className="text-xs text-muted-foreground">{describeKey(r, data?.values_cap ?? 0)}</span>
                </Label>
              </li>
            )
          })}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">{projectId ? 'No attributes received in this range.' : 'Nothing received yet: add keys by name, or pick them once events arrive.'}</p>
      )}
      <Input
        aria-label="Key not received yet"
        placeholder="Add a key not received yet"
        value={text}
        onChange={(e) => { setText(e.target.value); setRefused(null) }}
        onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); add() } }}
      />
      {refused && <p className="text-xs text-destructive">{refused}</p>}
      {problem && <p className="text-xs text-destructive">{problem}</p>}
    </fieldset>
  )
}
