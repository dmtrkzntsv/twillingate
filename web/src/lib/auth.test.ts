import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  _resetForTests,
  beginLogin,
  completeLogin,
  detectAuth,
  getAuthHeader,
  refreshAccess,
  setPastedToken,
} from './auth'

const PROTECTED_RESOURCE = { resource: 'https://api.example', authorization_servers: ['https://api.example'] }
const AS_METADATA = {
  issuer: 'https://api.example',
  authorization_endpoint: 'https://api.example/oauth/authorize',
  token_endpoint: 'https://api.example/oauth/token',
  registration_endpoint: 'https://api.example/oauth/register',
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

// jsdom's Location.assign isn't a configurable own property, so it can't be
// spied on directly; replace the whole object with a plain one that shares
// its (already-evaluated) fields, plus a mock assign.
function mockNavigate(): ReturnType<typeof vi.fn> {
  const assign = vi.fn()
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...window.location, assign },
  })
  return assign
}

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  _resetForTests()
  vi.stubGlobal('fetch', vi.fn())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('detectAuth', () => {
  it('is "open" when /api/dashboards answers without a token', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ dashboards: [] }))
    expect(await detectAuth()).toBe('open')
  })

  it('is "paste" when the protected-resource document is missing', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(new Response(null, { status: 404 }))
    expect(await detectAuth()).toBe('paste')
  })

  it('is "login" when the authorization server has a registration endpoint', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
    expect(await detectAuth()).toBe('login')
  })

  it('is "paste" when the authorization server has no registration endpoint', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse({ ...AS_METADATA, registration_endpoint: undefined }))
    expect(await detectAuth()).toBe('paste')
  })
})

describe('beginLogin', () => {
  it('registers, then redirects with S256, resource and the app redirect_uri', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
      .mockResolvedValueOnce(jsonResponse({ client_id: 'client-1' }, 201))
    const assign = mockNavigate()

    await beginLogin('/dashboards/1')

    expect(assign).toHaveBeenCalledTimes(1)
    const url = new URL(assign.mock.calls[0][0] as string)
    expect(url.origin + url.pathname).toBe('https://api.example/oauth/authorize')
    expect(url.searchParams.get('response_type')).toBe('code')
    expect(url.searchParams.get('client_id')).toBe('client-1')
    expect(url.searchParams.get('code_challenge_method')).toBe('S256')
    expect(url.searchParams.get('code_challenge')).toBeTruthy()
    expect(url.searchParams.get('resource')).toBe('https://api.example')
    expect(url.searchParams.get('redirect_uri')).toBe(`${location.origin}/app/callback`)
    expect(url.searchParams.get('state')).toBeTruthy()
    expect(localStorage.getItem('twillingate.client_id')).toBe('client-1')
  })

  it('reuses a previously registered client id', async () => {
    localStorage.setItem('twillingate.client_id', 'existing-client')
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
    mockNavigate()

    await beginLogin('/')

    expect(fetch).toHaveBeenCalledTimes(2) // no registration call
  })
})

describe('completeLogin', () => {
  async function begin() {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
      .mockResolvedValueOnce(jsonResponse({ client_id: 'client-1' }, 201))
    mockNavigate()
    await beginLogin('/dashboards/9')
    const state = sessionStorage.getItem('twillingate.oauth_state')
    vi.mocked(fetch).mockReset()
    return state
  }

  it('exchanges the code for tokens and returns the saved return path', async () => {
    const state = await begin()
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ access_token: 'access-1', refresh_token: 'refresh-1', token_type: 'Bearer', expires_in: 3600 })
    )

    const returnTo = await completeLogin(`?code=abc&state=${state}`)

    expect(returnTo).toBe('/dashboards/9')
    expect(getAuthHeader()).toBe('Bearer access-1')
    expect(localStorage.getItem('twillingate.refresh_token')).toBe('refresh-1')
  })

  it('rejects a mismatched state', async () => {
    await begin()
    await expect(completeLogin('?code=abc&state=not-the-right-state')).rejects.toThrow()
    expect(fetch).not.toHaveBeenCalled()
  })
})

describe('setPastedToken', () => {
  it('stores the token for the Authorization header', () => {
    setPastedToken('pasted-token')
    expect(getAuthHeader()).toBe('Bearer pasted-token')
  })
})

describe('refreshAccess', () => {
  it('returns false with nothing to refresh', async () => {
    expect(await refreshAccess()).toBe(false)
    expect(fetch).not.toHaveBeenCalled()
  })
})
