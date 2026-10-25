import { useEffect, useId, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { fieldNameProblem, pickerFields, toggleField } from '@/lib/forms'

interface PickerProps {
  /** Every field submissions have sent. */
  fields: string[]
  /** An approved form's expected fields: listed first, in order, and the rest marked "not kept". */
  kept?: string[]
  /** The fields chosen, in column order. */
  checked: string[]
  onChange: (next: string[]) => void
  disabled?: boolean
}

/**
 * One checkbox per field (D12): the kept ones first in their column order,
 * then every other field seen, each of those marked "not kept" when the
 * form has a kept list. A field checked again goes to the end. Below them,
 * a field nothing sent yet is added by name: it is checked, and saving the
 * pick records it on the form.
 */
export function FieldPicker({ fields, kept, checked, onChange, disabled }: PickerProps) {
  const id = useId()
  const [adding, setAdding] = useState('')
  const name = adding.trim()
  const problem = fieldNameProblem(name)
  const add = () => {
    if (name === '' || problem) return
    if (!checked.includes(name)) onChange([...checked, name])
    setAdding('')
  }
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <ul className="flex flex-col gap-2">
        {pickerFields(fields, kept, checked).map((f, i) => {
          const box = `${id}-${i}`
          return (
            <li key={f} className="flex min-w-0 items-center gap-2">
              <Checkbox id={box} checked={checked.includes(f)} disabled={disabled} onCheckedChange={() => onChange(toggleField(checked, f))} />
              <Label htmlFor={box} className="min-w-0 font-mono text-xs break-all">
                {f}
              </Label>
              {kept && !kept.includes(f) && (
                <Badge variant="outline" className="shrink-0 text-muted-foreground">
                  {fields.includes(f) ? 'not kept' : 'new'}
                </Badge>
              )}
            </li>
          )
        })}
      </ul>
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex min-w-0 gap-2">
          <Input
            aria-label="New field"
            value={adding}
            disabled={disabled}
            placeholder="New field name"
            aria-invalid={problem !== '' || undefined}
            className="h-8 min-w-0 font-mono text-xs"
            onChange={(e) => setAdding(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                add()
              }
            }}
          />
          <Button type="button" size="sm" variant="outline" disabled={disabled || name === '' || problem !== ''} onClick={add}>
            Add field
          </Button>
        </div>
        {problem && <p className="text-xs text-destructive">{problem}</p>}
      </div>
    </div>
  )
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  name: string
  /** Every field the draft's submissions sent: all preselected. */
  fields: string[]
  pending?: boolean
  /** Resolves true once approved, which closes the dialog. */
  onApprove: (fields: string[]) => Promise<boolean>
}

/** Approves a draft with the fields it keeps from now on, at least one (D12). */
export default function ApproveDialog({ open, onOpenChange, name, fields, pending, onApprove }: Props) {
  const [checked, setChecked] = useState(fields)
  // A fresh pick each time it opens, from the fields seen by then.
  useEffect(() => {
    if (open) setChecked(fields)
    // Only an opening resets the pick, not a refetch while it is open.
  }, [open])
  const approve = async () => {
    if (await onApprove(checked)) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="break-all">Approve {name}</DialogTitle>
          <DialogDescription>
            From now on its submissions keep only the fields checked, and each counts as a conversion. What arrived while it was a draft
            stays as it is.
          </DialogDescription>
        </DialogHeader>
        {fields.length === 0 && <p className="text-sm text-muted-foreground">No fields arrived yet; add the ones the form sends.</p>}
        <FieldPicker fields={fields} checked={checked} onChange={setChecked} disabled={pending} />
        <DialogFooter>
          <Button type="button" disabled={pending || checked.length === 0} onClick={() => void approve()}>
            Approve
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
