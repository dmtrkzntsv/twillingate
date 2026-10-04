import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, endpoints, type Project } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import BreakdownsSection from './BreakdownsSection'

const project = { project_id: 1, name: 'dev', allowed_origins: [], attributes: ['plan', 'never_sent'] } as Project
const range = { from: '2026-09-05', to: '2026-10-04' }
const answer = {
  project_id: 1, from: '2026-09-05', to: '2026-10-04', values_cap: 50, breakdowns_used: 2, breakdowns_max: 10, keys_total: 3,
  keys: [
    { key: 'plan', events: 900, max_values: 3, received: true, declared: true },
    { key: 'never_sent', events: 0, max_values: null, received: false, declared: true },
    { key: 'order_id', events: 980, max_values: 412, received: true, declared: false },
  ],
}

function renderSection(p: Project = project, onSave = vi.fn().mockResolvedValue(true)) {
  renderWithProviders(<BreakdownsSection project={p} range={range} onSave={onSave} />)
  return onSave
}

describe('BreakdownsSection', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue(answer)
  })

  it('lists the declared keys with what each received, and the budget in the header', async () => {
    renderSection()
    const section = screen.getByRole('region', { name: 'Breakdowns' })
    expect(await within(section).findByText('900 events · 3 values')).toBeInTheDocument()
    expect(within(section).getByText('not received')).toBeInTheDocument()
    expect(within(section).queryByText('order_id')).not.toBeInTheDocument()
    expect(within(section).getAllByRole('row').slice(1).map((r) => within(r).getAllByRole('cell')[0].textContent)).toEqual(['plan', 'never_sent'])
    expect(within(section).getByText(/2 of 10 in use/)).toBeInTheDocument()
    expect(endpoints.receivedAttributes).toHaveBeenCalledWith({ project_id: 1, from: '2026-09-05', to: '2026-10-04' })
  })

  it('says "N in use" when there is no limit', async () => {
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({ ...answer, breakdowns_max: 0 })
    renderSection()
    expect(await screen.findByText(/· 2 in use/)).toBeInTheDocument()
  })

  it('shows the keys alone while the counts load', () => {
    vi.spyOn(endpoints, 'receivedAttributes').mockReturnValue(new Promise(() => {}))
    renderSection()
    const rows = screen.getAllByRole('row').slice(1)
    expect(rows.map((r) => within(r).getAllByRole('cell')[0].textContent)).toEqual(['plan', 'never_sent'])
    expect(screen.queryByText(/in use/)).not.toBeInTheDocument()
  })

  it('disables Add breakdown at the limit, with the reason', async () => {
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({ ...answer, breakdowns_used: 10 })
    renderSection()
    await screen.findByText(/10 of 10 in use/)
    const add = screen.getByRole('button', { name: 'Add breakdown' })
    expect(add).toBeDisabled()
    expect(add).toHaveAttribute('title', '10 of 10 in use')
  })

  it('keeps Add breakdown enabled under the limit', async () => {
    renderSection()
    await screen.findByText(/2 of 10 in use/)
    expect(screen.getByRole('button', { name: 'Add breakdown' })).toBeEnabled()
  })

  it('asks before removing, then saves the list without the key', async () => {
    const user = userEvent.setup()
    const onSave = renderSection()
    await user.click(await screen.findByRole('button', { name: 'Remove plan' }))
    expect(await screen.findByText('Stop breaking down plan?')).toBeInTheDocument()
    expect(onSave).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Remove breakdown' }))
    expect(onSave).toHaveBeenCalledWith(['never_sent'])
  })

  it('removes nothing on Cancel, and keeps the key in the title while the dialog closes', async () => {
    const user = userEvent.setup()
    const onSave = renderSection()
    await user.click(await screen.findByRole('button', { name: 'Remove plan' }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.queryByText('Stop breaking down null?')).not.toBeInTheDocument()
    expect(screen.queryByText('Stop breaking down undefined?')).not.toBeInTheDocument()
  })

  it('says there are none, and how to add one', async () => {
    renderSection({ ...project, attributes: [] })
    expect(await screen.findByText(/No breakdowns yet\. Add one to break product events and measures down by an attribute\./)).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('treats a project with no attributes list as having none', async () => {
    renderSection({ project_id: 1, name: 'dev', allowed_origins: [] } as Project)
    expect(await screen.findByText(/No breakdowns yet/)).toBeInTheDocument()
  })

  it('says the load failed with a Retry, keeps the keys listed bare', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'receivedAttributes').mockRejectedValue(new ApiError(500, 'boom'))
    renderSection()
    expect(await screen.findByText(/Couldn't load received attributes/)).toBeInTheDocument()
    expect(screen.getByText(/boom/)).toBeInTheDocument()
    expect(screen.getAllByRole('row').slice(1).map((r) => within(r).getAllByRole('cell')[0].textContent)).toEqual(['plan', 'never_sent'])
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })

  it('adds a key through the dialog by saving the list with it appended', async () => {
    const user = userEvent.setup()
    const onSave = renderSection()
    await user.click(await screen.findByRole('button', { name: 'Add breakdown' }))
    await user.click(await screen.findByRole('radio', { name: /order_id/ }))
    await user.click(screen.getByRole('button', { name: 'Add' }))
    expect(onSave).toHaveBeenCalledWith(['plan', 'never_sent', 'order_id'])
  })
})
