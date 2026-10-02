import { expect, test, type Request, type Response } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...),
// the same token app.spec.ts uses for the REST API.
const TOKEN = 'e2e-token'
const ORIGIN = 'http://127.0.0.1:18080'

// The collector drops web views and web measures from a crawler User-Agent
// (enrich.IsBot: "headless", "lighthouse", ...). Pin a normal desktop Chrome
// here, whatever the project's device default, so the vitals are stored.
test.use({
  userAgent:
    'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36',
})

interface IngestResult {
  accepted: number
  rejected: number
  errors: unknown[] | null
}

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
  // test serves from, in case the project restricts origins. Labels are
  // unique per project, so each run (a retry, a repeat) takes its own.
  const keyRes = await request.post('/api/projects/1/keys', {
    headers: authHeaders(),
    data: { label: `vitals-e2e-${test.info().testId}-${test.info().repeatEachIndex}-${test.info().retry}` },
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
  // And every answer: each must be a 202 with no per-event errors, so a
  // rejected vital fails here rather than only by its absence.
  const results: { status: number; body: IngestResult }[] = []
  page.on('response', async (res: Response) => {
    if (res.request().method() !== 'POST' || !res.url().endsWith('/ingest/events')) return
    results.push({ status: res.status(), body: (await res.json()) as IngestResult })
  })

  await page.route(`${ORIGIN}/vitals-test`, (r) =>
    r.fulfill({
      contentType: 'text/html',
      // The page notes its first input with its own observer, so the test
      // can tell when Chromium has recorded the interaction.
      body: `<html><body><h1>Vitals</h1><button id="b">tap</button><script>window.firstInput = false; new PerformanceObserver(() => { window.firstInput = true }).observe({ type: 'first-input', buffered: true })</script><script src="/js/twillingate.js" data-key="${key}" data-vitals="1"></script></body></html>`,
    }),
  )
  await page.goto(`${ORIGIN}/vitals-test`)

  // A real interaction, for INP. Playwright's synthetic click alone does
  // not always make Chromium report INP, so a key press on the button
  // follows.
  await page.click('#b')
  await page.focus('#b')
  await page.keyboard.press(' ')

  // Hide only once web-vitals has processed the interaction. It handles
  // event entries in an idle callback; if the page is hidden first, its
  // forced report runs before that batch and finds no INP, and the batch's
  // own report afterwards is not sent. So wait for the first-input entry,
  // then for two idle periods (web-vitals' idle callback was queued before
  // ours, so it has run by the second).
  await page.waitForFunction(() => (window as unknown as { firstInput: boolean }).firstInput)
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestIdleCallback(() => requestIdleCallback(() => resolve(), { timeout: 2000 }), { timeout: 2000 }),
      ),
  )

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

  await expect.poll(() => results.length, { timeout: 15_000 }).toBe(bodies.length)
  for (const r of results) {
    expect(r.status, JSON.stringify(r.body)).toBe(202)
    expect(r.body.errors ?? [], JSON.stringify(r.body)).toEqual([])
    expect(r.body.rejected, JSON.stringify(r.body)).toBe(0)
  }

  for (const m of measures(bodies)) {
    expect(m.measure, `${m.name} measure kind`).toBe(expectedMeasureKind[m.name])
    expect(m.attributes.$sample_rate, `${m.name} $sample_rate`).toBe(1)
  }
})
