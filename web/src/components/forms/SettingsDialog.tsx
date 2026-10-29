import { useEffect, useId, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { Form, FormUpdate } from '@/lib/api'
import { FieldPicker } from './ApproveDialog'
import ClosingControl from './ClosingControl'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  form: Form
  pending?: boolean
  /** Sends update_form with what changed; resolves true once saved, which closes the dialog. */
  onSave: (body: FormUpdate) => Promise<boolean>
  /** Sends update_form's closes_at at once, apart from Save. */
  onClosing: (closesAt: string | null) => Promise<unknown>
}

/**
 * A form's settings (D12): purpose, return URL and an approved form's
 * expected fields, saved together with only what changed sent; and its
 * closing, whose buttons apply at once.
 */
export default function SettingsDialog({ open, onOpenChange, form, pending, onSave, onClosing }: Props) {
  const id = useId()
  const expected = form.expected_fields ?? []
  const approved = form.status === 'approved'
  const [purpose, setPurpose] = useState(form.purpose)
  const [returnURL, setReturnURL] = useState(form.return_url)
  const [checked, setChecked] = useState(expected)
  // Each opening starts from what is saved, not a refetch while it is open.
  useEffect(() => {
    if (!open) return
    setPurpose(form.purpose)
    setReturnURL(form.return_url)
    setChecked(expected)
  }, [open])

  const body: FormUpdate = {}
  if (purpose.trim() !== form.purpose) body.purpose = purpose.trim()
  if (returnURL.trim() !== form.return_url) body.return_url = returnURL.trim()
  if (approved && JSON.stringify(checked) !== JSON.stringify(expected)) body.expected_fields = checked
  const changed = Object.keys(body).length > 0
  const savable = !pending && changed && (!approved || checked.length > 0)

  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (savable && (await onSave(body))) onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Settings</DialogTitle>
          <DialogDescription className="font-mono break-all">{form.name}</DialogDescription>
        </DialogHeader>
        <form id={`${id}-form`} onSubmit={(e) => void save(e)} className="flex min-w-0 flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-purpose`}>Purpose</Label>
            <Input id={`${id}-purpose`} value={purpose} onChange={(e) => setPurpose(e.target.value)} placeholder="What the form is for" />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-return`}>Return URL</Label>
            <Input
              id={`${id}-return`}
              type="url"
              value={returnURL}
              onChange={(e) => setReturnURL(e.target.value)}
              placeholder="https://example.com/thanks"
            />
            <p className="text-xs text-muted-foreground">
              Where a plain HTML form sends the visitor back when it sets no <code className="font-mono">$redirect</code>; its origin must
              be one of the project's allowed origins.
            </p>
          </div>
          {approved && (
            <div role="group" aria-labelledby={`${id}-fields`} className="flex min-w-0 flex-col gap-2 border-t pt-4">
              <h3 id={`${id}-fields`} className="text-sm font-medium">
                Expected fields
              </h3>
              <p className="text-xs text-muted-foreground">
                New submissions keep only these, as the table's columns in this order. A field the form starts sending appears here
                unchecked; add one by name to keep it from its first submission.
              </p>
              <FieldPicker fields={form.fields} kept={expected} checked={checked} onChange={setChecked} disabled={pending} />
            </div>
          )}
        </form>
        {/* Outside the form: Enter in its date field must not save the rest. */}
        <div className="border-t pt-4">
          <ClosingControl closesAt={form.closes_at} pending={pending} onChange={onClosing} />
        </div>
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="outline">
              Cancel
            </Button>
          </DialogClose>
          <Button type="submit" form={`${id}-form`} disabled={!savable}>
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
