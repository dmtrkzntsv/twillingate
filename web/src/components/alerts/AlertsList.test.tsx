import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { TooltipProvider } from '@/components/ui/tooltip'
import { MemoryRouter, Route, Routes } from 'react-router'
import { ApiError, endpoints } from '@/lib/api'
import { alert, brokenAlert } from '@/test/alerts'
import { renderWithProviders } from '@/test/render'
import AlertsList from './AlertsList'

beforeEach(() => {
  vi.restoreAllMocks()
})

function renderList(path = '/projects/1/alerts') {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/projects/:id/alerts" element={<AlertsList projectId={1} />} />
      </Routes>
    </MemoryRouter>
  )
}

describe('AlertsList', () => {
  it('lists the alerts as the server sends them, each a link to its page keeping the range', async () => {
    vi.spyOn(endpoints, 'alerts').mockResolvedValue({
      max_live: 50,
      alerts: [
        alert({ id: 3, name: 'Conversion', unit: 'fraction', condition: { type: 'threshold', op: 'below', value: 0.03 }, window_days: 7, state: 'breached', last_value_text: '2.1%' }),
        alert({ id: 5, name: 'Signups', condition: { type: 'change', direction: 'drop', percent: 30, against: 'previous' }, window_days: 7, last_value_text: '1,240' }),
      ],
    })
    renderList('/projects/1/alerts?range=7d&x=1')
    const list = await screen.findByRole('list', { name: 'Alerts' })
    const links = within(list).getAllByRole('link')
    expect(links.map((l) => l.getAttribute('href'))).toEqual(['/projects/1/alerts/3?range=7d', '/projects/1/alerts/5?range=7d'])
    expect(links[0]).toHaveAccessibleName(/^Conversion/)
    expect(links[1]).toHaveAccessibleName(/^Signups/)
    expect(within(links[0]).getByText('below 3% over 7 days')).toBeInTheDocument()
    expect(within(links[0]).getByText('2.1%')).toBeInTheDocument()
    expect(within(links[0]).getByText('Breached')).toBeInTheDocument()
    expect(within(links[1]).getByText('drops 30% against the 7 days before')).toBeInTheDocument()
    expect(within(links[1]).getByText('1,240')).toBeInTheDocument()
    expect(within(links[1]).getByText('OK')).toBeInTheDocument()
    expect(endpoints.alerts).toHaveBeenCalledWith(1, false)
  })

  it('offers a template or an agent when the project has none', async () => {
    vi.spyOn(endpoints, 'alerts').mockResolvedValue({ max_live: 50, alerts: [] })
    renderList()
    const empty = await screen.findByRole('region', { name: 'No alerts yet' })
    expect(empty).toHaveTextContent(
      'Create one from a template, or ask an agent connected over MCP: for example, to alert you when signups drop 30% against the week before.'
    )
    expect(within(empty).getByRole('button', { name: 'New alert' })).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Alerts' })).toBeNull()
  })

  it('lists the archived alerts with ?show=archived, and says when there are none', async () => {
    vi.spyOn(endpoints, 'alerts').mockResolvedValue({ max_live: 50, alerts: [] })
    renderList('/projects/1/alerts?show=archived')
    expect(await screen.findByText('Nothing archived')).toBeInTheDocument()
    expect(endpoints.alerts).toHaveBeenCalledWith(1, true)
    expect(screen.queryByRole('region', { name: 'No alerts yet' })).toBeNull()
  })

  it('shows a broken alert with its reason and no condition line', async () => {
    vi.spyOn(endpoints, 'alerts').mockResolvedValue({ max_live: 50, alerts: [brokenAlert()] })
    renderList()
    const link = await screen.findByRole('link', { name: /^Old funnel/ })
    expect(link).toHaveAttribute('href', '/projects/1/alerts/9')
    expect(within(link).getByText('A rule renamed checkout to purchase')).toBeInTheDocument()
    expect(within(link).getByText('Broken')).toBeInTheDocument()
    expect(within(link).getByText('—')).toBeInTheDocument()
    expect(link.querySelector('[data-condition]')).toBeNull()
  })

  it('shows the reason only for a broken alert', async () => {
    vi.spyOn(endpoints, 'alerts').mockResolvedValue({ max_live: 50, alerts: [alert({ state: 'no_data', reason: 'No events named signup yet' })] })
    renderList()
    const link = await screen.findByRole('link', { name: /^Signups/ })
    expect(within(link).queryByText('No events named signup yet')).toBeNull()
  })

  it('says why the alerts did not load, with Retry', async () => {
    vi.spyOn(endpoints, 'alerts').mockRejectedValue(new ApiError(500, 'boom', 'internal'))
    renderList()
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the alerts. boom")
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })
  it('closes the wizard when it can no longer be shown, so it does not reopen by itself', async () => {
    vi.spyOn(endpoints, 'alerts').mockResolvedValue({ max_live: 50, alerts: [alert()] })
    const tree = (archived: boolean) => (
      <MemoryRouter initialEntries={['/projects/1/alerts']}>
        <Routes>
          <Route path="/projects/:id/alerts" element={<AlertsList projectId={1} projectArchived={archived} />} />
        </Routes>
      </MemoryRouter>
    )
    const { client, rerender } = renderWithProviders(tree(false))
    const wrap = (archived: boolean) => (
      <QueryClientProvider client={client}>
        <TooltipProvider>{tree(archived)}</TooltipProvider>
      </QueryClientProvider>
    )
    await userEvent.click(await screen.findByRole('button', { name: 'New alert' }))
    expect(await screen.findByRole('dialog', { name: 'New alert' })).toBeInTheDocument()
    rerender(wrap(true))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    rerender(wrap(false))
    await screen.findByRole('button', { name: 'New alert' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
