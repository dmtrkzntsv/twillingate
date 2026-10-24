import { useEffect, useRef, useState } from 'react'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { widgets } from '@/components/widgets'
import { fieldsOf, type PropField } from '@/lib/props-form'

type Props = Record<string, unknown>

interface FormProps {
  component: string
  value: Props
  onChange(next: Props): void
  /** The columns of the last run, offered as names in a column → format map. */
  columns: string[]
  /** False while any field holds text that is not valid JSON; the editor disables Save on it. */
  onValidity(ok: boolean): void
  /** All props as one JSON object instead of a field each; the editor's Settings bar flips it. */
  raw?: boolean
  /** Enum options this server cannot serve, by prop key: left out of the list (unless chosen), with a line saying why. */
  unavailable?: Record<string, { options: string[]; note: string }>
  /** Shown, not edited: every control is disabled. */
  disabled?: boolean
}

/** `value` with `key` set in place (kept in order), or removed when `v` is undefined. */
function put(value: Props, key: string, v: unknown): Props {
  if (v !== undefined) return { ...value, [key]: v }
  const { [key]: _gone, ...rest } = value
  return rest
}

function parse(text: string): { ok: true; value: unknown } | { ok: false } {
  try {
    return { ok: true, value: JSON.parse(text) }
  } catch {
    return { ok: false }
  }
}

/** A prop key as a label: `bar_format` reads "Bar format". */
export function labelOf(key: string): string {
  const words = key.replace(/_/g, ' ')
  return words.charAt(0).toUpperCase() + words.slice(1)
}

/**
 * One line of the editor's settings: the label in the first column, the
 * control in the second (stacked on a phone). The editor lays these out in
 * a two-column grid that PropsForm's rows join.
 */
export function SettingRow({ label, htmlFor, children }: { label: React.ReactNode; htmlFor?: string; children: React.ReactNode }) {
  return (
    <div className="grid min-w-0 grid-cols-1 gap-1.5 sm:col-span-2 sm:grid-cols-subgrid">
      <Label htmlFor={htmlFor} className="text-muted-foreground sm:h-9">
        {label}
      </Label>
      <div className="min-w-0 sm:max-w-md [&_[data-slot=native-select-wrapper]]:w-full">{children}</div>
    </div>
  )
}

const isObject = (v: unknown): v is Props => typeof v === 'object' && v !== null && !Array.isArray(v)

function Default({ options, ...rest }: { options: string[] } & React.ComponentProps<typeof NativeSelect>) {
  return (
    <NativeSelect className="w-full" {...rest}>
      <option value="">default</option>
      {options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </NativeSelect>
  )
}

function JsonField({
  name,
  value,
  onValue,
  onBad,
}: {
  name: string
  value: unknown
  onValue(v: unknown): void
  onBad(bad: boolean): void
}) {
  const [text, setText] = useState(value === undefined ? '' : JSON.stringify(value))
  const [bad, setBad] = useState(false)
  // Leaving (another component) must not leave this key marked bad.
  const leave = useRef(onBad)
  leave.current = onBad
  useEffect(() => () => leave.current(false), [])
  return (
    <SettingRow label={labelOf(name)} htmlFor={`prop-${name}`}>
      <Textarea
        id={`prop-${name}`}
        aria-label={name}
        aria-invalid={bad}
        className="font-mono text-xs"
        value={text}
        onChange={(e) => {
          const next = e.target.value
          setText(next)
          if (next.trim() === '') {
            setBad(false)
            onBad(false)
            onValue(undefined)
            return
          }
          const r = parse(next)
          setBad(!r.ok)
          onBad(!r.ok)
          if (r.ok) onValue(r.value)
        }}
      />
      {bad && <p className="mt-1.5 text-xs text-destructive">not valid JSON</p>}
    </SettingRow>
  )
}

function EnumMap({
  field,
  value,
  columns,
  onValue,
}: {
  field: Extract<PropField, { kind: 'enum-map' }>
  value: unknown
  columns: string[]
  onValue(v: unknown): void
}) {
  const map = isObject(value) ? value : {}
  const names = [...new Set([...columns, ...Object.keys(map)])]
  return (
    <SettingRow label={labelOf(field.key)}>
      <div className="grid min-w-0 grid-cols-1 gap-2">
        {names.length === 0 && <p className="flex min-h-9 items-center text-sm text-muted-foreground">Run the query to list columns</p>}
        {names.map((col) => (
          <div key={col} className="grid min-w-0 grid-cols-[minmax(0,1fr)_minmax(0,1fr)] items-center gap-2">
            <span className="truncate text-sm" title={col}>
              {col}
            </span>
            <Default
              aria-label={`${field.key}: ${col}`}
              options={field.options}
              value={typeof map[col] === 'string' ? (map[col] as string) : ''}
              onChange={(e) => {
                const next = put(map, col, e.target.value || undefined)
                onValue(Object.keys(next).length === 0 ? undefined : next)
              }}
            />
          </div>
        ))}
      </div>
    </SettingRow>
  )
}

/** The props of one component as a form generated from its contract (spec D11), or as one JSON object. */
export default function PropsForm({ component, value, onChange, columns, onValidity, raw = false, unavailable = {}, disabled = false }: FormProps) {
  const fields = fieldsOf(widgets[component]?.contract.props ?? {})
  const [rawText, setRawText] = useState(() => JSON.stringify(value, null, 2))
  const [rawBad, setRawBad] = useState(false)
  const [badKeys, setBadKeys] = useState<ReadonlySet<string>>(new Set())
  // Each flip starts afresh: JSON from the props as they are, the form with nothing marked bad.
  const [shownRaw, setShownRaw] = useState(raw)
  if (raw !== shownRaw) {
    setShownRaw(raw)
    setRawText(JSON.stringify(value, null, 2))
    setRawBad(false)
    setBadKeys(new Set())
  }

  const ok = raw ? !rawBad : badKeys.size === 0
  const report = useRef(onValidity)
  report.current = onValidity
  useEffect(() => report.current(ok), [ok])
  // A form that is gone has nothing invalid left to block Save.
  useEffect(() => () => report.current(true), [])

  const markBad = (key: string) => (bad: boolean) =>
    setBadKeys((prev) => {
      if (prev.has(key) === bad) return prev
      const next = new Set(prev)
      if (bad) next.add(key)
      else next.delete(key)
      return next
    })

  if (raw) {
    return (
      <fieldset disabled={disabled} className="contents">
        <SettingRow label="Props JSON" htmlFor="props-json">
          <Textarea
            id="props-json"
            aria-label="Props JSON"
            aria-invalid={rawBad}
            className="font-mono text-xs"
            value={rawText}
            onChange={(e) => {
              const text = e.target.value
              setRawText(text)
              const r = text.trim() === '' ? { ok: true as const, value: {} } : parse(text)
              const good = r.ok && isObject(r.value)
              setRawBad(!good)
              if (r.ok && isObject(r.value)) onChange(r.value)
            }}
          />
          {rawBad && <p className="mt-1.5 text-xs text-destructive">not valid JSON (an object is expected)</p>}
        </SettingRow>
      </fieldset>
    )
  }

  return (
    <fieldset disabled={disabled} className="contents">
      {fields.length === 0 && (
        <SettingRow label="Props">
          <p className="flex min-h-9 items-center text-sm text-muted-foreground">This visualization has none</p>
        </SettingRow>
      )}
      {fields.map((f) => {
        // Keyed by component too: a switch in place starts every field afresh.
        const key = `${component}:${f.key}`
        const set = (v: unknown) => onChange(put(value, f.key, v))
        switch (f.kind) {
          case 'enum': {
            const chosen = typeof value[f.key] === 'string' ? (value[f.key] as string) : ''
            const cut = unavailable[f.key]
            const options = cut ? f.options.filter((o) => o === chosen || !cut.options.includes(o)) : f.options
            return (
              <SettingRow key={key} label={labelOf(f.key)} htmlFor={`prop-${f.key}`}>
                <Default
                  id={`prop-${f.key}`}
                  aria-label={f.key}
                  options={options}
                  value={chosen}
                  onChange={(e) => set(e.target.value || undefined)}
                />
                {cut && <p className="mt-1.5 text-xs text-muted-foreground">{cut.note}</p>}
              </SettingRow>
            )
          }
          case 'boolean':
            return (
              <SettingRow key={key} label={labelOf(f.key)} htmlFor={`prop-${f.key}`}>
                <div className="flex h-9 items-center">
                  <Switch
                    id={`prop-${f.key}`}
                    aria-label={f.key}
                    checked={value[f.key] === true}
                    onCheckedChange={(on) => set(on ? true : undefined)}
                  />
                </div>
              </SettingRow>
            )
          case 'enum-map':
            return <EnumMap key={key} field={f} value={value[f.key]} columns={columns} onValue={set} />
          case 'json':
            return <JsonField key={key} name={f.key} value={value[f.key]} onValue={set} onBad={markBad(f.key)} />
        }
      })}
    </fieldset>
  )
}
