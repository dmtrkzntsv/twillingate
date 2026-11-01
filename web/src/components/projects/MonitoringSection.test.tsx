import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type Monitor, type Monitors } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import MonitoringSection from './MonitoringSection'

const days = (n: number) => new Date(Date.now() + n * 86_400_000 + 3_600_000)

/** The `YYYY-MM-DD` n days after today in UTC, whatever the hour. */
function utcDay(n: number): string {
  const now = new Date()
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + n)).toISOString().slice(0, 10)
}

const A = 'https://a.example'
const B = 'https://b.example'
const C = 'https://c.example'
const SENTENCE = 'Check that the site is up, and when its certificate and domain expire.'
const LINK = 'Also look for text on the page'

function monitor(origin: string, over: Partial<Monitor> = {}): Monitor {
  return {
    origin,
    enabled: false,
    contains: '',
    redirects: 0,
    cert: { expires_at: '', issuer: '', valid: false, error: '', checked_at: '' },
    check: { requested_at: '', finished_at: '', up: false, status: 0, reason: '', error: '', ms: 0 },
    ...over,
  }
}
const on = (origin: string, over: Partial<Monitor> = {}) => monitor(origin, { enabled: true, ...over })

function list(monitors: Monitor[], over: Partial<Monitors> = {}): Monitors {
  const enabled = monitors.filter((m) => m.enabled).length
  return { interval_seconds: 60, timeout_seconds: 5, check_every_seconds: 60 * Math.max(enabled, 1), monitors, domains: [], ...over }
}

function show(data: Monitors, onSave = vi.fn().mockResolvedValue(true), onCheck = vi.fn().mockResolvedValue(true), pending = false) {
  const read = vi.spyOn(endpoints, 'monitors').mockResolvedValue(data)
  vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [{ dashboard_id: 8, title: 'Availability', owner: 'system', group_id: 0 }] })
  const { client } = renderWithProviders(
    <MemoryRouter>
      <MonitoringSection projectId={4} pending={pending} onSave={onSave} onCheck={onCheck} />
    </MemoryRouter>
  )
  return { read, onSave, onCheck, client }
}

/** The row of an origin, once it is drawn. */
const row = async (origin: string) => within(await screen.findByRole('listitem', { name: origin }))

beforeEach(() => vi.restoreAllMocks())

describe('MonitoringSection', () => {
  it('nothing on: the sentence and a switch per origin, nothing else', async () => {
    show(list([monitor(A), monitor(B)]))
    expect(await screen.findByRole('switch', { name: `Monitor ${A}` })).not.toBeChecked()
    expect(screen.getByRole('switch', { name: `Monitor ${B}` })).not.toBeChecked()
    expect(screen.getByText(SENTENCE)).toBeInTheDocument()
    expect(screen.getByText(A)).toBeInTheDocument()
    expect(screen.queryByText(/each site is checked/)).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toEqual([])
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
  })

  it('names the empty state, with no rows', async () => {
    show(list([]))
    expect(await screen.findByText('Add an exact http(s) origin above to monitor it.')).toBeInTheDocument()
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
  })

  it('shows one row per origin: on rows with what was found, off rows with the origin and switch alone', async () => {
    const data = list(
      [
        on(A, { cert: { expires_at: days(54).toISOString(), issuer: 'R3', valid: true, error: '', checked_at: 'x' } }),
        on(B, { cert: { expires_at: days(12).toISOString(), issuer: 'R3', valid: true, error: '', checked_at: 'x' } }),
        monitor(C),
      ],
      { domains: [{ domain: 'a.example', origins: [A, B], expires_at: utcDay(285), checked_at: 'x' }] },
    )
    show(data)
    const a = await row(A)
    expect(a.getByText('cert 54 days')).toBeInTheDocument()
    expect(a.getByText('cert 54 days')).toHaveAttribute('title', expect.stringMatching(/^expires \S+ \(54 days\)$/))
    expect(a.getByRole('button', { name: 'Check now' })).toBeEnabled()
    expect(a.getByRole('button', { name: LINK })).toBeInTheDocument()
    expect(a.getByRole('switch')).toBeChecked()
    expect((await row(B)).getByText('cert 12 days')).toBeInTheDocument()
    const c = await row(C)
    expect(c.getByRole('switch')).not.toBeChecked()
    expect(c.queryAllByRole('button')).toEqual([])
    expect(c.queryByText(/cert/)).not.toBeInTheDocument()
    // Three monitors' worth of a header, the domain and the link.
    expect(screen.getByText('each site is checked every 2 min')).toBeInTheDocument()
    expect(screen.queryByText(SENTENCE)).not.toBeInTheDocument()
    expect(screen.getByText(`Domain: a.example renews ${utcDay(285)} (285 days)`)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open Availability' })).toHaveAttribute('href', '/projects/4/dashboards/8')
  })

  it('says when a domain is unknown, with a line per domain', async () => {
    show(
      list([on(A), on('https://other.test')], {
        domains: [
          { domain: 'a.example', origins: [A], expires_at: '', checked_at: '' },
          { domain: 'other.test', origins: ['https://other.test'], expires_at: utcDay(10), checked_at: 'x' },
        ],
      }),
    )
    expect(await screen.findByText('Domain: a.example unknown')).toBeInTheDocument()
    expect(screen.getByText(`Domain: other.test renews ${utcDay(10)} (10 days)`)).toBeInTheDocument()
  })

  describe('the switch', () => {
    it('saves at once, with no Save button, and shows it at once', async () => {
      const user = userEvent.setup()
      let done!: (saved: boolean) => void
      const onSave = vi.fn().mockReturnValue(new Promise((resolve) => { done = resolve }))
      const { read } = show(list([monitor(A), monitor(B)]), onSave)
      const sw = await screen.findByRole('switch', { name: `Monitor ${B}` })
      expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.click(sw)
      expect(onSave).toHaveBeenCalledWith({ origin: B, enabled: true })
      expect(sw).toBeChecked()
      expect(screen.getByRole('switch', { name: `Monitor ${A}` })).not.toBeChecked()
      // What the save's refetch brings stays, once it resolves.
      read.mockResolvedValue(list([monitor(A), on(B)]))
      done(true)
      await waitFor(() => expect(sw).toBeChecked())
    })

    it('goes back when the save fails', async () => {
      const user = userEvent.setup()
      const onSave = vi.fn().mockResolvedValue(false) // the toast is the action's
      show(list([monitor(A)]), onSave)
      const sw = await screen.findByRole('switch', { name: `Monitor ${A}` })
      await user.click(sw)
      await waitFor(() => expect(onSave).toHaveBeenCalled())
      await waitFor(() => expect(sw).not.toBeChecked())
    })

    it('turns off a row at once, which goes back to the origin alone once saved', async () => {
      const user = userEvent.setup()
      const { read, onSave, client } = show(list([on(A)]))
      const a = await row(A)
      read.mockResolvedValue(list([monitor(A)]))
      await user.click(a.getByRole('switch'))
      expect(onSave).toHaveBeenCalledWith({ origin: A, enabled: false })
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(a.queryByRole('button', { name: 'Check now' })).not.toBeInTheDocument())
      expect(await screen.findByText(SENTENCE)).toBeInTheDocument()
      expect(screen.queryByRole('link', { name: 'Open Availability' })).not.toBeInTheDocument()
    })

    it('locks while anything is saving', async () => {
      show(list([on(A, { contains: 'x' })]), undefined, undefined, true)
      const a = await row(A)
      expect(a.getByRole('switch')).toBeDisabled()
      expect(a.getByRole('button', { name: 'Check now' })).toBeDisabled()
      const input = a.getByLabelText('Page must contain')
      expect(input).toBeDisabled()
      expect(input).toHaveAttribute('maxlength', '200')
    })
  })

  describe('the text', () => {
    it('hides behind a link per row, which reveals and focuses that row\'s field only, and saves what was typed', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A), on(B)]))
      const a = await row(A)
      const b = await row(B)
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
      await user.click(b.getByRole('button', { name: LINK }))
      const input = b.getByLabelText('Page must contain')
      expect(input).toHaveFocus()
      expect(b.queryByRole('button', { name: LINK })).not.toBeInTheDocument()
      expect(a.getByRole('button', { name: LINK })).toBeInTheDocument()
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
      // Opening it is no edit; typing is.
      expect(b.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.type(input, ' Welcome ')
      await user.click(b.getByRole('button', { name: 'Save' }))
      expect(onSave).toHaveBeenCalledWith({ origin: B, contains: 'Welcome' })
    })

    it('shows the field directly for a saved text, without taking the focus or offering Save', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A, { contains: 'Welcome' })]))
      const a = await row(A)
      const input = a.getByLabelText('Page must contain')
      expect(input).toHaveValue('Welcome')
      expect(input).not.toHaveFocus()
      expect(a.queryByRole('button', { name: LINK })).not.toBeInTheDocument()
      expect(a.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.type(input, '{Enter}')
      expect(onSave).not.toHaveBeenCalled()
    })

    it('keeps the field while a saved text is cleared, and goes back to the link once saved', async () => {
      const user = userEvent.setup()
      const { read, onSave, client } = show(list([on(A, { contains: 'Welcome' })]))
      const a = await row(A)
      await user.clear(a.getByLabelText('Page must contain'))
      expect(a.getByLabelText('Page must contain')).toHaveValue('')
      await user.click(a.getByRole('button', { name: 'Save' }))
      expect(onSave).toHaveBeenCalledWith({ origin: A, contains: '' })
      read.mockResolvedValue(list([on(A)]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      expect(await a.findByRole('button', { name: LINK })).toBeInTheDocument()
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
    })

    it('keeps an edit through a refetch, in its row, and takes what is saved once saved', async () => {
      const user = userEvent.setup()
      const onSave = vi.fn().mockResolvedValue(false)
      const { read, client } = show(list([on(A, { contains: 'first' }), on(B, { contains: 'other' })]), onSave)
      const a = await row(A)
      const b = await row(B)
      const input = a.getByLabelText('Page must contain')
      expect(input).toHaveValue('first')

      // Untouched, it follows the server.
      read.mockResolvedValue(list([on(A, { contains: 'second' }), on(B, { contains: 'other' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(input).toHaveValue('second'))

      // Edited, a refetch (window focus, another row's save) leaves it, and its
      // neighbour still follows the server.
      await user.type(input, ' edited')
      read.mockResolvedValue(list([on(A, { contains: 'third' }), on(B, { contains: 'other two' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(b.getByLabelText('Page must contain')).toHaveValue('other two'))
      expect(input).toHaveValue('second edited')

      // A refused save keeps the edit too.
      await user.click(a.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
      read.mockResolvedValue(list([on(A, { contains: 'fourth' }), on(B, { contains: 'other two' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      expect(input).toHaveValue('second edited')

      // A save that went through hands the field back to the server.
      onSave.mockResolvedValue(true)
      await user.click(a.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2))
      read.mockResolvedValue(list([on(A, { contains: 'saved' }), on(B, { contains: 'other two' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(input).toHaveValue('saved'))
    })
  })

  describe('the certificate', () => {
    const certOf = (cert: Partial<Monitor['cert']>) => monitor(A, { enabled: true, cert: { ...monitor(A).cert, checked_at: 'x', ...cert } })

    it('says when it is unchecked, with no second line', async () => {
      show(list([on(A)]))
      const a = await row(A)
      expect(a.getByText('cert not checked yet')).toBeInTheDocument()
      expect(a.queryByText(/^Certificate:/)).not.toBeInTheDocument()
    })

    it('has a second line for an error, known expiry or not', async () => {
      show(list([certOf({ error: 'x509: expired' })]))
      const a = await row(A)
      expect(a.getByText('cert unknown')).toBeInTheDocument()
      expect(a.getByText('Certificate: x509: expired')).toBeInTheDocument()
    })

    it('shows both a known expiry and the check that failed since', async () => {
      show(list([certOf({ expires_at: days(30).toISOString(), valid: true, error: 'connection refused' })]))
      const a = await row(A)
      expect(a.getByText('cert 30 days')).toBeInTheDocument()
      expect(a.getByText(/^Certificate: expires \S+ \(30 days\); last check failed: connection refused$/)).toBeInTheDocument()
    })

    it('says a certificate that was read but does not verify is invalid', async () => {
      show(list([certOf({ expires_at: days(30).toISOString(), valid: false, error: 'x509: unknown authority' })]))
      expect((await row(A)).getByText(/^Certificate: invalid, expires \S+ \(30 days\): x509: unknown authority$/)).toBeInTheDocument()
    })

    it('says expired once the day has passed', async () => {
      show(list([certOf({ expires_at: days(-3).toISOString(), valid: true })]))
      expect((await row(A)).getByText('cert expired')).toBeInTheDocument()
    })

    it('says nothing on an http:// origin, which has none', async () => {
      show(list([on('http://a.example', { cert: { ...monitor(A).cert, checked_at: 'x' } })]))
      const a = await row('http://a.example')
      expect(a.queryByText(/cert/i)).not.toBeInTheDocument()
    })
  })

  it('offers a retry when the read fails', async () => {
    const user = userEvent.setup()
    const read = vi.spyOn(endpoints, 'monitors').mockRejectedValueOnce(new Error('boom')).mockResolvedValue(list([monitor(A)]))
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} onCheck={vi.fn()} onSave={vi.fn()} /></MemoryRouter>)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the monitors. boom")
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('switch', { name: `Monitor ${A}` })).toBeInTheDocument()
    expect(read).toHaveBeenCalledTimes(2)
  })

  describe('redirect warning', () => {
    it('warns at 3 hops, with the count', async () => {
      show(list([on(A, { redirects: 3 }), on(B, { redirects: 5 })]))
      expect((await row(A)).getByText('Redirects 3 times before answering, which slows every visit')).toBeInTheDocument()
      expect((await row(B)).getByText('Redirects 5 times before answering, which slows every visit')).toBeInTheDocument()
    })

    it('says nothing at 2 hops, or when the monitor is off', async () => {
      show(list([on(A, { redirects: 2 }), monitor(B, { redirects: 4 })]))
      await row(A)
      expect(screen.queryByText(/Redirects \d+ times/)).not.toBeInTheDocument()
    })
  })

  describe('Check now', () => {
    const at = '2026-11-01T10:00:00Z'
    const pendingCheck = { ...monitor(A).check, requested_at: at }
    const done = (over: Partial<Monitor['check']>): Monitor['check'] => ({ ...monitor(A).check, requested_at: at, finished_at: '2026-11-01T10:00:02Z', ...over })

    it('is per row, and asks for that origin\'s check', async () => {
      const user = userEvent.setup()
      const { onCheck } = show(list([on(A), on(B)]))
      await user.click((await row(B)).getByRole('button', { name: 'Check now' }))
      expect(onCheck).toHaveBeenCalledTimes(1)
      expect(onCheck).toHaveBeenCalledWith(B)
    })

    it('reads Checking… in the row with a check pending only', async () => {
      show(list([on(A, { check: pendingCheck }), on(B)]))
      expect((await row(A)).getByRole('button', { name: 'Checking…' })).toBeDisabled()
      expect((await row(B)).getByRole('button', { name: 'Check now' })).toBeEnabled()
    })

    it('polls while a row has one pending, shows its result and the fresh domain there, and stops', async () => {
      const { read } = show(list([on(A, { check: pendingCheck }), on(B)]))
      const a = await row(A)
      expect(await screen.findByText('each site is checked every 2 min')).toBeInTheDocument()
      read.mockResolvedValue(list([on(A, { check: done({ up: true, status: 200, ms: 123 }) }), on(B)], {
        domains: [{ domain: 'a.example', origins: [A, B], expires_at: utcDay(10), checked_at: at }],
      }))
      expect(await a.findByText(/^up, 200, 123 ms \(.+ ago\)$/, undefined, { timeout: 4000 })).toBeInTheDocument()
      expect(screen.getByText(`Domain: a.example renews ${utcDay(10)} (10 days)`)).toBeInTheDocument()
      expect(a.getByRole('button', { name: 'Check now' })).toBeEnabled()
      expect((await row(B)).queryByText(/^up,/)).not.toBeInTheDocument()
      const calls = read.mock.calls.length
      await new Promise((r) => setTimeout(r, 2300))
      expect(read.mock.calls.length).toBe(calls)
    }, 10_000)

    it('keeps an edit through the polling', async () => {
      const user = userEvent.setup()
      const { read } = show(list([on(A, { contains: 'Welcome', check: pendingCheck })]))
      const a = await row(A)
      const text = a.getByLabelText('Page must contain')
      await user.type(text, ' back')
      read.mockResolvedValue(list([on(A, { contains: 'Welcome', check: done({ up: true, status: 200, ms: 5 }) })]))
      await waitFor(() => expect(read.mock.results.length).toBeGreaterThan(1), { timeout: 4000 })
      expect(text).toHaveValue('Welcome back')
    }, 10_000)

    it('shows a down result with its reason and error', async () => {
      show(list([on(A, { check: done({ reason: 'content', error: 'text not found', status: 200, ms: 9 }) })]))
      expect((await row(A)).getByText(/^down, content: text not found \(.+ ago\)$/)).toBeInTheDocument()
    })

    it('says when the check did not run', async () => {
      show(list([on(A, { check: done({ error: 'the check did not run' }) })]))
      expect((await row(A)).getByText(/^the check did not run \(.+ ago\)$/)).toBeInTheDocument()
    })

    it('shows no result before any check', async () => {
      show(list([on(A)]))
      expect((await row(A)).queryByText(/^(up|down),/)).not.toBeInTheDocument()
    })

    it('gives up waiting on a row after 45 s with a message, and keeps looking every 15 s', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        const { read } = show(list([on(A, { check: pendingCheck }), on(B)]))
        const a = await row(A)
        expect(a.getByRole('button', { name: 'Checking…' })).toBeDisabled()
        expect(a.queryByText(/has not finished/)).not.toBeInTheDocument()
        await vi.advanceTimersByTimeAsync(45_500)
        expect(await a.findByText(/has not finished/)).toBeInTheDocument()
        expect(a.getByRole('button', { name: 'Check now' })).toBeEnabled()
        // It looks less often, not never: the text says the result will show.
        const calls = read.mock.calls.length
        await vi.advanceTimersByTimeAsync(10_000)
        expect(read.mock.calls.length).toBe(calls)
        await vi.advanceTimersByTimeAsync(5_500)
        expect(read.mock.calls.length).toBe(calls + 1)
        read.mockResolvedValue(list([on(A, { check: done({ up: true, status: 200, ms: 7 }) }), on(B)]))
        await vi.advanceTimersByTimeAsync(15_500)
        expect(await a.findByText(/^up, 200, 7 ms/)).toBeInTheDocument()
        expect(a.queryByText(/has not finished/)).not.toBeInTheDocument()
        // Finished, it stops.
        const finished = read.mock.calls.length
        await vi.advanceTimersByTimeAsync(60_000)
        expect(read.mock.calls.length).toBe(finished)
      } finally {
        vi.useRealTimers()
      }
    })

    it('polls fast again when another row starts a check after one gave up', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        const { read, client } = show(list([on(A, { check: pendingCheck }), on(B)]))
        await row(A)
        await vi.advanceTimersByTimeAsync(45_500)
        expect(await screen.findByText(/has not finished/)).toBeInTheDocument()
        client.setQueryData(['monitor', 4], list([on(A, { check: pendingCheck }), on(B, { check: { ...pendingCheck, requested_at: '2026-11-01T10:05:00Z' } })]))
        read.mockResolvedValue(list([on(A, { check: pendingCheck }), on(B, { check: { ...pendingCheck, requested_at: '2026-11-01T10:05:00Z' } })]))
        const calls = read.mock.calls.length
        await vi.advanceTimersByTimeAsync(2_500)
        expect(read.mock.calls.length).toBeGreaterThan(calls)
      } finally {
        vi.useRealTimers()
      }
    })

    it('reads the monitors again when the request is refused, showing the check that is running', async () => {
      const user = userEvent.setup()
      const onCheck = vi.fn().mockResolvedValue(false)
      const { read } = show(list([on(A)]), undefined, onCheck)
      const a = await row(A)
      read.mockResolvedValue(list([on(A, { check: pendingCheck })]))
      await user.click(a.getByRole('button', { name: 'Check now' }))
      expect(await a.findByRole('button', { name: 'Checking…' })).toBeDisabled()
    })

    it('does not read the monitors again when the request went through', async () => {
      const user = userEvent.setup()
      const { read } = show(list([on(A)]))
      const a = await row(A)
      const calls = read.mock.calls.length
      await user.click(a.getByRole('button', { name: 'Check now' }))
      await new Promise((r) => setTimeout(r, 50))
      expect(read.mock.calls.length).toBe(calls)
    })
  })
})
