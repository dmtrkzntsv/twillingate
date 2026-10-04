import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, endpoints, type Project } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import DetailsSection from './DetailsSection'

const project = { project_id: 1, name: 'dev', allowed_origins: ['https://a.example', '*'], attributes: ['plan', 'never_sent'] } as Project

describe('DetailsSection', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({
      project_id: 1, from: '2026-09-05', to: '2026-10-04', values_cap: 50, breakdowns_used: 2, breakdowns_max: 50, keys_total: 1,
      keys: [
        { key: 'plan', events: 900, max_values: 3, received: true, declared: true },
        { key: 'never_sent', events: 0, max_values: null, received: false, declared: true },
      ],
    })
  })

  it('lists origins one per line and breakdowns with what they received', async () => {
    renderWithProviders(<DetailsSection project={project} range={{ from: '2026-09-05', to: '2026-10-04' }} onSave={vi.fn()} />)
    const origins = screen.getByRole('list', { name: 'Allowed origins' })
    expect(within(origins).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['https://a.example', '*'])
    expect(await screen.findByText('900 events · 3 values')).toBeInTheDocument()
    expect(screen.getByText('not received')).toBeInTheDocument()
    expect(endpoints.receivedAttributes).toHaveBeenCalledWith({ project_id: 1, from: '2026-09-05', to: '2026-10-04' })
  })

  it('shows the keys alone while the counts load', () => {
    vi.spyOn(endpoints, 'receivedAttributes').mockReturnValue(new Promise(() => {}))
    renderWithProviders(<DetailsSection project={project} range={{ from: 'a', to: 'b' }} onSave={vi.fn()} />)
    const list = screen.getByRole('list', { name: 'Breakdowns' })
    expect(within(list).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['plan', 'never_sent'])
  })

  it('says the counts could not load, keeps the keys, and retries', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'receivedAttributes').mockRejectedValue(new ApiError(500, 'boom'))
    renderWithProviders(<DetailsSection project={project} range={{ from: 'a', to: 'b' }} onSave={vi.fn()} />)
    expect(await screen.findByText(/Couldn't load received attributes/)).toBeInTheDocument()
    expect(within(screen.getByRole('list', { name: 'Breakdowns' })).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['plan', 'never_sent'])
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })
})
