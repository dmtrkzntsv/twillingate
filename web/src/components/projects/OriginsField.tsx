import { PlusIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

/** Allowed origins, one row each: edit in place, × removes, Add origin appends an empty row. */
export default function OriginsField({ value, onChange }: { value: string[]; onChange: (next: string[]) => void }) {
  const set = (i: number, v: string) => onChange(value.map((o, j) => (j === i ? v : o)))
  return (
    <fieldset className="flex flex-col gap-1.5">
      <legend className="text-sm font-medium">Allowed origins</legend>
      {value.map((o, i) => (
        <div key={i} className="flex items-center gap-1.5">
          <Input aria-label={`Origin ${i + 1}`} value={o} placeholder="https://example.com or *" onChange={(e) => set(i, e.target.value)} />
          <Button type="button" variant="ghost" size="icon" aria-label={`Remove origin ${i + 1}`} onClick={() => onChange(value.filter((_, j) => j !== i))}>
            <XIcon />
          </Button>
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => onChange([...value, ''])}>
        <PlusIcon /> Add origin
      </Button>
      <p className="text-xs text-muted-foreground"><code>*</code> allows any origin. With none, browsers can't send; native apps still can.</p>
    </fieldset>
  )
}
