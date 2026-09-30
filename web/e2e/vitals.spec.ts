import { expect, test, type Request } from '@playwright/test'

// Matches web/e2e/serve.sh's API_AUTH_DSN (token://e2e-token?password=e2e-pass&...),
// the same token app.spec.ts uses for the REST API.
const TOKEN = 'e2e-token'
const ORIGIN = 'http://127.0.0.1:18080'

function authHeaders() {
  return { Authorization: `Bearer ${TOKEN}` }
}

interface WireEvent {
  family?: string
  name: string
  value?: number
  measure?: string
  attributes?: Record<string, unknown>
}
interface WireBody {
  attributes?: Record<string, unknown>
  events: WireEvent[]
}

/** One measures event, with batch attributes merged under its own (docs/twillingate.md "Attribute merge"). */
interface Measure {
  name: string
  measure?: string
  attributes: Record<string, unknown>
}

/** Every measures event across the collected /ingest/events request bodies. */
function measures(bodies: WireBody[]): Measure[] {
  return bodies.flatMap((body) =>
    body.events
      .filter((e) => e.family === 'measures')
      .map((e) => ({ name: e.name, measure: e.measure, attributes: { ...body.attributes, ...e.attributes } })),
  )
}

test('Web Vitals reach the collector from a real browser', async ({ page, request }) => {
  // Issue a fresh ingest key for the seeded "dev" project (id 1; see
  // docs/twillingate.md "### HTTP API") and allow the page origin this
  // test serves from, in case the project restricts origins.
  const keyRes = await request.post('/api/projects/1/keys', {
    headers: authHeaders(),
    data: { label: 'vitals-e2e' },
  })
  expect(keyRes.ok(), await keyRes.text()).toBeTruthy()
  const { key } = (await keyRes.json()) as { key: string }
  expect(key).toBeTruthy()

  const originRes = await request.patch('/api/projects/1', {
    headers: authHeaders(),
    data: { allowed_origins: [ORIGIN] },
  })
  expect(originRes.ok(), await originRes.text()).toBeTruthy()

  // Every request the page sends to the ingest endpoint, fetch or beacon
  // alike — a vital reported while the page is hidden is sent immediately
  // through sendBeacon, which still shows up here as a normal request.
  const bodies: WireBody[] = []
  page.on('request', (req: Request) => {
    if (req.method() !== 'POST' || !req.url().endsWith('/ingest/events')) return
    const data = req.postData()
    if (!data) return
    bodies.push(JSON.parse(data) as WireBody)
  })

  await page.route(`${ORIGIN}/vitals-test`, (r) =>
    r.fulfill({
      contentType: 'text/html',
      body: `<html><body><h1>Vitals</h1><button id="b">tap</button><script src="/js/twillingate.js" data-key="${key}" data-vitals="1"></script></body></html>`,
    }),
  )
  await page.goto(`${ORIGIN}/vitals-test`)

  // A real interaction, for INP. A click alone was not always enough to
  // make Chromium report INP under Playwright's synthetic input, so this
  // also presses a key on the button (see commit body).
  await page.click('#b')
  await page.focus('#b')
  await page.keyboard.press(' ')

  // LCP, CLS and INP are only reported once the page goes hidden.
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
  })

  const expectedMeasureKind: Record<string, string> = {
    $lcp: 'time',
    $fcp: 'time',
    $ttfb: 'time',
    $cls: 'number',
    $inp: 'time',
  }

  await expect
    .poll(() => measures(bodies).map((m) => m.name).sort(), { timeout: 15_000 })
    .toEqual(['$cls', '$fcp', '$inp', '$lcp', '$ttfb'])

  for (const m of measures(bodies)) {
    expect(m.measure, `${m.name} measure kind`).toBe(expectedMeasureKind[m.name])
    expect(m.attributes.$sample_rate, `${m.name} $sample_rate`).toBe(1)
  }
})
