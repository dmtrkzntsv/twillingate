import { useEffect, useId, useImperativeHandle, useRef, useState, type FormEvent, type Ref, type RefObject } from 'react'
import { XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { Monitor } from '@/lib/api'

/** The expectations a monitor can have beyond a 2xx answer: its fields that hold one. */
export type ExpectKey = 'contains' | 'redirect_to'

/**
 * One expectation as Settings offers it: an item in the row's menu that opens
 * its field, the saved value as a line under the origin with an X that removes
 * it, and the field that edits it. A new expectation is an entry in
 * EXPECT_OPTIONS (and its field in Monitor and MonitorBody).
 */
export interface ExpectOption {
  key: ExpectKey
  /** The row menu's item that opens the field. */
  menuLabel: string
  /** The field's label. */
  label: string
  /** The saved value as the row says it. */
  savedText: (value: string) => string
  /** The name of the X that removes it. */
  removeLabel: string
  editor:
    | { kind: 'text'; maxLength: number }
    /** One of the choices, which the row gives; none offered means no menu item. */
    | { kind: 'choice'; prompt: string; choices: (row: { others: string[] }) => string[] }
}

export const EXPECT_OPTIONS: readonly ExpectOption[] = [
  {
    key: 'contains',
    menuLabel: 'Look for text on the page',
    label: 'Page must contain',
    savedText: (v) => `must contain "${v}"`,
    removeLabel: 'Remove the page text',
    editor: { kind: 'text', maxLength: 200 },
  },
  {
    key: 'redirect_to',
    menuLabel: 'Expect a redirect',
    label: 'Must redirect to',
    savedText: (v) => `must redirect to ${v}`,
    removeLabel: 'Remove the redirect',
    editor: { kind: 'choice', prompt: 'Choose an origin', choices: ({ others }) => others },
  },
]

/** The choices an option's field offers on a row: its own, and the saved value even when no longer among them. */
export function choicesOf(option: ExpectOption, m: Monitor, others: string[]): string[] {
  if (option.editor.kind !== 'choice') return []
  const choices = option.editor.choices({ others })
  const saved = m[option.key]
  return saved !== '' && !choices.includes(saved) ? [...choices, saved] : choices
}

export interface ExpectFieldHandle {
  /** Opens the field, as the menu's item asks. */
  open: () => void
  /** Focuses the open field's control. */
  focus: () => void
}

interface Props {
  option: ExpectOption
  /** The value the server has; empty means none. */
  saved: string
  choices: string[]
  pending?: boolean
  /** Saves a value, "" to remove it; resolves true when saved. */
  onSave: (value: string) => Promise<boolean>
  /** Where focus goes when the field closes with no saved line to take it. */
  menuRef: RefObject<HTMLButtonElement | null>
  ref?: Ref<ExpectFieldHandle>
}

/**
 * One expectation of a row, in place under the origin: the saved line and its
 * X, or the open field. The field opens from the menu or the saved line and
 * stays open until saved or cancelled. While untouched it follows the server
 * (polling, window focus, another row's save); an edit stays through a refetch
 * and a refused save.
 */
export default function ExpectField({ option, saved, choices, pending, onSave, menuRef, ref }: Props) {
  const [value, setValue] = useState(saved)
  const [editing, setEditing] = useState(false)
  const id = useId()
  // The value the server had when the field last followed it.
  const followed = useRef(saved)
  // Closing the field hands focus back, once it is gone.
  const closing = useRef(false)
  const savedRef = useRef<HTMLButtonElement>(null)
  const controlRef = useRef<HTMLInputElement & HTMLSelectElement>(null)

  useImperativeHandle(ref, () => ({ open: () => setEditing(true), focus: () => controlRef.current?.focus() }), [])

  // Start from what is saved, again after each save; a real edit stays.
  useEffect(() => {
    const was = followed.current
    followed.current = saved
    setValue((v) => (v === was ? saved : v))
  }, [saved])

  const text = option.editor.kind === 'text'
  const next = text ? value.trim() : value
  const edited = next !== saved
  const showField = editing || value !== saved

  useEffect(() => {
    if (!closing.current || showField) return
    closing.current = false
    ;(savedRef.current ?? menuRef.current)?.focus()
  }, [showField, menuRef])

  const save = (e: FormEvent) => {
    e.preventDefault()
    if (!edited) return // Enter with nothing to save
    void onSave(next).then((ok) => {
      if (!ok) return
      // What was saved is what the field follows from here on.
      followed.current = next
      setEditing(false)
      setValue(next)
    })
  }
  const cancel = () => {
    closing.current = true
    setValue(saved)
    setEditing(false)
  }
  const remove = () => {
    void onSave('').then((ok) => {
      if (ok) menuRef.current?.focus()
    })
  }
  const escape = (e: React.KeyboardEvent) => {
    if (e.key !== 'Escape') return
    e.preventDefault()
    cancel()
  }

  if (!showField) {
    if (saved === '') return null
    const words = option.savedText(saved)
    return (
      <div className="flex min-w-0 items-center gap-1">
        <button
          type="button"
          ref={savedRef}
          className="w-fit min-w-0 text-left text-sm break-words text-muted-foreground underline-offset-2 hover:underline"
          disabled={pending}
          onClick={() => setEditing(true)}
        >
          {words}
        </button>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button type="button" size="icon-xs" variant="ghost" className="shrink-0 text-muted-foreground" aria-label={option.removeLabel} disabled={pending} onClick={remove}>
              <XIcon />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{option.removeLabel}</TooltipContent>
        </Tooltip>
      </div>
    )
  }

  // Mounted by the menu or the saved line, the only time the field is new.
  const autoFocus = editing && value === saved
  return (
    <form onSubmit={save} className="my-1 flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{option.label}</Label>
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        {option.editor.kind === 'text' ? (
          <Input
            id={id}
            ref={controlRef}
            className="min-w-0 flex-1"
            value={value}
            disabled={pending}
            maxLength={option.editor.maxLength}
            autoFocus={autoFocus}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={escape}
          />
        ) : (
          <div className="max-w-full min-w-0">
            <NativeSelect id={id} ref={controlRef} className="max-w-full" value={value} disabled={pending} autoFocus={autoFocus} onChange={(e) => setValue(e.target.value)} onKeyDown={escape}>
              {saved === '' && <NativeSelectOption value="" disabled>{option.editor.prompt}</NativeSelectOption>}
              {choices.map((o) => <NativeSelectOption key={o} value={o}>{o}</NativeSelectOption>)}
            </NativeSelect>
          </div>
        )}
        {edited && <Button type="submit" size="sm" disabled={pending}>Save</Button>}
        <Button type="button" size="sm" variant="ghost" onClick={cancel}>Cancel</Button>
      </div>
    </form>
  )
}
