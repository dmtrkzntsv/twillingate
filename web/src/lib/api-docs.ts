import { getAuthHeader, refreshAccess } from './auth'

/** The REST API's OpenAPI document (internal/api/openapi.go). */
export const OPENAPI_URL = '/api/openapi.json'

/** The part of a Swagger UI request an interceptor reads and signs. */
export interface SwaggerRequest {
  url: string
  headers: Record<string, string>
}

/**
 * Signs a Swagger UI Try-it-out request with the app's own login, the way
 * `api()` does, unless the Authorize dialog already set a token. After a
 * page load the access token is only in memory once refreshed, so it
 * refreshes first. The document itself is public and goes unsigned.
 */
export async function withAppAuth(req: SwaggerRequest): Promise<SwaggerRequest> {
  if (req.headers.Authorization || new URL(req.url, window.location.href).pathname === OPENAPI_URL) return req
  if (!getAuthHeader()) await refreshAccess()
  const auth = getAuthHeader()
  if (auth) req.headers.Authorization = auth
  return req
}
