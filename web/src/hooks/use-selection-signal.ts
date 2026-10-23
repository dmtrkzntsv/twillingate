import { useCallback, useEffect, useRef } from 'react'

/**
 * A signal for the loads a page starts under its current selection
 * (`selection` is anything that changes with it, such as its `paramsFor`):
 * aborted once the selection changes or the page is left, so a refresh
 * still streaming for the old range stops there.
 */
export function useSelectionSignal(selection: unknown): () => AbortSignal {
  const controller = useRef<AbortController | null>(null)
  useEffect(
    () => () => {
      controller.current?.abort()
      controller.current = null
    },
    [selection]
  )
  return useCallback(() => (controller.current ??= new AbortController()).signal, [])
}
