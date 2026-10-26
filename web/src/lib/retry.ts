import { ApiError } from './api'

/**
 * Whether a failed query is tried again (TanStack Query's `retry`). A 4xx is
 * the API rejecting the request itself (bad params, not found, unauthorized,
 * a busy SQL engine's 429): retrying it verbatim just repeats the same
 * failure, and a busy engine is asked again by the person, from the card. A
 * timeout (code timeout, 504) is a query that ran its whole
 * CONSOLE_QUERY_TIMEOUT: another run would cost as much and most likely time
 * out again, so the card says so at once and offers Retry. Other 5xx (a
 * proxy's own 504 among them) and network errors get up to three tries.
 */
export function shouldRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && ((error.status >= 400 && error.status < 500) || error.code === 'timeout')) return false
  return failureCount < 3
}
