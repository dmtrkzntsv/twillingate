import { afterEach, describe, expect, it, vi } from 'vitest'
import { withAppAuth } from './api-docs'
import { _resetForTests, setPastedToken } from './auth'

afterEach(() => {
  localStorage.clear()
  _resetForTests()
  vi.restoreAllMocks()
})

describe('withAppAuth', () => {
  it("signs a request with the app's token", async () => {
    setPastedToken('pasted')
    const req = await withAppAuth({ url: '/api/projects', headers: {} })
    expect(req.headers.Authorization).toBe('Bearer pasted')
  })

  it('keeps a token set in the Authorize dialog', async () => {
    setPastedToken('pasted')
    const req = await withAppAuth({ url: '/api/projects', headers: { Authorization: 'Bearer typed' } })
    expect(req.headers.Authorization).toBe('Bearer typed')
  })

  it('leaves the public document unsigned', async () => {
    setPastedToken('pasted')
    const req = await withAppAuth({ url: 'http://localhost:3000/api/openapi.json', headers: {} })
    expect(req.headers.Authorization).toBeUndefined()
  })

  it('sends a request unsigned when there is no login, after trying to refresh', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch')
    const req = await withAppAuth({ url: '/api/projects', headers: {} })
    expect(req.headers.Authorization).toBeUndefined()
    expect(fetch).not.toHaveBeenCalled() // nothing stored to refresh with
  })
})
