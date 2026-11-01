import { useCallback, useState } from 'react'

/**
 * Like useState, but kept in localStorage under `key` as JSON; null deletes
 * the key. `parse` vets what is read back and returns null to refuse it.
 * Without a key, or when storage is blocked, the value lives in memory only.
 *
 * The value belongs to the key it was read for: when `key` changes while
 * mounted, the new key's value is read in that same render, so one key's
 * value is never shown, or saved by the setter, under another.
 */
export function useStoredState<T>(
  key: string | undefined,
  parse: (stored: unknown) => T | null
): [T | null, (next: T | null) => void] {
  const [state, setState] = useState<{ key: string | undefined; value: T | null }>(() => ({ key, value: read(key, parse) }))
  let value = state.value
  if (state.key !== key) {
    value = read(key, parse)
    setState({ key, value })
  }
  const set = useCallback(
    (next: T | null) => {
      setState({ key, value: next })
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
