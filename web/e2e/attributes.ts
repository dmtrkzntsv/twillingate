import { expect, type APIRequestContext } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?...).
const TOKEN = 'e2e-token'

/**
 * Posts one product event per entry from a server, as a backend would, each
 * carrying its attributes; a new key reaches the collector within a moment,
 * so a 401 is retried. Returns the ingest answer (warnings included).
 */
export async function trackWithAttributes(
  request: APIRequestContext,
  key: string,
  events: { name: string; attributes: Record<string, string> }[]
): Promise<{ accepted: number; rejected: number; warnings?: { index: number; reason: string }[] | null }> {
  let result: { accepted: number; rejected: number; warnings?: { index: number; reason: string }[] | null } | undefined
  await expect(async () => {
    const res = await request.post('/ingest/events', {
      headers: { 'X-Analytics-Key': key },
      data: {
        events: events.map((e) => ({ id: crypto.randomUUID(), ts: new Date().toISOString(), family: 'product', ...e })),
      },
    })
    expect(res.status(), await res.text()).toBe(202)
    result = await res.json()
  }).toPass({ timeout: 15_000 })
  return result!
}

/**
 * Waits for the write buffer to flush (every few seconds): the project's
 * attributes today list every one of `keys`. A key sent today is listed as
 * received with no counts until the daily pass counts it.
 */
export async function attributesListed(request: APIRequestContext, id: number, keys: string[]): Promise<void> {
  const today = new Date().toISOString().slice(0, 10)
  await expect
    .poll(
      async () => {
        const res = await request.get(`/api/projects/${id}/attributes?from=${today}&to=${today}`, { headers: { Authorization: `Bearer ${TOKEN}` } })
        expect(res.ok(), await res.text()).toBeTruthy()
        const { attributes } = (await res.json()) as { attributes: { key: string }[] }
        return keys.filter((k) => !attributes.some((a) => a.key === k))
      },
      { timeout: 30_000 }
    )
    .toEqual([])
}
