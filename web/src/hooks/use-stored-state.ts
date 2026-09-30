import { useCallback, useState } from 'react'

/**
 * Like useState, but kept in localStorage under `key` as JSON; null deletes
 * the key. `parse` vets what is read back and returns null to refuse it.
 * Without a key, or when storage is blocked, the value lives in memory only.
 */
export function useStoredState<T>(
  key: string | undefined,
  parse: (stored: unknown) => T | null
): [T | null, (next: T | null) => void] {
  const [value, setValue] = useState<T | null>(() => read(key, parse))
  const set = useCallback(
    (next: T | null) => {
      setValue(next)
      if (!key) return
      try {
        if (next === null) localStorage.removeItem(key)
        else localStorage.setItem(key, JSON.stringify(next))
      } catch {
        // Storage full or blocked: the value still holds for this page.
      }
    },
    [key]
  )
  return [value, set]
}

function read<T>(key: string | undefined, parse: (stored: unknown) => T | null): T | null {
  if (!key) return null
  try {
    const raw = localStorage.getItem(key)
    return raw === null ? null : parse(JSON.parse(raw))
  } catch {
    return null
  }
}
