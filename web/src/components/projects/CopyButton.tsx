import { CheckIcon, CopyIcon, TriangleAlertIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { useCopy } from '@/hooks/use-copy'

/**
 * Copies `value`; the icon and label say what happened, then reset after a
 * moment. `icon` replaces the copy icon while idle, to tell two copies apart.
 * `onFail` hears of a failed copy, for a page that then shows the text.
 */
export default function CopyButton({ value, label, icon, onFail }: { value: string; label: string; icon?: ReactNode; onFail?: () => void }) {
  const { state, copy } = useCopy()
  const name = state === 'copied' ? 'Copied' : state === 'failed' ? "Couldn't copy" : label
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      aria-label={name}
      title={name}
      onClick={() => void copy(value).then((out) => out === 'failed' && onFail?.())}
    >
      {state === 'copied' ? <CheckIcon /> : state === 'failed' ? <TriangleAlertIcon /> : (icon ?? <CopyIcon />)}
    </Button>
  )
}
