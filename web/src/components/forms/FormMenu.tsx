import { useState } from 'react'
import { MoreHorizontalIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import type { Form } from '@/lib/api'
import { isClosed } from '@/lib/forms'
import StopDialog from './StopDialog'

interface Props {
  form: Form
  pending: boolean
  /** On the form's page: **Settings…** first. */
  onSettings?: () => void
  onApprove: () => void
  /** Sends update_form's closes_at: an RFC 3339 time, or null to reopen; resolves true once saved. */
  onClosing: (closesAt: string | null) => Promise<boolean>
  onArchive: () => void
}

/**
 * A form's menu (D12), on its row and its page (which adds **Settings…**
 * first): **Approve…** on a draft,
 * **Stop accepting…** while it takes submissions, **Reopen** once it has a
 * closing time, and **Archive form**.
 */
export default function FormMenu({ form, pending, onSettings, onApprove, onClosing, onArchive }: Props) {
  const [stopping, setStopping] = useState(false)
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="size-8 shrink-0" aria-label={`Actions for ${form.name}`}>
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {onSettings && <DropdownMenuItem onClick={onSettings}>Settings…</DropdownMenuItem>}
          {form.status === 'draft' && (
            <DropdownMenuItem disabled={pending} onClick={onApprove}>
              Approve…
            </DropdownMenuItem>
          )}
          {!isClosed(form) && (
            <DropdownMenuItem disabled={pending} onClick={() => setStopping(true)}>
              Stop accepting…
            </DropdownMenuItem>
          )}
          {form.closes_at !== undefined && (
            <DropdownMenuItem disabled={pending} onClick={() => void onClosing(null)}>
              Reopen
            </DropdownMenuItem>
          )}
          <DropdownMenuItem disabled={pending} onClick={onArchive}>
            Archive form
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <StopDialog
        open={stopping}
        onOpenChange={setStopping}
        name={form.name}
        closesAt={form.closes_at}
        pending={pending}
        onStop={(closesAt) => onClosing(closesAt)}
      />
    </>
  )
}
