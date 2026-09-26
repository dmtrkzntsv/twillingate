import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as auth from './auth'
import { ApiError, api } from './api'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn())
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
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
