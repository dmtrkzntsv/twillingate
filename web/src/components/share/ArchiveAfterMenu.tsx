import { PencilIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import type { ArchiveAfter } from '@/lib/api'
import { cn } from '@/lib/utils'
import { ARCHIVE_AFTER } from '@/lib/share'

interface Props {
  /** When the share archives now: "Nov 4", or "Project lifetime". */
  current: string
  onChange: (value: ArchiveAfter) => void
  disabled?: boolean
  /** The compact trigger, for a line of small text (the Shares page's folded line). */
  small?: boolean
}

/**
 * A live share's archive date as quiet text with a pencil; it opens the
 * archive-after periods, counted from today. For a list, where a select per
 * row would be heavy.
 */
export function ArchiveAfterMenu({ current, onChange, disabled, small }: Props) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          disabled={disabled}
          aria-label={`Archives ${current}`}
          className={cn('gap-1.5 font-normal', small ? 'h-6 px-1 text-xs' : '-ml-2 h-7 px-2')}
        >
          {current}
          <PencilIcon className="size-3 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">Archive after, from today</DropdownMenuLabel>
        {ARCHIVE_AFTER.map((o) => (
          <DropdownMenuItem key={o.value} onClick={() => onChange(o.value)}>
            {o.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
