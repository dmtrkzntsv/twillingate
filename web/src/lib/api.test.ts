import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as auth from './auth'
import { ApiError, api, endpoints } from './api'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  auth._resetForTests()
  vi.stubGlobal('fetch', vi.fn())
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function urlOf(input: RequestInfo | URL): string {
  return typeof input === 'string' ? input : input.toString()
}

describe('endpoints.renameGroup', () => {
  it('PATCHes the dashboard with whole_group and the new name', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ dashboard_id: 7, group_title: 'Ops' }))

    await endpoints.renameGroup(7, 'Ops')

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(urlOf(url)).toContain('/api/dashboards/7')
    expect(init?.method).toBe('PATCH')
    expect(JSON.parse(init?.body as string)).toEqual({ whole_group: true, title: 'Ops' })
  })
})

describe('endpoints.createWidgetShare', () => {
  it('POSTs the form as it is, with no Content-Type so the browser adds the multipart boundary', async () => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue('Bearer token-1')
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ id: 'abc' }))
    const form = new FormData()
    form.set('widget_id', '7')

    await endpoints.createWidgetShare(form)

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(urlOf(url)).toBe('/api/widget-shares')
    expect(init?.method).toBe('POST')
    expect(init?.body).toBe(form)
    const headers = new Headers(init?.headers)
    expect(headers.get('Content-Type')).toBeNull()
    expect(headers.get('Authorization')).toBe('Bearer token-1')
  })
})

describe('endpoints.widgetShareImage', () => {
  it('GETs the share image route with the token and returns the PNG as a Blob', async () => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue('Bearer token-1')
    vi.mocked(fetch).mockResolvedValueOnce(new Response('png', { status: 200, headers: { 'Content-Type': 'image/png' } }))

    const blob = await endpoints.widgetShareImage('abc')

    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(urlOf(url)).toBe('/api/widget-shares/abc/image')
    expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer token-1')
    expect(blob.size).toBe(3)
    expect(blob.type).toBe('image/png')
  })

  it('refreshes once on a 401, like every API call, and throws an ApiError on a 404', async () => {
    vi.spyOn(auth, 'refreshAccess').mockResolvedValue(true)
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({ error: { code: 'unauthorized', message: 'no' } }, 401))
      .mockResolvedValueOnce(jsonResponse({ error: { code: 'not_found', message: 'share not found' } }, 404))

    await expect(endpoints.widgetShareImage('abc')).rejects.toMatchObject({ status: 404, code: 'not_found' })
    expect(fetch).toHaveBeenCalledTimes(2)
  })
})

describe('api', () => {
  it('adds the Authorization header when there is one', async () => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue('Bearer token-1')
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ ok: true }))

    await api('/api/dashboards')

    const [, init] = vi.mocked(fetch).mock.calls[0]
    expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer token-1')
  })

  it('sends no Authorization header in open mode', async () => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue(undefined)
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ ok: true }))

    await api('/api/dashboards')

    const [, init] = vi.mocked(fetch).mock.calls[0]
    expect(new Headers(init?.headers).get('Authorization')).toBeNull()
  })

  it('refreshes once on a 401 and retries the request', async () => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue('Bearer stale')
    vi.spyOn(auth, 'refreshAccess').mockResolvedValue(true)
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(jsonResponse({ ok: true }))

    const result = await api<{ ok: boolean }>('/api/dashboards')

    expect(result).toEqual({ ok: true })
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('throws an ApiError with status 401 when the retry also fails', async () => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue('Bearer stale')
    vi.spyOn(auth, 'refreshAccess').mockResolvedValue(true)
    const reportUnauthorized = vi.spyOn(auth, 'reportUnauthorized').mockImplementation(() => {})
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(
        jsonResponse({ error: { code: 'invalid', message: 'token rejected' } }, 401)
      )

    await expect(api('/api/dashboards')).rejects.toMatchObject({ status: 401, code: 'invalid' })
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(reportUnauthorized).toHaveBeenCalledOnce()
  })

  it('throws an ApiError without retrying when refresh is unavailable', async () => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue(undefined)
    vi.spyOn(auth, 'refreshAccess').mockResolvedValue(false)
    vi.spyOn(auth, 'reportUnauthorized').mockImplementation(() => {})
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ error: { code: 'invalid', message: 'no token' } }, 401)
    )

    await expect(api('/api/dashboards')).rejects.toBeInstanceOf(ApiError)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('shares one refresh request across concurrent 401s (no refresh stampede)', async () => {
    // Exercises the real refreshAccess, not a mock, since the single-flight
    // guarantee lives there.
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue('Bearer stale')
    localStorage.setItem('twillingate.refresh_token', 'refresh-1')
    localStorage.setItem('twillingate.token_endpoint', 'https://api.example/oauth/token')
    localStorage.setItem('twillingate.resource', 'https://api.example')
    localStorage.setItem('twillingate.client_id', 'client-1')

    let dashboardCalls = 0
    vi.mocked(fetch).mockImplementation((input) => {
      if (urlOf(input) === 'https://api.example/oauth/token') {
        return Promise.resolve(jsonResponse({ access_token: 'access-2', refresh_token: 'refresh-2' }))
      }
      dashboardCalls++
      // The first request from each of the two concurrent callers 401s;
      // once both have retried after the shared refresh, they succeed.
      return Promise.resolve(dashboardCalls <= 2 ? new Response(null, { status: 401 }) : jsonResponse({ ok: true }))
    })

    const [a, b] = await Promise.all([
      api<{ ok: boolean }>('/api/dashboards'),
      api<{ ok: boolean }>('/api/dashboards'),
    ])

    expect(a).toEqual({ ok: true })
    expect(b).toEqual({ ok: true })
    const tokenCalls = vi.mocked(fetch).mock.calls.filter(([input]) => urlOf(input) === 'https://api.example/oauth/token')
    expect(tokenCalls).toHaveLength(1)
    expect(fetch).toHaveBeenCalledTimes(5) // 2 initial 401s + 1 refresh + 2 retries
  })

  it('throws an ApiError for a non-401 error response', async () => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue(undefined)
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({ error: { code: 'not_found', message: 'no such dashboard' } }, 404)
    )

    const error = await api('/api/dashboards/1').catch((e) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 404, code: 'not_found', message: 'no such dashboard' })
  })
})

describe('project endpoints', () => {
  beforeEach(() => {
    vi.spyOn(auth, 'getAuthHeader').mockReturnValue(undefined)
  })

  it('encodes a key label with spaces and slashes in the path', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ status: 'disabled' }))
    await endpoints.disableKey(7, 'web / staging 100%')
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/projects/7/keys/web%20%2F%20staging%20100%25/disable')
  })

  it('sends only the fields given to update', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ project_id: 7 }))
    await endpoints.updateProject(7, { allowed_origins: [] })
    const init = vi.mocked(fetch).mock.calls[0][1] as RequestInit
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body as string)).toEqual({ allowed_origins: [] })
  })

  it('asks for usage with the query it was given', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [] }))
    await endpoints.usage({ project_id: 4, from: '2026-09-01', to: '2026-09-30' })
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/usage?project_id=4&from=2026-09-01&to=2026-09-30')
  })
})
