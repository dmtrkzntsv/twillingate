import { CheckIcon, CopyIcon, TriangleAlertIcon } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'

type CopyState = 'idle' | 'copied' | 'failed'

/**
 * Copies `value`. Plain http has no clipboard, so failing is a state, not an
 * error: the icon and label say what happened, then reset after a moment.
 * `icon` replaces the copy icon while idle, to tell two copies apart.
 */
export default function CopyButton({ value, label, icon }: { value: string; label: string; icon?: ReactNode }) {
  const [state, setState] = useState<CopyState>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const copy = async () => {
    // A second click before the first reset must not let the old timer clear the new state.
    clearTimeout(timer.current)
    try {
      if (!navigator.clipboard) throw new Error('no clipboard')
      await navigator.clipboard.writeText(value)
      setState('copied')
    } catch {
      setState('failed')
    }
    timer.current = setTimeout(() => setState('idle'), 2000)
  }
  const name = state === 'copied' ? 'Copied' : state === 'failed' ? "Couldn't copy" : label
  return (
    <Button type="button" variant="ghost" size="icon" aria-label={name} title={name} onClick={() => void copy()}>
      {state === 'copied' ? <CheckIcon /> : state === 'failed' ? <TriangleAlertIcon /> : (icon ?? <CopyIcon />)}
    </Button>
  )
}
