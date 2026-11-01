import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type Monitor } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import MonitoringSection from './MonitoringSection'

const days = (n: number) => new Date(Date.now() + n * 86_400_000 + 3_600_000)

const base: Monitor = {
  project_id: 4,
  enabled: false,
  origin: '',
  contains: '',
  eligible_origins: ['https://a.example', 'https://b.example'],
  interval_seconds: 60,
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
    expect(await screen.findByRole('switch', { name: 'Monitor this project' })).not.toBeChecked()
    expect(screen.getByRole('combobox', { name: 'Origin to monitor' })).toBeInTheDocument()
    expect(screen.getByLabelText('Page must contain (optional)')).toBeInTheDocument()
    expect(screen.queryByText(/Checked every/)).not.toBeInTheDocument()
  })

  it('saves enabled, origin and the text', async () => {
    const user = userEvent.setup()
    const onSave = show({ ...base, eligible_origins: ['https://a.example'] })
    await user.click(await screen.findByRole('switch', { name: 'Monitor this project' }))
    await user.type(screen.getByLabelText('Page must contain (optional)'), 'Welcome')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith({ enabled: true, origin: 'https://a.example', contains: 'Welcome' })
  })

  it('hides the select with one eligible origin and names it', async () => {
    show({ ...base, eligible_origins: ['https://a.example'] })
    expect(await screen.findByText('https://a.example')).toBeInTheDocument()
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
      domain: { expires_at: days(200).toISOString().slice(0, 10), checked_at: new Date().toISOString() },
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
})
