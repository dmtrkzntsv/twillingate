import { XIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

interface Props {
  label: string
  value: string[]
  onChange: (next: string[]) => void
  placeholder?: string
}

/**
 * A list of strings as chips: Enter or a comma adds the trimmed text, never
 * twice; pasted text with commas becomes one chip per piece; × removes.
 */
export default function ChipsInput({ label, value, onChange, placeholder }: Props) {
  const id = useId()
  const [text, setText] = useState('')
  const add = (raw = text) => {
    const next = [...value]
    for (const piece of raw.split(',')) {
      const v = piece.trim()
      if (v && !next.includes(v)) next.push(v)
    }
    setText('')
    if (next.length > value.length) onChange(next)
  }
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex flex-wrap items-center gap-1.5 rounded-md border p-1.5">
        {value.map((v) => (
          <Badge key={v} variant="secondary" className="gap-1">
            {v}
            <button type="button" aria-label={`Remove ${v}`} onClick={() => onChange(value.filter((x) => x !== v))}>
              <XIcon className="size-3" />
            </button>
          </Badge>
        ))}
        <Input
          id={id}
          value={text}
          placeholder={placeholder}
          className="h-7 min-w-32 flex-1 border-0 shadow-none focus-visible:ring-0"
          onChange={(e) => {
            if (e.target.value.includes(',')) add(e.target.value)
            else setText(e.target.value)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add()
            }
          }}
          onBlur={() => add()}
        />
      </div>
    </div>
  )
}
