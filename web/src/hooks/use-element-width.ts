import { useLayoutEffect, useState, type RefObject } from 'react'

/**
 * An element's own content width, measured before first paint and kept up
 * to date with a ResizeObserver: the grid adapts to the space it gets, not
 * to the screen (D37), since the sidebar takes part of the screen.
 */
export function useElementWidth(ref: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(0)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    setWidth(el.getBoundingClientRect().width)
    const observer = new ResizeObserver((entries) => {
      const entry = entries[entries.length - 1]
      if (entry) setWidth(entry.contentRect.width)
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [ref])
  return width
}
