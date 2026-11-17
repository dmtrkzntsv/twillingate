// Where the app keeps what signs its requests: the refresh token of a
// login and a token pasted by hand. Both live in sessionStorage, so they
// last as long as the browser session: closing the browser forgets them,
// unless it restores its last session, which brings sessionStorage back
// with the tabs. (They used to live in localStorage, which is kept until
// someone clears it; `credential` moves a value found there.)
//
// sessionStorage is per tab, so the tabs of one browser hand the values to
// each other over a BroadcastChannel, which only pages of this origin can
// join: a new tab asks, and any tab that is logged in answers
// (`adoptCredentials`); a tab that rotates the refresh token or logs out
// tells the rest. With every tab closed, the next visit logs in again.
//
// A logout must hold against messages already on their way: another tab's
// refresh that crosses it, an answer to an earlier hello. So a logout also
// writes a new logout epoch to localStorage, which every tab reads at once
// (it signs nothing); each tab notes the epoch its credentials were stored
// under, and a credential from an older epoch, held or arriving, counts
// for nothing.

export const REFRESH_TOKEN_KEY = 'twillingate.refresh_token'
export const PASTED_TOKEN_KEY = 'twillingate.token'

const KEYS = [REFRESH_TOKEN_KEY, PASTED_TOKEN_KEY] as const
type Key = (typeof KEYS)[number]

// Every message that carries a credential names the epoch it belongs to.
type Message =
  | { type: 'hello' }
  | { type: 'offer'; epoch: string; values: Partial<Record<Key, string>> }
  | { type: 'set'; epoch: string; key: Key; value: string | null }

/** localStorage: changes on every logout, in any tab. Not a credential. */
export const LOGOUT_EPOCH_KEY = 'twillingate.logout_epoch'
/** sessionStorage: the epoch this tab's credentials were stored under. */
const STORED_EPOCH_KEY = 'twillingate.credentials_epoch'

const CHANNEL = 'twillingate.credentials'
/** How long a new tab waits for another to answer before showing the login. */
const ADOPT_WAIT_MS = 150
/** How long a late answer is still taken (adoptCredentials' onLate). */
const ADOPT_LATE_MS = 5000

let channel: BroadcastChannel | null | undefined
const forgotten = new Set<(key: Key) => void>()

/**
 * Calls fn when another tab forgets a credential (a logout, a rejected
 * refresh token), so this tab can drop what it derived from it: the
 * access token it holds in memory.
 */
export function onForgottenElsewhere(fn: (key: Key) => void): void {
  forgotten.add(fn)
}

function isKey(k: unknown): k is Key {
  return (KEYS as readonly unknown[]).includes(k)
}

/** The current logout epoch, shared by every tab. */
export function logoutEpoch(): string {
  return localStorage.getItem(LOGOUT_EPOCH_KEY) ?? '0'
}

/** Starts a new logout epoch: every credential stored before it, in any tab, stops counting. */
export function markLoggedOut(): void {
  localStorage.setItem(LOGOUT_EPOCH_KEY, `${Date.now()}.${Math.random().toString(36).slice(2)}`)
}

// Stores value as of epoch at; a value from an epoch a logout has ended
// is refused. Reports whether it was stored.
function store(key: Key, value: string | null, at: string = logoutEpoch()): boolean {
  // Never both: a value in localStorage would outlive the session.
  localStorage.removeItem(key)
  if (value === null) {
    sessionStorage.removeItem(key)
    if (KEYS.every((k) => sessionStorage.getItem(k) === null)) sessionStorage.removeItem(STORED_EPOCH_KEY)
    return true
  }
  if (at !== logoutEpoch()) return false
  sessionStorage.setItem(key, value)
  sessionStorage.setItem(STORED_EPOCH_KEY, at)
  return true
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
    if (Object.keys(values).length > 0) {
      channel?.postMessage({ type: 'offer', epoch: logoutEpoch(), values } satisfies Message)
    }
  } else if (m?.type === 'set' && isKey(m.key)) {
    const value = typeof m.value === 'string' ? m.value : null
    store(m.key, value, String(m.epoch))
    if (value === null) for (const fn of forgotten) fn(m.key)
  }
}

function open(): BroadcastChannel | null {
  if (channel === undefined) {
    channel = typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(CHANNEL)
    channel?.addEventListener('message', onMessage)
  }
  return channel
}

/**
 * A stored credential, or null. One stored before the latest logout, in
 * any tab, is forgotten. One left in localStorage by an older build moves
 * to the session.
 */
export function credential(key: Key): string | null {
  const legacy = localStorage.getItem(key)
  if (legacy !== null) {
    if (sessionStorage.getItem(key) === null) store(key, legacy)
    localStorage.removeItem(key)
  }
  if ((sessionStorage.getItem(STORED_EPOCH_KEY) ?? '0') !== logoutEpoch()) {
    for (const k of KEYS) sessionStorage.removeItem(k)
    sessionStorage.removeItem(STORED_EPOCH_KEY)
    return null
  }
  return sessionStorage.getItem(key)
}

/**
 * Stores a credential for this session (null forgets it) and tells the
 * other tabs. A value obtained before a logout must not be stored: the
 * caller checks logoutEpoch() is still the one it started under.
 */
export function setCredential(key: Key, value: string | null): void {
  store(key, value)
  open()?.postMessage({ type: 'set', epoch: logoutEpoch(), key, value } satisfies Message)
}

/**
 * Run once before the first request: joins the channel, and in a tab that
 * holds no credential asks the others for theirs, waiting briefly for an
 * answer. Resolves either way; with no other tab logged in, the app shows
 * its login.
 *
 * A tab that answers late (busy, or throttled in the background) is still
 * heard for ADOPT_LATE_MS: if this tab still holds nothing, it takes the
 * credentials and calls onLate, so the page can start again logged in.
 */
export function adoptCredentials(onLate?: () => void): Promise<void> {
  const ch = open()
  if (!ch || Object.keys(held()).length > 0) return Promise.resolve()
  return new Promise((resolve) => {
    let resolved = false
    const stop = () => {
      ch.removeEventListener('message', onOffer)
      clearTimeout(lateTimer)
    }
    const onOffer = (e: MessageEvent<Message>) => {
      if (e.data?.type !== 'offer') return
      if (resolved && Object.keys(held()).length > 0) return stop()
      let took = false
      for (const [k, v] of Object.entries(e.data.values ?? {})) {
        if (isKey(k) && typeof v === 'string' && store(k, v, String(e.data.epoch))) took = true
      }
      if (!took) return // from before a logout: keep waiting for a live one
      stop()
      clearTimeout(timer)
      if (resolved) onLate?.()
      else resolve()
    }
    const timer = setTimeout(() => {
      resolved = true
      resolve()
    }, ADOPT_WAIT_MS)
    const lateTimer = setTimeout(stop, ADOPT_LATE_MS)
    ch.addEventListener('message', onOffer)
    ch.postMessage({ type: 'hello' } satisfies Message)
  })
}

/** Test-only: leaves the channel, so each case starts without one. */
export function _resetCredentialsForTests(): void {
  channel?.close()
  channel = undefined
}
