import type { ReactNode } from 'react'
import { Label } from '@/components/ui/label'

/** The attributes that tie a control to its error line: invalid while there is one. */
export function errorProps(id: string, error: string | undefined) {
  return error ? { 'aria-invalid': true, 'aria-describedby': `${id}-error` } : {}
}

/** One labelled input of the editor: the label, the control, a hint under it, and its error under that, which breaks anywhere (a refusal naming a long snake_case event) rather than widen the form. */
export function Field({ id, label, hint, error, children }: { id: string; label: string; hint?: string; error?: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      {error && (
        <p id={`${id}-error`} className="text-sm [overflow-wrap:anywhere] text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}
