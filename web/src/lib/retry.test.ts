import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import { shouldRetry } from './retry'

describe('shouldRetry', () => {
  it('never retries a refusal, a busy engine or a timeout', () => {
    for (const [status, code] of [
      [400, 'invalid'],
      [404, 'not_found'],
      [409, 'conflict'],
      [429, 'busy'],
      [504, 'timeout'],
    ] as const) {
      expect(shouldRetry(0, new ApiError(status, 'x', code))).toBe(false)
    }
  })

  it('retries other server and network errors up to three times', () => {
    for (const error of [new ApiError(500, 'boom', 'internal'), new ApiError(503, 'down'), new TypeError('Failed to fetch')]) {
      expect(shouldRetry(0, error)).toBe(true)
      expect(shouldRetry(2, error)).toBe(true)
      expect(shouldRetry(3, error)).toBe(false)
    }
  })
})
