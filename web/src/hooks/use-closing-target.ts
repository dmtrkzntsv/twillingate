import { useState } from 'react'

/** A dialog opened on a target: `show(t)` opens it, and the target is kept once it closes. */
export interface ClosingTarget<T> {
  /** The last target shown; undefined until the first. */
  target: T | undefined
  open: boolean
  show: (target: T) => void
  /** For the dialog's onOpenChange: a close keeps the target, opening is `show`'s job. */
  onOpenChange: (open: boolean) => void
}

/**
 * The state of a dialog opened on a target. The target outlives the close
 * animation, so a closing title does not flash "Block undefined?".
 */
export function useClosingTarget<T>(): ClosingTarget<T> {
  const [state, setState] = useState<{ target: T; open: boolean } | null>(null)
  return {
    target: state?.target,
    open: state?.open === true,
    show: (target) => setState({ target, open: true }),
    onOpenChange: (open) => {
      if (!open) setState((d) => d && { ...d, open: false })
    },
  }
}
