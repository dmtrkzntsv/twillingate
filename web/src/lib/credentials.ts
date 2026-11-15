// Where the app keeps what signs its requests: the refresh token of a
// login and a token pasted by hand. Both live in sessionStorage, so they
// last as long as the browser session and no longer: nothing that can sign
// a request is left on disk for whoever opens the browser, or its profile
// directory, next. (They used to live in localStorage, which is kept until
// someone clears it; `credential` moves a value found there.)
//
// sessionStorage is per tab, so the tabs of one browser hand the values to
// each other over a BroadcastChannel, which only pages of this origin can
// join: a new tab asks, and any tab that is logged in answers
// (`adoptCredentials`); a tab that rotates the refresh token or logs out
// tells the rest. With every tab closed, the next visit logs in again.

export const REFRESH_TOKEN_KEY = 'twillingate.refresh_token'
export const PASTED_TOKEN_KEY = 'twillingate.token'

const KEYS = [REFRESH_TOKEN_KEY, PASTED_TOKEN_KEY] as const
type Key = (typeof KEYS)[number]

type Message =
  | { type: 'hello' }
  | { type: 'offer'; values: Partial<Record<Key, string>> }
  | { type: 'set'; key: Key; value: string | null }

const CHANNEL = 'twillingate.credentials'
/** How long a new tab waits for another to answer before showing the login. */
const ADOPT_WAIT_MS = 150

let channel: BroadcastChannel | null | undefined

function isKey(k: unknown): k is Key {
  return (KEYS as readonly unknown[]).includes(k)
}

function store(key: Key, value: string | null): void {
  if (value === null) sessionStorage.removeItem(key)
  else sessionStorage.setItem(key, value)
  // Never both: a value in localStorage would outlive the session.
  localStorage.removeItem(key)
}

function held(): Partial<Record<Key, string>> {
  const values: Partial<Record<Key, string>> = {}
  for (const k of KEYS) {
    const v = credential(k)
    if (v) values[k] = v
  }
  return values
}

function onMessage(e: MessageEvent<Message>): void {
  const m = e.data
  if (m?.type === 'hello') {
    const values = held()
    if (Object.keys(values).length > 0) channel?.postMessage({ type: 'offer', values } satisfies Message)
  } else if (m?.type === 'set' && isKey(m.key)) {
    store(m.key, typeof m.value === 'string' ? m.value : null)
  }
}

function open(): BroadcastChannel | null {
  if (channel === undefined) {
    channel = typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(CHANNEL)
    channel?.addEventListener('message', onMessage)
  }
  return channel
}

/** A stored credential, or null. One left in localStorage by an older build moves to the session. */
export function credential(key: Key): string | null {
  const legacy = localStorage.getItem(key)
  if (legacy !== null) {
    if (sessionStorage.getItem(key) === null) sessionStorage.setItem(key, legacy)
    localStorage.removeItem(key)
  }
  return sessionStorage.getItem(key)
}

/** Stores a credential for this session (null forgets it) and tells the other tabs. */
export function setCredential(key: Key, value: string | null): void {
  store(key, value)
  open()?.postMessage({ type: 'set', key, value } satisfies Message)
}

/**
 * Run once before the first request: joins the channel, and in a tab that
 * holds no credential asks the others for theirs, waiting briefly for an
 * answer. Resolves either way; with no other tab logged in, the app shows
 * its login.
 */
export function adoptCredentials(): Promise<void> {
  const ch = open()
  if (!ch || Object.keys(held()).length > 0) return Promise.resolve()
  return new Promise((resolve) => {
    const done = () => {
      ch.removeEventListener('message', onOffer)
      clearTimeout(timer)
      resolve()
    }
    const onOffer = (e: MessageEvent<Message>) => {
      if (e.data?.type !== 'offer') return
      for (const [k, v] of Object.entries(e.data.values ?? {})) {
        if (isKey(k) && typeof v === 'string') store(k, v)
      }
      done()
    }
    const timer = setTimeout(done, ADOPT_WAIT_MS)
    ch.addEventListener('message', onOffer)
    ch.postMessage({ type: 'hello' } satisfies Message)
  })
}

/** Test-only: leaves the channel, so each case starts without one. */
export function _resetCredentialsForTests(): void {
  channel?.close()
  channel = undefined
}
