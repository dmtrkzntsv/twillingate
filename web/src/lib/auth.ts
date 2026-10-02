// Login against the API's own token:// OAuth server (D41): discovery via
// RFC 9728/8414, dynamic client registration, then an authorization-code
// PKCE flow. See internal/api/oauth*.go for the server side.

const METADATA_PATH = '/.well-known/oauth-protected-resource'
const CALLBACK_PATH = '/app/callback'
const CLIENT_NAME = 'Twillingate Analytics'

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

interface OAuthErrorBody {
  error?: string
}

// The access token lives only in memory; a page reload falls back to the
// refresh token (oauth) or the pasted token (paste), both in localStorage.
let accessToken: string | null = null

/**
 * The host of the authorization server discovered by the last `detectAuth`
 * call that fell back to "paste" because it has no registration endpoint —
 * so the paste screen can name it.
 */
let providerHint: string | undefined

export function authProviderHint(): string | undefined {
  return providerHint
}

/**
 * Keeps a `returnTo` an in-app path: never an absolute URL, a
 * protocol-relative one (`//evil.com`), or a backslash form a browser's URL
 * parser may normalize into one (`/\evil.com`, `/\/evil.com`) before it ever
 * reaches the router.
 */
export function sanitizeReturnTo(v: string | null | undefined): string {
  if (!v || !v.startsWith('/') || v.includes('\\')) return '/'
  if (v[1] === '/') return '/'
  return v
}

/**
 * Where the app is now, inside its `/app` base and with its query string:
 * the page to come back to after logging in.
 */
export function currentAppPath(): string {
  const path = window.location.pathname.replace(/^\/app(?=\/|$)/, '') || '/'
  return sanitizeReturnTo(path + window.location.search)
}

function defaultUnauthorized(): void {
  window.location.assign(`/app/login?returnTo=${encodeURIComponent(currentAppPath())}`)
}

let unauthorized: () => void = defaultUnauthorized

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

const AS_METADATA_PATH = '/.well-known/oauth-authorization-server'

// onThisOrigin moves an endpoint URL onto the page's own origin.
function onThisOrigin(endpoint: string): string {
  const u = new URL(endpoint, location.origin)
  return location.origin + u.pathname + u.search
}

// A token:// backend's login server is mounted on every name that reaches
// it, so when this origin serves login metadata the page logs in here, even
// if the issuer names another host: a cross-origin fetch to that host would
// fail. The server accepts this origin's /app/callback when it is the
// resource origin or a redirect= host in CONSOLE_AUTH_DSN. Without metadata
// here (an oauth:// identity provider), the page follows the issuer.
async function discover(): Promise<{ resource: ProtectedResource; meta: AuthServerMetadata } | undefined> {
  const resource = await fetchJSON<ProtectedResource>(METADATA_PATH)
  const asURL = resource?.authorization_servers?.[0]
  if (!resource || !asURL) return undefined
  const own = await fetchJSON<AuthServerMetadata>(AS_METADATA_PATH)
  if (own) {
    return {
      resource,
      meta: {
        ...own,
        authorization_endpoint: onThisOrigin(own.authorization_endpoint),
        token_endpoint: onThisOrigin(own.token_endpoint),
        registration_endpoint: own.registration_endpoint && onThisOrigin(own.registration_endpoint),
      },
    }
  }
  const meta = await fetchJSON<AuthServerMetadata>(`${asURL}${AS_METADATA_PATH}`)
  if (!meta) return undefined
  return { resource, meta }
}

function hostOf(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

/**
 * Which login path the client should take (D41): "open" needs no
 * credentials at all (reporting dev), "paste" is a bare bearer token typed
 * in, "login" is the full PKCE flow against a discovered authorization
 * server. When it falls back to "paste" because the server has no
 * registration endpoint, `authProviderHint()` names the server so the UI
 * can say why.
 */
export async function detectAuth(): Promise<'open' | 'login' | 'paste'> {
  providerHint = undefined
  const open = await fetch('/api/dashboards')
  if (open.ok) return 'open'
  const found = await discover()
  if (!found) return 'paste'
  if (!found.meta.registration_endpoint) {
    providerHint = hostOf(found.meta.issuer || found.resource.authorization_servers[0])
    return 'paste'
  }
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

// Client ids are JWTs signed with keys derived from the DSN's token and
// password (internal/api/oauth.go): rotating either strands a cached id on
// the server's "Unknown client" page. Registration is stateless server-side,
// so beginLogin always re-registers; the id is still kept in localStorage
// for refreshAccess, which runs long after the login page is gone.
async function registerClient(meta: AuthServerMetadata): Promise<string> {
  if (!meta.registration_endpoint) {
    const existing = localStorage.getItem(CLIENT_ID_KEY)
    if (existing) return existing
    throw new Error('the authorization server has no registration endpoint')
  }
  const res = await fetch(meta.registration_endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      redirect_uris: [redirectURI()],
      token_endpoint_auth_method: 'none',
      grant_types: ['authorization_code', 'refresh_token'],
      client_name: CLIENT_NAME,
    }),
  })
  // A refusal names its fix (a page opened on a host the login server does
  // not know says to add it as redirect=<host>), so show it rather than a
  // bare "failed".
  const body = (await res.json().catch(() => undefined)) as
    | { client_id?: string; error_description?: string }
    | undefined
  if (!res.ok || !body?.client_id) {
    throw new Error(body?.error_description || 'client registration failed')
  }
  const registration = { client_id: body.client_id }
  localStorage.setItem(CLIENT_ID_KEY, registration.client_id)
  return registration.client_id
}

/** Starts the PKCE flow: (re-)register, then redirect to authorize. */
export async function beginLogin(returnTo: string): Promise<void> {
  const found = await discover()
  if (!found) throw new Error('no authorization server found')
  const { resource, meta } = found
  const clientId = await registerClient(meta)

  const verifier = randomToken(32)
  const state = randomToken(16)
  sessionStorage.setItem(VERIFIER_KEY, verifier)
  sessionStorage.setItem(STATE_KEY, state)
  sessionStorage.setItem(RETURN_TO_KEY, sanitizeReturnTo(returnTo))
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

async function exchangeCode(search: string): Promise<string> {
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
  const returnTo = sanitizeReturnTo(sessionStorage.getItem(RETURN_TO_KEY))
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

// React StrictMode (dev) mounts effects twice, and the callback page reruns
// the same effect with the same location.search both times. The code and
// state are single-use, so the second run would find them already cleared
// and fail even though the first succeeded; memoising by `search` makes the
// second call join the first instead of repeating it.
let lastExchange: { search: string; result: Promise<string> } | null = null

/** Exchanges the callback's code for tokens, returning where to send the user. */
export function completeLogin(search: string): Promise<string> {
  if (!lastExchange || lastExchange.search !== search) {
    lastExchange = { search, result: exchangeCode(search) }
  }
  return lastExchange.result
}

/** A bare bearer token pasted in by hand (the "paste" path). */
export function setPastedToken(t: string): void {
  localStorage.setItem(PASTED_TOKEN_KEY, t)
}

// A rotated signing key (or a stale/foreign client id) turns every refresh
// into invalid_client or invalid_grant forever; clearing the credentials
// sends the user back through a fresh login instead of failing silently on
// every request.
async function clearOnRejection(res: Response): Promise<void> {
  let code: string | undefined
  try {
    code = ((await res.json()) as OAuthErrorBody).error
  } catch {
    // Not a JSON OAuth error body; nothing more to learn from it.
  }
  if (code === 'invalid_client' || code === 'invalid_grant') {
    accessToken = null
    localStorage.removeItem(REFRESH_TOKEN_KEY)
    localStorage.removeItem(CLIENT_ID_KEY)
  }
}

async function exchangeRefresh(): Promise<boolean> {
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
  if (!res.ok) {
    await clearOnRejection(res)
    return false
  }
  let token: TokenResponse
  try {
    token = (await res.json()) as TokenResponse
  } catch {
    // A 200 with a body that isn't JSON is not a usable token response;
    // treat it as a failed refresh rather than let the SyntaxError escape.
    return false
  }
  accessToken = token.access_token
  if (token.refresh_token) localStorage.setItem(REFRESH_TOKEN_KEY, token.refresh_token)
  return true
}

// Single-flight: concurrent 401s (e.g. several widgets loading at once)
// share one refresh instead of each redeeming the refresh token, which
// would race the token's single-use rotation and fail all but one of them.
let refreshInFlight: Promise<boolean> | null = null

/**
 * Redeems the stored refresh token for a new access token. Used by `api()`
 * after a 401 (at most once per failure, shared across concurrent callers);
 * returns false with nothing to refresh (open or paste mode) or on failure.
 */
export function refreshAccess(): Promise<boolean> {
  if (!refreshInFlight) {
    refreshInFlight = exchangeRefresh().finally(() => {
      refreshInFlight = null
    })
  }
  return refreshInFlight
}

/**
 * Forgets every credential this app holds: the in-memory access token and
 * every `twillingate.*` key in localStorage (refresh token, pasted token,
 * client id, endpoints). The identity provider's own session, if any, is
 * its to end.
 */
export function logout(): void {
  accessToken = null
  const keys: string[] = []
  for (let i = 0; i < localStorage.length; i++) {
    const k = localStorage.key(i)
    if (k?.startsWith('twillingate.')) keys.push(k)
  }
  keys.forEach((k) => localStorage.removeItem(k))
}

/** Reports the failure that made `api()` give up after one refresh attempt. */
export function reportUnauthorized(): void {
  unauthorized()
}

/** Test-only: clears in-memory state that would otherwise leak between cases. */
export function _resetForTests(): void {
  accessToken = null
  providerHint = undefined
  refreshInFlight = null
  lastExchange = null
  unauthorized = defaultUnauthorized
}
