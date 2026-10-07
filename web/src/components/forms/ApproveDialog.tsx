import { useEffect, useId, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { pickerFields, toggleField } from '@/lib/forms'

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
 * form has a kept list. A field checked again goes to the end.
 */
export function FieldPicker({ fields, kept, checked, onChange, disabled }: PickerProps) {
  const id = useId()
  return (
    <ul className="flex flex-col gap-2">
      {pickerFields(fields, kept).map((f, i) => {
        const box = `${id}-${i}`
        return (
          <li key={f} className="flex min-w-0 items-center gap-2">
            <Checkbox id={box} checked={checked.includes(f)} disabled={disabled} onCheckedChange={() => onChange(toggleField(checked, f))} />
            <Label htmlFor={box} className="min-w-0 font-mono text-xs break-all">
              {f}
            </Label>
            {kept && !kept.includes(f) && (
              <Badge variant="outline" className="shrink-0 text-muted-foreground">
                not kept
              </Badge>
            )}
          </li>
        )
      })}
    </ul>
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
        {fields.length === 0 ? (
          <p className="text-sm text-muted-foreground">No fields arrived yet.</p>
        ) : (
          <FieldPicker fields={fields} checked={checked} onChange={setChecked} />
        )}
        <DialogFooter>
          <Button type="button" disabled={pending || checked.length === 0} onClick={() => void approve()}>
            Approve
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
