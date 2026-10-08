import { useCallback, useEffect, useRef, useState } from 'react'

export type CopyState = 'idle' | 'copied' | 'failed'

/** What one copy did: `none` when a text still to come never came (its request failed). */
export type CopyOutcome = 'copied' | 'failed' | 'none'

/** Thrown into a ClipboardItem's promise when its text never comes. */
class NoText extends Error {}

async function write(text: string | Promise<string | undefined>): Promise<CopyOutcome> {
  const clip = typeof navigator === 'undefined' ? undefined : navigator.clipboard
  if (typeof text !== 'string') {
    // Safari copies only inside the click: a write started after a request
    // answers is refused. A ClipboardItem takes the text as a promise, so the
    // write starts now and finishes when the text comes.
    if (clip?.write && typeof ClipboardItem !== 'undefined') {
      const blob = text.then((t) => {
        if (t === undefined) throw new NoText()
        return new Blob([t], { type: 'text/plain' })
      })
      blob.catch(() => {})
      try {
        await clip.write([new ClipboardItem({ 'text/plain': blob })])
        return 'copied'
      } catch {
        // Refused (a browser without promised items) or no text: below says which.
      }
    }
    const t = await text
    if (t === undefined) return 'none'
    text = t
  }
  if (!clip) return 'failed'
  try {
    await clip.writeText(text)
    return 'copied'
  } catch {
    return 'failed'
  }
}

/**
 * Copies text to the clipboard, now or once it comes. Plain http has no
 * clipboard, so failing is a state, not an error: `state` says what the last
 * copy did, then goes back to idle after a moment.
 */
export function useCopy(): { state: CopyState; copy: (text: string | Promise<string | undefined>) => Promise<CopyOutcome> } {
  const [state, setState] = useState<CopyState>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const copy = useCallback(async (text: string | Promise<string | undefined>) => {
    const out = await write(text)
    if (out === 'none') return out
    // A second copy before the first reset must not let the old timer clear the new state.
    clearTimeout(timer.current)
    setState(out)
    timer.current = setTimeout(() => setState('idle'), 2000)
    return out
  }, [])
  return { state, copy }
}
