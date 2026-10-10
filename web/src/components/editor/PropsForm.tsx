import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
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
    <div className="grid min-w-0 grid-cols-1 gap-1.5">
      <Label htmlFor={`prop-${name}`}>{name}</Label>
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
      {bad && <p className="text-xs text-destructive">not valid JSON</p>}
    </div>
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
    <div className="grid min-w-0 grid-cols-1 gap-1.5">
      <span className="text-sm leading-none font-medium">{field.key}</span>
      {names.length === 0 && <p className="text-xs text-muted-foreground">Run the query to list columns</p>}
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
  )
}

/** The props of one component as a form generated from its contract (spec D11), or as one JSON object. */
export default function PropsForm({ component, value, onChange, columns, onValidity }: FormProps) {
  const fields = fieldsOf(widgets[component]?.contract.props ?? {})
  const [raw, setRaw] = useState(false)
  const [rawText, setRawText] = useState('')
  const [rawBad, setRawBad] = useState(false)
  const [badKeys, setBadKeys] = useState<ReadonlySet<string>>(new Set())

  const ok = raw ? !rawBad : badKeys.size === 0
  const report = useRef(onValidity)
  report.current = onValidity
  useEffect(() => report.current(ok), [ok])

  const markBad = (key: string) => (bad: boolean) =>
    setBadKeys((prev) => {
      if (prev.has(key) === bad) return prev
      const next = new Set(prev)
      if (bad) next.add(key)
      else next.delete(key)
      return next
    })

  const toggle = (
    <Button
      type="button"
      variant="link"
      size="sm"
      className="h-auto justify-self-start p-0"
      onClick={() => {
        setRawText(JSON.stringify(value, null, 2))
        setRawBad(false)
        setBadKeys(new Set())
        setRaw(!raw)
      }}
    >
      {raw ? 'Edit props as a form' : 'Edit props as JSON'}
    </Button>
  )

  if (raw) {
    return (
      <div className="grid min-w-0 grid-cols-1 gap-3">
        <div className="grid min-w-0 grid-cols-1 gap-1.5">
          <Label htmlFor="props-json">Props JSON</Label>
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
          {rawBad && <p className="text-xs text-destructive">not valid JSON (an object is expected)</p>}
        </div>
        {toggle}
      </div>
    )
  }

  return (
    <div className="grid min-w-0 grid-cols-1 gap-3">
      {fields.length === 0 && <p className="text-xs text-muted-foreground">No props</p>}
      {fields.map((f) => {
        const set = (v: unknown) => onChange(put(value, f.key, v))
        switch (f.kind) {
          case 'enum':
            return (
              <div key={f.key} className="grid min-w-0 grid-cols-1 gap-1.5">
                <Label htmlFor={`prop-${f.key}`}>{f.key}</Label>
                <Default
                  id={`prop-${f.key}`}
                  aria-label={f.key}
                  options={f.options}
                  value={typeof value[f.key] === 'string' ? (value[f.key] as string) : ''}
                  onChange={(e) => set(e.target.value || undefined)}
                />
              </div>
            )
          case 'boolean':
            return (
              <div key={f.key} className="flex min-w-0 items-center gap-2">
                <Switch
                  id={`prop-${f.key}`}
                  aria-label={f.key}
                  checked={value[f.key] === true}
                  onCheckedChange={(on) => set(on ? true : undefined)}
                />
                <Label htmlFor={`prop-${f.key}`}>{f.key}</Label>
              </div>
            )
          case 'enum-map':
            return <EnumMap key={f.key} field={f} value={value[f.key]} columns={columns} onValue={set} />
          case 'json':
            return <JsonField key={f.key} name={f.key} value={value[f.key]} onValue={set} onBad={markBad(f.key)} />
        }
      })}
      {toggle}
    </div>
  )
}
