import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type Monitor } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import MonitoringSection from './MonitoringSection'

const days = (n: number) => new Date(Date.now() + n * 86_400_000 + 3_600_000)

/** The `YYYY-MM-DD` n days after today in UTC, whatever the hour. */
function utcDay(n: number): string {
  const now = new Date()
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + n)).toISOString().slice(0, 10)
}

const SWITCH = 'Monitor this site'

const base: Monitor = {
  project_id: 4,
  enabled: false,
  origin: '',
  contains: '',
  eligible_origins: ['https://a.example', 'https://b.example'],
  interval_seconds: 60,
  redirects: 0,
  cert: { expires_at: '', issuer: '', valid: false, error: '', checked_at: '' },
  domain: { expires_at: '', checked_at: '' },
  check: { requested_at: '', finished_at: '', up: false, status: 0, reason: '', error: '', ms: 0 },
}

function show(m: Monitor, onSave = vi.fn().mockResolvedValue(true), onCheck = vi.fn().mockResolvedValue(true)) {
  vi.spyOn(endpoints, 'monitor').mockResolvedValue(m)
  vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [{ dashboard_id: 8, title: 'Availability', owner: 'system', group_id: 0 }] })
  renderWithProviders(
    <MemoryRouter>
      <MonitoringSection projectId={4} pending={false} onSave={onSave} onCheck={onCheck} />
    </MemoryRouter>
  )
  return onSave
}

beforeEach(() => vi.restoreAllMocks())

describe('MonitoringSection', () => {
  const on: Monitor = { ...base, enabled: true, origin: 'https://a.example' }
  const one = { eligible_origins: ['https://a.example'] }
  const checked = new Date().toISOString()
  /** The value beside a term of the status list. */
  const status = (term: string) => screen.getByText(term).nextElementSibling

  it('off and saved, shows the switch and one sentence, nothing else', async () => {
    show(base)
    expect(await screen.findByRole('switch', { name: SWITCH })).not.toBeChecked()
    expect(screen.getByText('Check that the site is up, and when its certificate and domain expire.')).toBeInTheDocument()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toEqual([])
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
  })

  it('switched on with one origin: the origin and interval on one line, and Save', async () => {
    const user = userEvent.setup()
    const onSave = show({ ...base, ...one })
    expect(screen.queryByText(/every 1 min/)).not.toBeInTheDocument()
    await user.click(await screen.findByRole('switch', { name: SWITCH }))
    expect(screen.getByText('https://a.example · every 1 min')).toBeInTheDocument()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.queryByText(/Check that the site/)).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith({ enabled: true, origin: 'https://a.example', contains: '' })
  })

  it('switched on with several origins: the select and the interval under it', async () => {
    const user = userEvent.setup()
    show(base)
    await user.click(await screen.findByRole('switch', { name: SWITCH }))
    expect(screen.getByRole('combobox', { name: 'Origin to monitor' })).toBeInTheDocument()
    expect(screen.getByText('Checked every 1 min')).toBeInTheDocument()
  })

  it('switching on and off again has nothing to save', async () => {
    const user = userEvent.setup()
    show(base)
    const sw = await screen.findByRole('switch', { name: SWITCH })
    await user.click(sw)
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
    await user.click(sw)
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
  })

  it('turning a monitor off is an edit: only the sentence and Save', async () => {
    const user = userEvent.setup()
    const onSave = show({ ...on, ...one })
    await user.click(await screen.findByRole('switch', { name: SWITCH }))
    expect(screen.getByText(/Check that the site/)).toBeInTheDocument()
    expect(screen.queryByText('Certificate')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Check now' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith({ enabled: false, origin: 'https://a.example', contains: '' })
  })

  it('hides the text field behind a link, which reveals and focuses it, and saves what was typed', async () => {
    const user = userEvent.setup()
    const onSave = show({ ...on, ...one })
    await screen.findByRole('button', { name: 'Check now' })
    expect(screen.queryByLabelText('Page must contain')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Also look for text on the page' }))
    const input = screen.getByLabelText('Page must contain')
    expect(input).toHaveFocus()
    expect(screen.queryByRole('button', { name: 'Also look for text on the page' })).not.toBeInTheDocument()
    // Opening it is no edit; typing is.
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Check now' })).toBeInTheDocument()
    await user.type(input, 'Welcome')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith({ enabled: true, origin: 'https://a.example', contains: 'Welcome' })
  })

  it('shows the field directly for a saved text, without taking the focus', async () => {
    show({ ...on, ...one, contains: 'Welcome' })
    const input = await screen.findByLabelText('Page must contain')
    expect(input).toHaveValue('Welcome')
    expect(input).not.toHaveFocus()
    expect(screen.queryByRole('button', { name: 'Also look for text on the page' })).not.toBeInTheDocument()
  })

  it('keeps the field while a saved text is cleared, and goes back to the link once saved', async () => {
    const user = userEvent.setup()
    const read = vi.spyOn(endpoints, 'monitor').mockResolvedValue({ ...on, ...one, contains: 'Welcome' })
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    const onSave = vi.fn().mockResolvedValue(true)
    const { client } = renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} onCheck={vi.fn()} onSave={onSave} /></MemoryRouter>)
    await user.clear(await screen.findByLabelText('Page must contain'))
    expect(screen.getByLabelText('Page must contain')).toHaveValue('')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith({ enabled: true, origin: 'https://a.example', contains: '' })
    read.mockResolvedValue({ ...on, ...one })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    expect(await screen.findByRole('button', { name: 'Also look for text on the page' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Page must contain')).not.toBeInTheDocument()
  })

  it('goes back to the link when a text asked for is saved and later cleared', async () => {
    const user = userEvent.setup()
    const read = vi.spyOn(endpoints, 'monitor').mockResolvedValue({ ...on, ...one })
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    const onSave = vi.fn().mockResolvedValue(true)
    const { client } = renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} onCheck={vi.fn()} onSave={onSave} /></MemoryRouter>)
    await user.click(await screen.findByRole('button', { name: 'Also look for text on the page' }))
    await user.type(screen.getByLabelText('Page must contain'), 'x')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    read.mockResolvedValue({ ...on, ...one, contains: 'x' })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument())
    await user.clear(screen.getByLabelText('Page must contain'))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    read.mockResolvedValue({ ...on, ...one })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    expect(await screen.findByRole('button', { name: 'Also look for text on the page' })).toBeInTheDocument()
  })

  it('has no Save at rest, not even on Enter in the field', async () => {
    const user = userEvent.setup()
    const onSave = show({ ...on, ...one, contains: 'Welcome' })
    const input = await screen.findByLabelText('Page must contain')
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
    await user.type(input, '{Enter}')
    expect(onSave).not.toHaveBeenCalled()
  })

  it('hides the status and footer while there are edits, and shows them again without', async () => {
    const user = userEvent.setup()
    show({ ...on, ...one, contains: 'Welcome' })
    await screen.findByText('Certificate')
    const input = screen.getByLabelText('Page must contain')
    await user.type(input, '!')
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
    expect(screen.queryByText('Certificate')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Check now' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open Availability' })).not.toBeInTheDocument()
    await user.type(input, '{Backspace}')
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
    expect(screen.getByText('Certificate')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open Availability' })).toBeInTheDocument()
  })

  it('names the empty state, with no switch', async () => {
    show({ ...base, eligible_origins: [] })
    expect(await screen.findByText('Add an exact http(s) origin above to monitor it.')).toBeInTheDocument()
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
  })

  it('shows the interval, certificate and domain days left, and the Availability link', async () => {
    show({
      ...on,
      ...one,
      cert: { expires_at: days(30).toISOString(), issuer: 'R3', valid: true, error: '', checked_at: checked },
      domain: { expires_at: utcDay(200), checked_at: checked },
    })
    expect(await screen.findByText('https://a.example · every 1 min')).toBeInTheDocument()
    expect(status('Certificate')).toHaveTextContent(/^expires \S+ \(30 days\)$/)
    expect(status('Domain')).toHaveTextContent(/^renews \S+ \(200 days\)$/)
    expect(screen.getByRole('link', { name: 'Open Availability' })).toHaveAttribute('href', '/projects/4/dashboards/8')
  })

  it('says when the certificate is unchecked, or the domain unknown', async () => {
    show(on)
    await screen.findByText('Certificate')
    expect(status('Certificate')).toHaveTextContent('not checked yet')
    expect(status('Domain')).toHaveTextContent('unknown')
  })

  it('shows a certificate error', async () => {
    show({ ...on, cert: { ...base.cert, error: 'x509: expired', checked_at: checked } })
    await screen.findByText('Certificate')
    expect(status('Certificate')).toHaveTextContent('x509: expired')
  })

  it('shows both a known expiry and the check that failed since', async () => {
    show({ ...on, cert: { expires_at: days(30).toISOString(), issuer: 'R3', valid: true, error: 'connection refused', checked_at: checked } })
    await screen.findByText('Certificate')
    expect(status('Certificate')).toHaveTextContent(/^expires \S+ \(30 days\); last check failed: connection refused$/)
  })

  it('says a certificate that was read but does not verify is invalid', async () => {
    show({ ...on, cert: { expires_at: days(30).toISOString(), issuer: 'R3', valid: false, error: 'x509: unknown authority', checked_at: checked } })
    await screen.findByText('Certificate')
    expect(status('Certificate')).toHaveTextContent(/^invalid, expires \S+ \(30 days\): x509: unknown authority$/)
  })

  it('says no certificate applies to an http:// origin', async () => {
    show({ ...on, origin: 'http://a.example', eligible_origins: ['http://a.example'], cert: { ...base.cert, checked_at: checked } })
    await screen.findByText('Certificate')
    expect(status('Certificate')).toHaveTextContent('none on a plain http:// origin')
  })

  it('offers a retry when the read fails', async () => {
    const user = userEvent.setup()
    const read = vi.spyOn(endpoints, 'monitor').mockRejectedValueOnce(new Error('boom')).mockResolvedValue(base)
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} onCheck={vi.fn()} onSave={vi.fn()} /></MemoryRouter>)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the monitor. boom")
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('switch', { name: SWITCH })).toBeInTheDocument()
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('locks the fields while saving and caps the text', async () => {
    vi.spyOn(endpoints, 'monitor').mockResolvedValue({ ...on, contains: 'Welcome' })
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} pending onCheck={vi.fn()} onSave={vi.fn()} /></MemoryRouter>)
    const input = await screen.findByLabelText('Page must contain')
    expect(input).toBeDisabled()
    expect(input).toHaveAttribute('maxlength', '200')
    expect(screen.getByRole('combobox', { name: 'Origin to monitor' })).toBeDisabled()
    expect(screen.getByRole('switch', { name: SWITCH })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Check now' })).toBeDisabled()
  })

  it('keeps an edit through a refetch, and takes what is saved once saved', async () => {
    const user = userEvent.setup()
    const read = vi.spyOn(endpoints, 'monitor').mockResolvedValue({ ...on, contains: 'first' })
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    const onSave = vi.fn().mockResolvedValue(false)
    const { client } = renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} onCheck={vi.fn()} onSave={onSave} /></MemoryRouter>)
    const input = await screen.findByLabelText('Page must contain')
    expect(input).toHaveValue('first')

    // Untouched, it follows the server.
    read.mockResolvedValue({ ...on, contains: 'second' })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    await waitFor(() => expect(input).toHaveValue('second'))

    // Edited, a refetch (window focus, another section's save) leaves it.
    await user.type(input, ' edited')
    read.mockResolvedValue({ ...on, contains: 'third' })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    await waitFor(() => expect(read).toHaveBeenCalledTimes(3))
    expect(input).toHaveValue('second edited')

    // A refused save keeps the edit too.
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    read.mockResolvedValue({ ...on, contains: 'fourth' })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    await waitFor(() => expect(read).toHaveBeenCalledTimes(4))
    expect(input).toHaveValue('second edited')

    // A save that went through hands the form back to the server.
    onSave.mockResolvedValue(true)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2))
    read.mockResolvedValue({ ...on, contains: 'saved' })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    await waitFor(() => expect(input).toHaveValue('saved'))
  })

  describe('redirect warning', () => {
    const NOTE = 'Redirects 3 times before answering, which slows every visit'

    it('warns at 3 hops, with the count', async () => {
      show({ ...base, enabled: true, origin: 'https://a.example', redirects: 3 })
      expect(await screen.findByText(NOTE)).toBeInTheDocument()
    })

    it('says the count it has', async () => {
      show({ ...base, enabled: true, origin: 'https://a.example', redirects: 5 })
      expect(await screen.findByText('Redirects 5 times before answering, which slows every visit')).toBeInTheDocument()
    })

    it('says nothing at 2 hops', async () => {
      show({ ...base, enabled: true, origin: 'https://a.example', redirects: 2 })
      await screen.findByText('Certificate')
      expect(screen.queryByText(/Redirects \d+ times/)).not.toBeInTheDocument()
    })

    it('says nothing when the monitor is off', async () => {
      show({ ...base, enabled: false, origin: 'https://a.example', redirects: 4 })
      await screen.findByRole('switch', { name: SWITCH })
      expect(screen.queryByText(/Redirects \d+ times/)).not.toBeInTheDocument()
    })
  })

  describe('Check now', () => {
    const on: Monitor = { ...base, enabled: true, origin: 'https://a.example', contains: 'Welcome' }
    const at = '2026-11-01T10:00:00Z'
    const pending: Monitor = { ...on, check: { ...on.check, requested_at: at } }
    const done = (over: Partial<Monitor['check']>): Monitor => ({ ...on, check: { ...on.check, requested_at: at, finished_at: '2026-11-01T10:00:02Z', ...over } })

    it('is a button on an enabled monitor, and asks for the check', async () => {
      const onCheck = vi.fn().mockResolvedValue(true)
      show(on, undefined, onCheck)
      const user = userEvent.setup()
      await user.click(await screen.findByRole('button', { name: 'Check now' }))
      expect(onCheck).toHaveBeenCalledTimes(1)
    })

    it('shows the saved origin, not the first, with nothing to save', async () => {
      const onCheck = vi.fn().mockResolvedValue(true)
      show({ ...on, origin: 'https://b.example' }, undefined, onCheck)
      expect(await screen.findByRole('combobox', { name: 'Origin to monitor' })).toHaveTextContent('https://b.example')
      expect(await screen.findByRole('button', { name: 'Check now' })).toBeInTheDocument()
    })

    it('is not offered on a disabled monitor', async () => {
      show({ ...on, enabled: false })
      await screen.findByRole('switch', { name: SWITCH })
      expect(screen.queryByRole('button', { name: 'Check now' })).not.toBeInTheDocument()
    })

    it('goes away while the form has edits to save, and comes back without them', async () => {
      const user = userEvent.setup()
      show(on)
      await screen.findByRole('button', { name: 'Check now' })
      const text = screen.getByLabelText('Page must contain')
      await user.type(text, '!')
      expect(screen.queryByRole('button', { name: 'Check now' })).not.toBeInTheDocument()
      await user.type(text, '{Backspace}')
      expect(screen.getByRole('button', { name: 'Check now' })).toBeInTheDocument()
    })

    it('is disabled and reads Checking… while a check is pending', async () => {
      show(pending)
      expect(await screen.findByRole('button', { name: 'Checking…' })).toBeDisabled()
      expect(screen.queryByRole('button', { name: 'Check now' })).not.toBeInTheDocument()
    })

    it('polls until the check finishes, then shows its result and fresh readings, and stops', async () => {
      show(pending)
      await screen.findByRole('button', { name: 'Checking…' })
      expect(status('Domain')).toHaveTextContent('unknown')
      const spy = vi.mocked(endpoints.monitor)
      spy.mockResolvedValue({ ...done({ up: true, status: 200, ms: 123 }), domain: { expires_at: utcDay(10), checked_at: at } })
      await screen.findByText('Last check', undefined, { timeout: 4000 })
      expect(status('Last check')).toHaveTextContent(/^up, 200, 123 ms \(.+ ago\)$/)
      expect(status('Domain')).toHaveTextContent(/^renews \S+ \(10 days\)$/)
      expect(screen.getByRole('button', { name: 'Check now' })).toBeEnabled()
      const calls = spy.mock.calls.length
      await new Promise((r) => setTimeout(r, 2300))
      expect(spy.mock.calls.length).toBe(calls)
    }, 10_000)

    it('keeps an edit through the polling', async () => {
      const user = userEvent.setup()
      show(pending)
      await screen.findByRole('button', { name: 'Checking…' })
      const text = screen.getByLabelText('Page must contain')
      await user.type(text, ' back')
      vi.mocked(endpoints.monitor).mockResolvedValue(done({ up: true, status: 200, ms: 5 }))
      await waitFor(() => expect(vi.mocked(endpoints.monitor).mock.results.length).toBeGreaterThan(1), { timeout: 4000 })
      expect(text).toHaveValue('Welcome back')
    }, 10_000)

    it('shows a down result with its reason and error', async () => {
      show(done({ reason: 'content', error: 'text not found', status: 200, ms: 9 }))
      await screen.findByText('Last check')
      expect(status('Last check')).toHaveTextContent(/^down, content: text not found \(.+ ago\)$/)
    })

    it('says when the check did not run', async () => {
      show(done({ error: 'the check did not run' }))
      await screen.findByText('Last check')
      expect(status('Last check')).toHaveTextContent(/^the check did not run \(.+ ago\)$/)
    })

    it('shows no result line before any check', async () => {
      show(on)
      await screen.findByRole('button', { name: 'Check now' })
      expect(screen.queryByText('Last check')).not.toBeInTheDocument()
    })

    it('gives up waiting after 45 s with a message, and keeps looking every 15 s', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        show(pending)
        await screen.findByRole('button', { name: 'Checking…' })
        expect(screen.queryByText(/has not finished/)).not.toBeInTheDocument()
        const spy = vi.mocked(endpoints.monitor)
        await vi.advanceTimersByTimeAsync(45_500)
        expect(await screen.findByText(/has not finished/)).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Check now' })).toBeEnabled()
        // It looks less often, not never: the text says the result will show.
        const calls = spy.mock.calls.length
        await vi.advanceTimersByTimeAsync(10_000)
        expect(spy.mock.calls.length).toBe(calls)
        await vi.advanceTimersByTimeAsync(5_500)
        expect(spy.mock.calls.length).toBe(calls + 1)
        spy.mockResolvedValue(done({ up: true, status: 200, ms: 7 }))
        await vi.advanceTimersByTimeAsync(15_500)
        expect(await screen.findByText('Last check')).toBeInTheDocument()
        expect(screen.queryByText(/has not finished/)).not.toBeInTheDocument()
        // Finished, it stops.
        const finished = spy.mock.calls.length
        await vi.advanceTimersByTimeAsync(60_000)
        expect(spy.mock.calls.length).toBe(finished)
      } finally {
        vi.useRealTimers()
      }
    })

    it('reads the monitor again when the request is refused, showing the check that is running', async () => {
      const user = userEvent.setup()
      const onCheck = vi.fn().mockResolvedValue(false)
      show(on, undefined, onCheck)
      const button = await screen.findByRole('button', { name: 'Check now' })
      vi.mocked(endpoints.monitor).mockResolvedValue(pending)
      await user.click(button)
      expect(await screen.findByRole('button', { name: 'Checking…' })).toBeDisabled()
    })

    it('does not read the monitor again when the request went through', async () => {
      const user = userEvent.setup()
      show(on)
      const button = await screen.findByRole('button', { name: 'Check now' })
      const calls = vi.mocked(endpoints.monitor).mock.calls.length
      await user.click(button)
      await new Promise((r) => setTimeout(r, 50))
      expect(vi.mocked(endpoints.monitor).mock.calls.length).toBe(calls)
    })

    it('keeps the form as it was when the request is refused', async () => {
      const user = userEvent.setup()
      const onCheck = vi.fn().mockResolvedValue(false) // the toast is the action's
      show(on, undefined, onCheck)
      await user.click(await screen.findByRole('button', { name: 'Check now' }))
      await waitFor(() => expect(onCheck).toHaveBeenCalled())
      expect(screen.getByLabelText('Page must contain')).toHaveValue('Welcome')
      expect(screen.getByRole('button', { name: 'Check now' })).toBeEnabled()
    })
  })
})
