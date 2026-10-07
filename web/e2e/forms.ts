import { expect, type APIRequestContext } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?...).
const TOKEN = 'e2e-token'

/** A fresh project with these origins and its first key, through the console API. */
export async function createProject(
  request: APIRequestContext,
  name: string,
  allowedOrigins: string[]
): Promise<{ id: number; key: string }> {
  const res = await request.post('/api/projects', {
    headers: { Authorization: `Bearer ${TOKEN}` },
    data: { name, allowed_origins: allowedOrigins },
  })
  expect(res.ok(), await res.text()).toBeTruthy()
  const body = (await res.json()) as { project_id: number; key: string }
  return { id: body.project_id, key: body.key }
}

/**
 * Sends one JSON submission to the project's form `name` from a server (no
 * Origin), as a backend would; the first one creates the form as a draft.
 * A new key reaches the collector within a moment, so a 401 is retried.
 */
export async function submitJSON(request: APIRequestContext, key: string, name: string, fields: Record<string, string>): Promise<void> {
  await expect(async () => {
    const res = await request.post(`/ingest/forms/${name}`, {
      headers: { 'X-Analytics-Key': key, 'Content-Type': 'application/json' },
      data: JSON.stringify({ fields }),
    })
    expect(res.status(), await res.text()).toBe(201)
  }).toPass({ timeout: 15_000 })
}
