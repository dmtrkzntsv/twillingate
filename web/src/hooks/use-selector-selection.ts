import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router'
import type { Selector } from '@/lib/api'
import { readSelection, writeSelection, type Selection } from '@/lib/selectors'

export interface SelectorSelection {
  /** The values picked of each selector, from the URL, and the dashboard's selector names in order; stable while they hold. */
  picked: { selection: Selection; order: string[] }
  /** Picks `values` of selector `name` (none is All): into the URL only, never stored (a forgotten filter looks plausible). */
  pick: (name: string, values: string[]) => void
}

/**
 * The values picked of a dashboard's selectors: the URL's `s.<name>`
 * parameters, so a dashboard opened from the sidebar, whose link has none,
 * starts at All. While `frozen` (the page is leaving this dashboard) a pick
 * is ignored.
 */
export function useSelectorSelection(selectors: Selector[], frozen = false): SelectorSelection {
  const [url, setURL] = useSearchParams()
  const order = useMemo(() => selectors.map((s) => s.name), [selectors])
  // Kept by its text, so the object, and every paramsFor built on it, holds
  // across renders until the selection itself changes.
  const text = JSON.stringify(readSelection(url, order))
  const picked = useMemo(() => ({ selection: JSON.parse(text) as Selection, order }), [text, order])
  const pick = useCallback(
    (name: string, values: string[]) => {
      if (frozen) return
      // Replacing the entry, so each tick of a checklist is not a step of Back.
      setURL((prev) => writeSelection(prev, { ...readSelection(prev, order), [name]: values }), { replace: true })
    },
    [frozen, order, setURL]
  )
  return { picked, pick }
}
