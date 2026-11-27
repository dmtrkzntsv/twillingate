import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { endpoints } from '@/lib/api'
import { alert } from '@/test/alerts'
import { renderWithProviders } from '@/test/render'
import AlertTabs from './AlertTabs'

beforeEach(() => {
  vi.restoreAllMocks()
})

function Where() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}

function renderTabs(path: string, alertId?: string, search = 'range=7d') {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/projects/:id/alerts" element={<AlertTabs projectId={1} search={search} />} />
        <Route path="/projects/:id/alerts/:alertId" element={<AlertTabs projectId={1} alertId={alertId} search={search} />} />
      </Routes>
      <Where />
    </MemoryRouter>
  )
}

const links = () => within(screen.getByRole('navigation', { name: 'Alert lists' })).getAllByRole('link')
const current = () => links().find((l) => l.getAttribute('aria-current') === 'page')?.textContent ?? null

describe('AlertTabs', () => {
  it('links Live and Archived to the lists, keeping the range', () => {
    renderTabs('/projects/1/alerts?range=7d')
    expect(links().map((l) => l.textContent)).toEqual(['Live', 'Archived'])
    expect(links().map((l) => l.getAttribute('href'))).toEqual(['/projects/1/alerts?range=7d', '/projects/1/alerts?show=archived&range=7d'])
  })

  it('marks Live current on the list, and Archived with ?show=archived', () => {
    const { unmount } = renderTabs('/projects/1/alerts?range=7d')
    expect(current()).toBe('Live')
    unmount()
    renderTabs('/projects/1/alerts?show=archived&range=7d')
    expect(current()).toBe('Archived')
  })

  it("asks for no list on the list page, whose own query is the list's", () => {
    const spy = vi.spyOn(endpoints, 'alerts')
    renderTabs('/projects/1/alerts')
    expect(spy).not.toHaveBeenCalled()
  })

  it('marks Live current on the page of a live alert, and leads back to the list from it', async () => {
    vi.spyOn(endpoints, 'alerts').mockResolvedValue({ max_live: 50, alerts: [alert({ id: 12 })] })
    renderTabs('/projects/1/alerts/12', '12', '')
    await vi.waitFor(() => expect(current()).toBe('Live'))
    expect(endpoints.alerts).toHaveBeenCalledWith(1, false)
    const live = screen.getByRole('link', { name: 'Live' })
    expect(live).toHaveAttribute('href', '/projects/1/alerts')
    await userEvent.click(live)
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/projects\/1\/alerts$/)
  })

  it('marks Archived current on the page of an alert the live list lacks', async () => {
    vi.spyOn(endpoints, 'alerts').mockResolvedValue({ max_live: 50, alerts: [alert({ id: 3 })] })
    renderTabs('/projects/1/alerts/12', '12')
    await vi.waitFor(() => expect(current()).toBe('Archived'))
  })

  it('marks none current while the live list loads', () => {
    vi.spyOn(endpoints, 'alerts').mockReturnValue(new Promise(() => {}))
    renderTabs('/projects/1/alerts/12', '12')
    expect(links()).toHaveLength(2)
    expect(current()).toBeNull()
  })
})
