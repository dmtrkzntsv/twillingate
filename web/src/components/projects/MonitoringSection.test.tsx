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

const SWITCH = "Check this project's availability"

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
}

function show(m: Monitor, onSave = vi.fn().mockResolvedValue(true)) {
  vi.spyOn(endpoints, 'monitor').mockResolvedValue(m)
  vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [{ dashboard_id: 8, title: 'Availability', owner: 'system', group_id: 0 }] })
  renderWithProviders(
    <MemoryRouter>
      <MonitoringSection projectId={4} pending={false} onSave={onSave} />
    </MemoryRouter>
  )
  return onSave
}

beforeEach(() => vi.restoreAllMocks())

describe('MonitoringSection', () => {
  it('shows an off monitor with the switch, the origin choice and no status', async () => {
    show(base)
    expect(await screen.findByRole('switch', { name: SWITCH })).not.toBeChecked()
    expect(screen.getByRole('combobox', { name: 'Origin to monitor' })).toBeInTheDocument()
    expect(screen.getByLabelText('Page must contain (optional)')).toBeInTheDocument()
    expect(screen.queryByText(/Checked every/)).not.toBeInTheDocument()
  })

  it('saves enabled, origin and the text', async () => {
    const user = userEvent.setup()
    const onSave = show({ ...base, eligible_origins: ['https://a.example'] })
    await user.click(await screen.findByRole('switch', { name: SWITCH }))
    await user.type(screen.getByLabelText('Page must contain (optional)'), 'Welcome')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith({ enabled: true, origin: 'https://a.example', contains: 'Welcome' })
  })

  it('hides the select with one eligible origin and names it', async () => {
    show({ ...base, eligible_origins: ['https://a.example'] })
    expect((await screen.findByText('https://a.example')).parentElement).toHaveTextContent('Origin to monitor: https://a.example')
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  })

  it('hints to add an origin when none is eligible, and offers nothing to switch', async () => {
    show({ ...base, eligible_origins: [] })
    expect(await screen.findByText('Add an http(s) origin above to monitor it')).toBeInTheDocument()
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
  })

  it('shows the interval, certificate and domain days left, and the Availability link', async () => {
    show({
      ...base,
      enabled: true,
      origin: 'https://a.example',
      cert: { expires_at: days(30).toISOString(), issuer: 'R3', valid: true, error: '', checked_at: new Date().toISOString() },
      domain: { expires_at: utcDay(200), checked_at: new Date().toISOString() },
    })
    expect(await screen.findByText('Checked every 1 min')).toBeInTheDocument()
    expect(screen.getByText(/Certificate: expires .* \(30 days\)/)).toBeInTheDocument()
    expect(screen.getByText(/Domain: renews .* \(200 days\)/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open the Availability tab' })).toHaveAttribute('href', '/projects/4/dashboards/8')
  })

  it('says when the certificate is unchecked, failed, or the domain unknown', async () => {
    show({ ...base, enabled: true, origin: 'https://a.example' })
    expect(await screen.findByText('Certificate: not checked yet')).toBeInTheDocument()
    expect(screen.getByText('Domain: unknown')).toBeInTheDocument()
  })

  it('shows a certificate error', async () => {
    show({ ...base, enabled: true, origin: 'https://a.example', cert: { ...base.cert, error: 'x509: expired', checked_at: new Date().toISOString() } })
    expect(await screen.findByText('Certificate: x509: expired')).toBeInTheDocument()
  })

  const on = { ...base, enabled: true, origin: 'https://a.example' }
  const checked = new Date().toISOString()

  it('shows both a known expiry and the check that failed since', async () => {
    show({ ...on, cert: { expires_at: days(30).toISOString(), issuer: 'R3', valid: true, error: 'connection refused', checked_at: checked } })
    expect(await screen.findByText(/^Certificate: expires \S+ \(30 days\); last check failed: connection refused$/)).toBeInTheDocument()
  })

  it('says a certificate that was read but does not verify is invalid', async () => {
    show({ ...on, cert: { expires_at: days(30).toISOString(), issuer: 'R3', valid: false, error: 'x509: unknown authority', checked_at: checked } })
    expect(await screen.findByText(/^Certificate: invalid, expires \S+ \(30 days\): x509: unknown authority$/)).toBeInTheDocument()
  })

  it('says no certificate applies to an http:// origin', async () => {
    show({ ...on, origin: 'http://a.example', eligible_origins: ['http://a.example'], cert: { ...base.cert, checked_at: checked } })
    expect(await screen.findByText('Certificate: none on a plain http:// origin')).toBeInTheDocument()
  })

  it('offers a retry when the read fails', async () => {
    const user = userEvent.setup()
    const read = vi.spyOn(endpoints, 'monitor').mockRejectedValueOnce(new Error('boom')).mockResolvedValue(base)
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} onSave={vi.fn()} /></MemoryRouter>)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the monitor. boom")
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('switch', { name: SWITCH })).toBeInTheDocument()
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('locks the fields while saving and caps the text', async () => {
    vi.spyOn(endpoints, 'monitor').mockResolvedValue(base)
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} pending onSave={vi.fn()} /></MemoryRouter>)
    const input = await screen.findByLabelText('Page must contain (optional)')
    expect(input).toBeDisabled()
    expect(input).toHaveAttribute('maxlength', '200')
    expect(screen.getByRole('combobox', { name: 'Origin to monitor' })).toBeDisabled()
  })

  it('keeps an edit through a refetch, and takes what is saved once saved', async () => {
    const user = userEvent.setup()
    const read = vi.spyOn(endpoints, 'monitor').mockResolvedValue({ ...base, contains: 'first' })
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    const onSave = vi.fn().mockResolvedValue(false)
    const { client } = renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} onSave={onSave} /></MemoryRouter>)
    const input = await screen.findByLabelText('Page must contain (optional)')
    expect(input).toHaveValue('first')

    // Untouched, it follows the server.
    read.mockResolvedValue({ ...base, contains: 'second' })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    await waitFor(() => expect(input).toHaveValue('second'))

    // Edited, a refetch (window focus, another section's save) leaves it.
    await user.type(input, ' edited')
    read.mockResolvedValue({ ...base, contains: 'third' })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    await waitFor(() => expect(read).toHaveBeenCalledTimes(3))
    expect(input).toHaveValue('second edited')

    // A refused save keeps the edit too.
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    read.mockResolvedValue({ ...base, contains: 'fourth' })
    await client.invalidateQueries({ queryKey: ['monitor'] })
    await waitFor(() => expect(read).toHaveBeenCalledTimes(4))
    expect(input).toHaveValue('second edited')

    // A save that went through hands the form back to the server.
    onSave.mockResolvedValue(true)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2))
    read.mockResolvedValue({ ...base, contains: 'saved' })
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
      await screen.findByText(/Checked every/)
      expect(screen.queryByText(/Redirects \d+ times/)).not.toBeInTheDocument()
    })

    it('says nothing when the monitor is off', async () => {
      show({ ...base, enabled: false, origin: 'https://a.example', redirects: 4 })
      await screen.findByRole('switch', { name: SWITCH })
      expect(screen.queryByText(/Redirects \d+ times/)).not.toBeInTheDocument()
    })
  })
})
