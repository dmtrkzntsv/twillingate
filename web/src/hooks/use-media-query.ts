import { useSyncExternalStore } from 'react'

/** Whether a CSS media query currently matches, following its changes. */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const mql = window.matchMedia(query)
      mql.addEventListener('change', onChange)
      return () => mql.removeEventListener('change', onChange)
    },
    () => window.matchMedia(query).matches
  )
}

/** Phones: below 640px the chrome switches to drawers, sheets and selects (D37). */
export const PHONE = '(max-width: 639px)'
