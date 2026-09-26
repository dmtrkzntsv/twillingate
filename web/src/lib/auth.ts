// Login against the API's own token:// OAuth server (D41): discovery via
// RFC 9728/8414, dynamic client registration, then an authorization-code
// PKCE flow. See internal/api/oauth*.go for the server side.

const METADATA_PATH = '/.well-known/oauth-protected-resource'
const CALLBACK_PATH = '/app/callback'

const CLIENT_ID_KEY = 'twillingate.client_id'
const REFRESH_TOKEN_KEY = 'twillingate.refresh_token'
const PASTED_TOKEN_KEY = 'twillingate.token'
const TOKEN_ENDPOINT_KEY = 'twillingate.token_endpoint'
const RESOURCE_KEY = 'twillingate.resource'
const VERIFIER_KEY = 'twillingate.oauth_verifier'
const STATE_KEY = 'twillingate.oauth_state'
const RETURN_TO_KEY = 'twillingate.oauth_return_to'

/** What is currently authenticating the app. */
export type AuthState = { kind: 'none' } | { kind: 'token' } | { kind: 'oauth' }

interface ProtectedResource {
  resource: string
  authorization_servers: string[]
}

interface AuthServerMetadata {
  issuer: string
  authorization_endpoint: string
  token_endpoint: string
  registration_endpoint?: string
}

interface TokenResponse {
  access_token: string
  refresh_token?: string
}

// The access token lives only in memory; a page reload falls back to the
// refresh token (oauth) or the pasted token (paste), both in localStorage.
let accessToken: string | null = null

let unauthorized: () => void = () => {
  window.location.assign('/app/login')
}

/** Lets the app route to /login itself instead of a hard navigation. */
export function onUnauthorized(handler: () => void): void {
  unauthorized = handler
}

/** The current Authorization header value, if any (open mode has none). */
export function getAuthHeader(): string | undefined {
  const token = accessToken ?? localStorage.getItem(PASTED_TOKEN_KEY)
  return token ? `Bearer ${token}` : undefined
}

export function currentAuthState(): AuthState {
  if (accessToken || localStorage.getItem(REFRESH_TOKEN_KEY)) return { kind: 'oauth' }
  if (localStorage.getItem(PASTED_TOKEN_KEY)) return { kind: 'token' }
  return { kind: 'none' }
}

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T | undefined> {
  const res = await fetch(url, init)
  if (!res.ok) return undefined
  return (await res.json()) as T
}

async function discover(): Promise<{ resource: ProtectedResource; meta: AuthServerMetadata } | undefined> {
  const resource = await fetchJSON<ProtectedResource>(METADATA_PATH)
  const asURL = resource?.authorization_servers?.[0]
  if (!resource || !asURL) return undefined
  const meta = await fetchJSON<AuthServerMetadata>(`${asURL}/.well-known/oauth-authorization-server`)
  if (!meta) return undefined
  return { resource, meta }
}

/**
 * Which login path the client should take (D41): "open" needs no
 * credentials at all (reporting dev), "paste" is a bare bearer token typed
 * in, "login" is the full PKCE flow against a discovered authorization
 * server.
 */
export async function detectAuth(): Promise<'open' | 'login' | 'paste'> {
  const open = await fetch('/api/dashboards')
  if (open.ok) return 'open'
  const found = await discover()
  if (!found || !found.meta.registration_endpoint) return 'paste'
  return 'login'
}

function base64url(bytes: Uint8Array): string {
  let binary = ''
  bytes.forEach((b) => {
    binary += String.fromCharCode(b)
  })
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function randomToken(byteLength: number): string {
  return base64url(crypto.getRandomValues(new Uint8Array(byteLength)))
}

async function challengeFor(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))
  return base64url(new Uint8Array(digest))
}

function redirectURI(): string {
  return location.origin + CALLBACK_PATH
}

async function registerClient(meta: AuthServerMetadata): Promise<string> {
  const existing = localStorage.getItem(CLIENT_ID_KEY)
  if (existing) return existing
  if (!meta.registration_endpoint) throw new Error('the authorization server has no registration endpoint')
  const registration = await fetchJSON<{ client_id: string }>(meta.registration_endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      redirect_uris: [redirectURI()],
      token_endpoint_auth_method: 'none',
      grant_types: ['authorization_code', 'refresh_token'],
    }),
  })
  if (!registration) throw new Error('client registration failed')
  localStorage.setItem(CLIENT_ID_KEY, registration.client_id)
  return registration.client_id
}

/** Starts the PKCE flow: register if needed, then redirect to authorize. */
export async function beginLogin(returnTo: string): Promise<void> {
  const found = await discover()
  if (!found) throw new Error('no authorization server found')
  const { resource, meta } = found
  const clientId = await registerClient(meta)

  const verifier = randomToken(32)
  const state = randomToken(16)
  sessionStorage.setItem(VERIFIER_KEY, verifier)
  sessionStorage.setItem(STATE_KEY, state)
  sessionStorage.setItem(RETURN_TO_KEY, returnTo)
  localStorage.setItem(TOKEN_ENDPOINT_KEY, meta.token_endpoint)
  localStorage.setItem(RESOURCE_KEY, resource.resource)

  const url = new URL(meta.authorization_endpoint)
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('client_id', clientId)
  url.searchParams.set('redirect_uri', redirectURI())
  url.searchParams.set('code_challenge', await challengeFor(verifier))
  url.searchParams.set('code_challenge_method', 'S256')
  url.searchParams.set('state', state)
  url.searchParams.set('resource', resource.resource)
  window.location.assign(url.toString())
}

function clearPendingLogin(): void {
  sessionStorage.removeItem(VERIFIER_KEY)
  sessionStorage.removeItem(STATE_KEY)
  sessionStorage.removeItem(RETURN_TO_KEY)
}

/** Exchanges the callback's code for tokens, returning where to send the user. */
export async function completeLogin(search: string): Promise<string> {
  const params = new URLSearchParams(search)
  const error = params.get('error')
  if (error) {
    clearPendingLogin()
    throw new Error(`login failed: ${error}`)
  }
  const code = params.get('code')
  const state = params.get('state')
  const expectedState = sessionStorage.getItem(STATE_KEY)
  if (!code || !state || !expectedState || state !== expectedState) {
    clearPendingLogin()
    throw new Error('login state does not match: start again from the client')
  }
  const verifier = sessionStorage.getItem(VERIFIER_KEY)
  const tokenEndpoint = localStorage.getItem(TOKEN_ENDPOINT_KEY)
  const resource = localStorage.getItem(RESOURCE_KEY)
  const clientId = localStorage.getItem(CLIENT_ID_KEY)
  const returnTo = sessionStorage.getItem(RETURN_TO_KEY) ?? '/'
  clearPendingLogin()
  if (!verifier || !tokenEndpoint || !resource || !clientId) {
    throw new Error('login session expired: start again from the client')
  }

  const body = new URLSearchParams({
    grant_type: 'authorization_code',
    code,
    redirect_uri: redirectURI(),
    client_id: clientId,
    code_verifier: verifier,
    resource,
  })
  const res = await fetch(tokenEndpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: body.toString(),
  })
  if (!res.ok) throw new Error('token exchange failed')
  const token = (await res.json()) as TokenResponse
  accessToken = token.access_token
  if (token.refresh_token) localStorage.setItem(REFRESH_TOKEN_KEY, token.refresh_token)
  return returnTo
}

/** A bare bearer token pasted in by hand (the "paste" path). */
export function setPastedToken(t: string): void {
  localStorage.setItem(PASTED_TOKEN_KEY, t)
}

/**
 * Redeems the stored refresh token for a new access token. Used once by
 * `api()` after a 401; returns false when there is nothing to refresh with
 * (open or paste mode, or the refresh itself failing).
 */
export async function refreshAccess(): Promise<boolean> {
  const refreshToken = localStorage.getItem(REFRESH_TOKEN_KEY)
  const tokenEndpoint = localStorage.getItem(TOKEN_ENDPOINT_KEY)
  const resource = localStorage.getItem(RESOURCE_KEY)
  const clientId = localStorage.getItem(CLIENT_ID_KEY)
  if (!refreshToken || !tokenEndpoint || !resource || !clientId) return false

  const body = new URLSearchParams({
    grant_type: 'refresh_token',
    refresh_token: refreshToken,
    client_id: clientId,
    resource,
  })
  const res = await fetch(tokenEndpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: body.toString(),
  })
  if (!res.ok) return false
  const token = (await res.json()) as TokenResponse
  accessToken = token.access_token
  if (token.refresh_token) localStorage.setItem(REFRESH_TOKEN_KEY, token.refresh_token)
  return true
}

/** Reports the failure that made `api()` give up after one refresh attempt. */
export function reportUnauthorized(): void {
  unauthorized()
}

/** Test-only: clears the in-memory access token between cases. */
export function _resetForTests(): void {
  accessToken = null
}
