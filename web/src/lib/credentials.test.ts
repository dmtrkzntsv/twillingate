import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  _resetCredentialsForTests,
  LOGOUT_EPOCH_KEY,
  adoptCredentials,
  credential,
  PASTED_TOKEN_KEY,
  REFRESH_TOKEN_KEY,
  setCredential,
} from './credentials'
import { _resetForTests, currentAuthState, refreshAccess, getAuthHeader, logout, setPastedToken } from './auth'

// Another tab of the same origin: its own end of the channel.
let other: BroadcastChannel
const heard: unknown[] = []

const tick = () => new Promise((r) => setTimeout(r, 20))

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  _resetForTests()
  _resetCredentialsForTests()
  heard.length = 0
  other = new BroadcastChannel('twillingate.credentials')
  other.addEventListener('message', (e) => heard.push(e.data))
})

afterEach(() => {
  other.close()
  _resetCredentialsForTests()
})

describe('credentials', () => {
  it('keeps a credential for the session, never in localStorage', () => {
    setPastedToken('secret')
    setCredential(REFRESH_TOKEN_KEY, 'refresh-1')

    expect(getAuthHeader()).toBe('Bearer secret')
    expect(sessionStorage.getItem(PASTED_TOKEN_KEY)).toBe('secret')
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('refresh-1')
    for (let i = 0; i < localStorage.length; i++) {
      const v = localStorage.getItem(localStorage.key(i)!)
      expect(v).not.toContain('secret')
      expect(v).not.toContain('refresh-1')
    }
  })

  it('moves a credential an older build left in localStorage into the session', () => {
    localStorage.setItem(REFRESH_TOKEN_KEY, 'old-refresh')
    localStorage.setItem(PASTED_TOKEN_KEY, 'old-token')

    expect(currentAuthState()).toEqual({ kind: 'oauth' })
    expect(credential(PASTED_TOKEN_KEY)).toBe('old-token')
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull()
    expect(localStorage.getItem(PASTED_TOKEN_KEY)).toBeNull()
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('old-refresh')
  })

  it('a new tab adopts the credentials of a tab that is logged in', async () => {
    other.addEventListener('message', (e) => {
      if (e.data?.type === 'hello') {
        other.postMessage({ type: 'offer', epoch: '0', values: { [REFRESH_TOKEN_KEY]: 'theirs', 'not.a.credential': 'x' } })
      }
    })
    await adoptCredentials()

    expect(credential(REFRESH_TOKEN_KEY)).toBe('theirs')
    expect(sessionStorage.getItem('not.a.credential')).toBeNull()
    expect(currentAuthState()).toEqual({ kind: 'oauth' })
  })

  it('takes a late answer after the login has shown, and says so', async () => {
    let late = 0
    await adoptCredentials(() => late++)
    expect(currentAuthState()).toEqual({ kind: 'none' })

    other.postMessage({ type: 'offer', epoch: '0', values: { [REFRESH_TOKEN_KEY]: 'late' } })
    await tick()
    expect(credential(REFRESH_TOKEN_KEY)).toBe('late')
    expect(late).toBe(1)

    // Only the first answer counts.
    other.postMessage({ type: 'offer', epoch: '0', values: { [REFRESH_TOKEN_KEY]: 'later' } })
    await tick()
    expect(credential(REFRESH_TOKEN_KEY)).toBe('late')
    expect(late).toBe(1)
  })

  it('ignores a late answer once this tab has logged in by itself', async () => {
    let late = 0
    await adoptCredentials(() => late++)
    setCredential(REFRESH_TOKEN_KEY, 'mine')
    other.postMessage({ type: 'offer', epoch: '0', values: { [REFRESH_TOKEN_KEY]: 'theirs' } })
    await tick()
    expect(credential(REFRESH_TOKEN_KEY)).toBe('mine')
    expect(late).toBe(0)
  })

  it('a new tab with no one to ask goes on without credentials', async () => {
    await adoptCredentials()
    expect(currentAuthState()).toEqual({ kind: 'none' })
  })

  it('a tab that is logged in does not ask, and answers a tab that does', async () => {
    setCredential(PASTED_TOKEN_KEY, 'mine')
    await adoptCredentials()
    await tick()
    expect(heard.filter((m) => (m as { type: string }).type === 'hello')).toHaveLength(0)

    other.postMessage({ type: 'hello' })
    await tick()
    expect(heard).toContainEqual({ type: 'offer', epoch: '0', values: { [PASTED_TOKEN_KEY]: 'mine' } })
  })

  it('a tab without credentials answers no one', async () => {
    await adoptCredentials()
    other.postMessage({ type: 'hello' })
    await tick()
    expect(heard.filter((m) => (m as { type: string }).type === 'offer')).toHaveLength(0)
  })

  it('follows a rotated refresh token and a logout from another tab', async () => {
    setCredential(REFRESH_TOKEN_KEY, 'refresh-1')
    await adoptCredentials()

    other.postMessage({ type: 'set', epoch: '0', key: REFRESH_TOKEN_KEY, value: 'refresh-2' })
    await tick()
    expect(credential(REFRESH_TOKEN_KEY)).toBe('refresh-2')

    other.postMessage({ type: 'set', epoch: '0', key: 'twillingate.last_dashboard', value: 'x' })
    other.postMessage({ type: 'set', epoch: '0', key: REFRESH_TOKEN_KEY, value: null })
    await tick()
    expect(credential(REFRESH_TOKEN_KEY)).toBeNull()
    expect(sessionStorage.getItem('twillingate.last_dashboard')).toBeNull()
  })

  it('a logout in another tab drops the access token held in memory here', async () => {
    setCredential(REFRESH_TOKEN_KEY, 'refresh-1')
    localStorage.setItem('twillingate.token_endpoint', 'https://api.example/oauth/token')
    localStorage.setItem('twillingate.resource', 'https://api.example')
    localStorage.setItem('twillingate.client_id', 'client-1')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ access_token: 'access-1' }), { status: 200 })))
    try {
      expect(await refreshAccess()).toBe(true)
      expect(getAuthHeader()).toBe('Bearer access-1')

      other.postMessage({ type: 'set', epoch: '0', key: REFRESH_TOKEN_KEY, value: null })
      await tick()
      expect(getAuthHeader()).toBeUndefined()
      expect(currentAuthState()).toEqual({ kind: 'none' })
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('a refresh answered after a logout stores nothing', async () => {
    setCredential(REFRESH_TOKEN_KEY, 'refresh-1')
    localStorage.setItem('twillingate.token_endpoint', 'https://api.example/oauth/token')
    localStorage.setItem('twillingate.resource', 'https://api.example')
    localStorage.setItem('twillingate.client_id', 'client-1')
    let answer: (r: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((r) => (answer = r))))
    try {
      const refreshed = refreshAccess()
      await tick()
      logout()
      await tick() // the logout's own messages
      heard.length = 0
      answer(new Response(JSON.stringify({ access_token: 'access-2', refresh_token: 'refresh-2' }), { status: 200 }))
      expect(await refreshed).toBe(false)
      await tick()

      expect(getAuthHeader()).toBeUndefined()
      expect(credential(REFRESH_TOKEN_KEY)).toBeNull()
      expect(heard).toHaveLength(0)
    } finally {
      vi.unstubAllGlobals()
    }
  })

  // A logout must hold against what is already on its way from other tabs.
  describe('after a logout', () => {
    it('a refresh token another tab sent before it is refused', async () => {
      setCredential(REFRESH_TOKEN_KEY, 'refresh-1')
      const before = localStorage.getItem(LOGOUT_EPOCH_KEY) ?? '0'
      logout()
      other.postMessage({ type: 'set', epoch: before, key: REFRESH_TOKEN_KEY, value: 'refresh-2' })
      await tick()
      expect(credential(REFRESH_TOKEN_KEY)).toBeNull()
      expect(currentAuthState()).toEqual({ kind: 'none' })
    })

    it('a late answer to an earlier hello is refused', async () => {
      let late = 0
      await adoptCredentials(() => late++)
      const before = localStorage.getItem(LOGOUT_EPOCH_KEY) ?? '0'
      logout()
      other.postMessage({ type: 'offer', epoch: before, values: { [REFRESH_TOKEN_KEY]: 'stale' } })
      await tick()
      expect(credential(REFRESH_TOKEN_KEY)).toBeNull()
      expect(late).toBe(0)
    })

    it('in another tab, what this tab holds stops counting before its message arrives', async () => {
      setCredential(REFRESH_TOKEN_KEY, 'refresh-1')
      localStorage.setItem('twillingate.token_endpoint', 'https://api.example/oauth/token')
      localStorage.setItem('twillingate.resource', 'https://api.example')
      localStorage.setItem('twillingate.client_id', 'client-1')
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ access_token: 'access-1' }), { status: 200 })))
      try {
        expect(await refreshAccess()).toBe(true)
        // The other tab's logout, as localStorage shows it at once.
        localStorage.setItem(LOGOUT_EPOCH_KEY, 'theirs')
        expect(getAuthHeader()).toBeUndefined()
        expect(credential(REFRESH_TOKEN_KEY)).toBeNull()
        expect(currentAuthState()).toEqual({ kind: 'none' })
      } finally {
        vi.unstubAllGlobals()
      }
    })

    it('a forgetting sent before it does not end the login made since', async () => {
      const before = localStorage.getItem(LOGOUT_EPOCH_KEY) ?? '0'
      logout()
      setCredential(REFRESH_TOKEN_KEY, 'next-login')
      other.postMessage({ type: 'set', epoch: before, key: REFRESH_TOKEN_KEY, value: null })
      await tick()
      expect(credential(REFRESH_TOKEN_KEY)).toBe('next-login')
    })

    it('a tab that missed it does not revive its stale credential by storing another', () => {
      setCredential(REFRESH_TOKEN_KEY, 'stale')
      // Another tab logs out; this one never hears of it.
      localStorage.setItem(LOGOUT_EPOCH_KEY, 'theirs')
      setPastedToken('pasted')
      expect(credential(PASTED_TOKEN_KEY)).toBe('pasted')
      expect(credential(REFRESH_TOKEN_KEY)).toBeNull()
    })

    it('a credential sent under the new epoch is taken: the next login', async () => {
      logout()
      const now = localStorage.getItem(LOGOUT_EPOCH_KEY)!
      other.postMessage({ type: 'set', epoch: now, key: REFRESH_TOKEN_KEY, value: 'fresh' })
      await tick()
      expect(credential(REFRESH_TOKEN_KEY)).toBe('fresh')
    })
  })

  it('logout forgets the credentials here and tells the other tabs', async () => {
    setCredential(REFRESH_TOKEN_KEY, 'refresh-1')
    setPastedToken('secret')
    localStorage.setItem('twillingate.client_id', 'client')
    heard.length = 0

    logout()
    await tick()

    expect(sessionStorage.length).toBe(0)
    expect(localStorage.getItem('twillingate.client_id')).toBeNull()
    expect(currentAuthState()).toEqual({ kind: 'none' })
    expect(heard).toContainEqual(expect.objectContaining({ type: 'set', key: REFRESH_TOKEN_KEY, value: null }))
    expect(heard).toContainEqual(expect.objectContaining({ type: 'set', key: PASTED_TOKEN_KEY, value: null }))
  })
})
