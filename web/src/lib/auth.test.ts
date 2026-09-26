import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  _resetForTests,
  authProviderHint,
  beginLogin,
  completeLogin,
  detectAuth,
  getAuthHeader,
  onUnauthorized,
  refreshAccess,
  reportUnauthorized,
  sanitizeReturnTo,
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

function bodyOf(init: RequestInit | undefined): URLSearchParams {
  return new URLSearchParams(init?.body as string)
}

async function s256(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))
  let binary = ''
  new Uint8Array(digest).forEach((b) => {
    binary += String.fromCharCode(b)
  })
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// jsdom's Location.assign isn't a configurable own property, so it can't be
// spied on directly; stub the global with a plain object that carries the
// fields auth.ts reads (explicitly, since Location's own fields are
// accessors a plain object spread doesn't pick up), plus a mock assign.
// vi.stubGlobal (unlike Object.defineProperty) restores the real, live
// Location on vi.unstubAllGlobals(), so a later test's history.pushState
// still works.
function mockNavigate(): ReturnType<typeof vi.fn> {
  const assign = vi.fn()
  const { origin, pathname, href } = window.location
  vi.stubGlobal('location', { origin, pathname, href, assign })
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
  window.history.pushState({}, '', '/')
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

describe('authProviderHint', () => {
  it('names the authorization server when it falls back to paste', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse({ ...AS_METADATA, registration_endpoint: undefined }))
    await detectAuth()
    expect(authProviderHint()).toBe('api.example')
  })

  it('is unset after a "login" outcome', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
    await detectAuth()
    expect(authProviderHint()).toBeUndefined()
  })
})

describe('sanitizeReturnTo', () => {
  it('accepts an in-app path', () => {
    expect(sanitizeReturnTo('/dashboards/3')).toBe('/dashboards/3')
  })

  it('rejects a protocol-relative URL', () => {
    expect(sanitizeReturnTo('//evil.example')).toBe('/')
  })

  it('rejects an absolute URL', () => {
    expect(sanitizeReturnTo('https://evil.example')).toBe('/')
  })

  it('rejects a path with no leading slash', () => {
    expect(sanitizeReturnTo('dashboards/3')).toBe('/')
  })

  it('defaults a missing value to "/"', () => {
    expect(sanitizeReturnTo(null)).toBe('/')
    expect(sanitizeReturnTo(undefined)).toBe('/')
  })

  it('rejects a backslash form a browser may normalize into //evil.com', () => {
    expect(sanitizeReturnTo('/\\evil.com')).toBe('/')
    expect(sanitizeReturnTo('/\\/evil.com')).toBe('/')
  })

  it('rejects a backslash anywhere in the path, not just as the second character', () => {
    expect(sanitizeReturnTo('/dashboards/\\evil.com')).toBe('/')
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

  it('sends the client name in the registration request', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
      .mockResolvedValueOnce(jsonResponse({ client_id: 'client-1' }, 201))
    mockNavigate()

    await beginLogin('/')

    const [registerURL, registerInit] = vi.mocked(fetch).mock.calls[2]
    expect(registerURL).toBe('https://api.example/oauth/register')
    const body = JSON.parse(registerInit?.body as string)
    expect(body.client_name).toBe('twillingate dashboards')
    expect(body.token_endpoint_auth_method).toBe('none')
    expect(body.redirect_uris).toEqual([`${location.origin}/app/callback`])
  })

  it('the code_challenge is the S256 hash of the verifier it stores', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
      .mockResolvedValueOnce(jsonResponse({ client_id: 'client-1' }, 201))
    const assign = mockNavigate()

    await beginLogin('/')

    const url = new URL(assign.mock.calls[0][0] as string)
    const verifier = sessionStorage.getItem('twillingate.oauth_verifier')
    expect(verifier).toBeTruthy()
    expect(url.searchParams.get('code_challenge')).toBe(await s256(verifier!))
  })

  it('re-registers on every call, replacing a cached client id', async () => {
    localStorage.setItem('twillingate.client_id', 'stale-client')
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
      .mockResolvedValueOnce(jsonResponse({ client_id: 'fresh-client' }, 201))
    const assign = mockNavigate()

    await beginLogin('/')

    expect(fetch).toHaveBeenCalledTimes(3)
    expect(localStorage.getItem('twillingate.client_id')).toBe('fresh-client')
    const url = new URL(assign.mock.calls[0][0] as string)
    expect(url.searchParams.get('client_id')).toBe('fresh-client')
  })

  it('falls back to a cached client id when the server has no registration endpoint', async () => {
    localStorage.setItem('twillingate.client_id', 'cached-client')
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse({ ...AS_METADATA, registration_endpoint: undefined }))
    mockNavigate()

    await beginLogin('/')

    expect(fetch).toHaveBeenCalledTimes(2) // no registration call possible
    expect(localStorage.getItem('twillingate.client_id')).toBe('cached-client')
  })

  it('sanitizes an unsafe returnTo before storing it', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(PROTECTED_RESOURCE))
      .mockResolvedValueOnce(jsonResponse(AS_METADATA))
      .mockResolvedValueOnce(jsonResponse({ client_id: 'client-1' }, 201))
    mockNavigate()

    await beginLogin('//evil.example')

    expect(sessionStorage.getItem('twillingate.oauth_return_to')).toBe('/')
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
    const verifier = sessionStorage.getItem('twillingate.oauth_verifier')
    vi.mocked(fetch).mockReset()
    return { state, verifier }
  }

  it('exchanges the code for tokens and returns the saved return path', async () => {
    const { state } = await begin()
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ access_token: 'access-1', refresh_token: 'refresh-1', token_type: 'Bearer', expires_in: 3600 })
    )

    const returnTo = await completeLogin(`?code=abc&state=${state}`)

    expect(returnTo).toBe('/dashboards/9')
    expect(getAuthHeader()).toBe('Bearer access-1')
    expect(localStorage.getItem('twillingate.refresh_token')).toBe('refresh-1')
  })

  it('sends code_verifier, redirect_uri, client_id and resource in the token request', async () => {
    const { state, verifier } = await begin()
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ access_token: 'access-1', refresh_token: 'refresh-1' })
    )

    await completeLogin(`?code=abc123&state=${state}`)

    const [tokenURL, tokenInit] = vi.mocked(fetch).mock.calls[0]
    expect(tokenURL).toBe('https://api.example/oauth/token')
    const body = bodyOf(tokenInit)
    expect(body.get('grant_type')).toBe('authorization_code')
    expect(body.get('code')).toBe('abc123')
    expect(body.get('code_verifier')).toBe(verifier)
    expect(body.get('redirect_uri')).toBe(`${location.origin}/app/callback`)
    expect(body.get('client_id')).toBe('client-1')
    expect(body.get('resource')).toBe('https://api.example')
  })

  it('rejects a mismatched state', async () => {
    await begin()
    await expect(completeLogin('?code=abc&state=not-the-right-state')).rejects.toThrow()
    expect(fetch).not.toHaveBeenCalled()
  })

  it('rejects a replayed state once it has already been consumed', async () => {
    const { state } = await begin()
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ access_token: 'access-1', refresh_token: 'refresh-1' })
    )
    await completeLogin(`?code=abc&state=${state}`)

    // A different code presented with the same, already-consumed state.
    await expect(completeLogin(`?code=another-code&state=${state}`)).rejects.toThrow()
  })

  it('memoises by search string, so a StrictMode double-invoke joins the first call', async () => {
    const { state } = await begin()
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ access_token: 'access-1', refresh_token: 'refresh-1' })
    )
    const search = `?code=abc&state=${state}`

    const [first, second] = await Promise.all([completeLogin(search), completeLogin(search)])

    expect(first).toBe('/dashboards/9')
    expect(second).toBe('/dashboards/9')
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})

describe('setPastedToken', () => {
  it('stores the token for the Authorization header', () => {
    setPastedToken('pasted-token')
    expect(getAuthHeader()).toBe('Bearer pasted-token')
  })
})

describe('refreshAccess', () => {
  function storeRefreshCredentials() {
    localStorage.setItem('twillingate.refresh_token', 'refresh-1')
    localStorage.setItem('twillingate.token_endpoint', 'https://api.example/oauth/token')
    localStorage.setItem('twillingate.resource', 'https://api.example')
    localStorage.setItem('twillingate.client_id', 'client-1')
  }

  it('returns false with nothing to refresh', async () => {
    expect(await refreshAccess()).toBe(false)
    expect(fetch).not.toHaveBeenCalled()
  })

  it('redeems the refresh token and updates the access token', async () => {
    storeRefreshCredentials()
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ access_token: 'access-2', refresh_token: 'refresh-2' })
    )

    expect(await refreshAccess()).toBe(true)

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('https://api.example/oauth/token')
    const body = bodyOf(init)
    expect(body.get('grant_type')).toBe('refresh_token')
    expect(body.get('refresh_token')).toBe('refresh-1')
    expect(body.get('client_id')).toBe('client-1')
    expect(body.get('resource')).toBe('https://api.example')
    expect(getAuthHeader()).toBe('Bearer access-2')
    expect(localStorage.getItem('twillingate.refresh_token')).toBe('refresh-2')
  })

  it('is single-flight: concurrent callers share one request', async () => {
    storeRefreshCredentials()
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ access_token: 'access-2', refresh_token: 'refresh-2' })
    )

    const [a, b] = await Promise.all([refreshAccess(), refreshAccess()])

    expect(a).toBe(true)
    expect(b).toBe(true)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('clears the refresh token and client id on invalid_grant', async () => {
    storeRefreshCredentials()
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ error: 'invalid_grant' }, 400))

    expect(await refreshAccess()).toBe(false)

    expect(localStorage.getItem('twillingate.refresh_token')).toBeNull()
    expect(localStorage.getItem('twillingate.client_id')).toBeNull()
    expect(getAuthHeader()).toBeUndefined()
  })

  it('clears the refresh token and client id on invalid_client', async () => {
    storeRefreshCredentials()
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ error: 'invalid_client' }, 400))

    expect(await refreshAccess()).toBe(false)

    expect(localStorage.getItem('twillingate.refresh_token')).toBeNull()
    expect(localStorage.getItem('twillingate.client_id')).toBeNull()
  })

  it('leaves stored credentials alone on a transient server error', async () => {
    storeRefreshCredentials()
    vi.mocked(fetch).mockResolvedValueOnce(new Response(null, { status: 503 }))

    expect(await refreshAccess()).toBe(false)

    expect(localStorage.getItem('twillingate.refresh_token')).toBe('refresh-1')
    expect(localStorage.getItem('twillingate.client_id')).toBe('client-1')
  })

  it('treats a 200 with a non-JSON body as a failed refresh, not a thrown SyntaxError', async () => {
    storeRefreshCredentials()
    vi.mocked(fetch).mockResolvedValueOnce(new Response('not json', { status: 200 }))

    await expect(refreshAccess()).resolves.toBe(false)

    expect(getAuthHeader()).toBeUndefined()
    expect(localStorage.getItem('twillingate.refresh_token')).toBe('refresh-1')
    expect(localStorage.getItem('twillingate.client_id')).toBe('client-1')
  })
})

describe('reportUnauthorized', () => {
  it('routes to /app/login with the current in-app path as returnTo, by default', () => {
    window.history.pushState({}, '', '/app/dashboards/7')
    const assign = mockNavigate()

    reportUnauthorized()

    expect(assign).toHaveBeenCalledWith('/app/login?returnTo=%2Fdashboards%2F7')
  })

  it('falls back to "/" outside of /app', () => {
    window.history.pushState({}, '', '/')
    const assign = mockNavigate()

    reportUnauthorized()

    expect(assign).toHaveBeenCalledWith('/app/login?returnTo=%2F')
  })

  it('lets the app override the default handler', () => {
    const handler = vi.fn()
    onUnauthorized(handler)

    reportUnauthorized()

    expect(handler).toHaveBeenCalledOnce()
  })
})
