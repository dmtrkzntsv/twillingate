import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, endpoints } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import AddBreakdownDialog from './AddBreakdownDialog'

const answer = {
  project_id: 1, from: '2026-09-05', to: '2026-10-04', values_cap: 50, breakdowns_used: 3, breakdowns_max: 10, keys_total: 3,
  keys: [
    { key: 'order_id', events: 980, max_values: 412, received: true, declared: false },
    { key: 'plan', events: 900, max_values: 3, received: true, declared: true },
    { key: 'tier', events: 20, max_values: 2, received: true, declared: false },
    { key: 'never_sent', events: 0, max_values: null, received: false, declared: true },
  ],
}

function renderDialog(over: { onAdd?: (k: string) => Promise<boolean>; onOpenChange?: (o: boolean) => void; pending?: boolean } = {}) {
  const onAdd = over.onAdd ?? vi.fn().mockResolvedValue(true)
  const onOpenChange = over.onOpenChange ?? vi.fn()
  renderWithProviders(
    <AddBreakdownDialog open onOpenChange={onOpenChange} projectId={1} declared={['plan', 'never_sent']} pending={over.pending} onAdd={onAdd} />,
  )
  return { onAdd, onOpenChange }
}

const dayMs = 86_400_000
const span = (call: { from?: string; to?: string }) => Math.round((Date.parse(call.to!) - Date.parse(call.from!)) / dayMs)

describe('AddBreakdownDialog', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue(answer)
  })

  it('lists only the received keys not declared yet, with what each received', async () => {
    renderDialog()
    expect(await screen.findByRole('radio', { name: /order_id/ })).not.toBeChecked()
    expect(screen.getByRole('radio', { name: /tier/ })).toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: /plan/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: /never_sent/ })).not.toBeInTheDocument()
    expect(screen.getByText('980 events · 412 values · folds past 50')).toBeInTheDocument()
    expect(screen.getByText(/3 of 10 in use/)).toBeInTheDocument()
  })

  it('keeps Add disabled until a key is picked, then adds it and closes', async () => {
    const user = userEvent.setup()
    const { onAdd, onOpenChange } = renderDialog()
    const add = screen.getByRole('button', { name: 'Add' })
    await user.click(await screen.findByRole('radio', { name: /tier/ }))
    expect(add).toBeEnabled()
    await user.click(screen.getByRole('radio', { name: /order_id/ }))
    expect(screen.getByRole('radio', { name: /tier/ })).not.toBeChecked()
    await user.click(add)
    expect(onAdd).toHaveBeenCalledWith('order_id')
    await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('disables Add before any pick', async () => {
    renderDialog()
    await screen.findByRole('radio', { name: /tier/ })
    expect(screen.getByRole('button', { name: 'Add' })).toBeDisabled()
  })

  it('stays open when the save is refused', async () => {
    const user = userEvent.setup()
    const { onAdd, onOpenChange } = renderDialog({ onAdd: vi.fn().mockResolvedValue(false) })
    await user.click(await screen.findByRole('radio', { name: /tier/ }))
    await user.click(screen.getByRole('button', { name: 'Add' }))
    expect(onAdd).toHaveBeenCalledWith('tier')
    expect(onOpenChange).not.toHaveBeenCalled()
    expect(screen.getByRole('radio', { name: /tier/ })).toBeChecked()
  })

  it('disables Add while a save is pending', async () => {
    const user = userEvent.setup()
    renderDialog({ pending: true })
    await user.click(await screen.findByRole('radio', { name: /tier/ }))
    expect(screen.getByRole('button', { name: 'Add' })).toBeDisabled()
  })

  it('disables Add at the limit, with the reason', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({ ...answer, breakdowns_used: 10 })
    renderDialog()
    await user.click(await screen.findByRole('radio', { name: /tier/ }))
    const add = screen.getByRole('button', { name: 'Add' })
    expect(add).toBeDisabled()
    expect(add).toHaveAttribute('title', '10 of 10 in use')
  })

  it('asks for 30 days first and refetches for 7 when toggled', async () => {
    const user = userEvent.setup()
    const spy = vi.mocked(endpoints.receivedAttributes)
    renderDialog()
    await screen.findByRole('radio', { name: /tier/ })
    expect(spy.mock.calls[0][0]).toMatchObject({ project_id: 1 })
    expect(span(spy.mock.calls[0][0])).toBe(29)
    await user.click(screen.getByRole('radio', { name: '7 days' }))
    await vi.waitFor(() => expect(span(spy.mock.calls.at(-1)![0])).toBe(6))
    expect(spy.mock.calls.at(-1)![0].to).toBe(spy.mock.calls[0][0].to)
  })

  it('says none arrived when every received key is declared', async () => {
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({ ...answer, keys: answer.keys.slice(1, 2), keys_total: 1 })
    renderDialog()
    expect(await screen.findByText('No new attributes received in this range.')).toBeInTheDocument()
  })

  it('says it is loading, not that nothing arrived, until the first answer', async () => {
    vi.spyOn(endpoints, 'receivedAttributes').mockReturnValue(new Promise(() => {}))
    renderDialog()
    expect(await screen.findByText('Loading…')).toBeInTheDocument()
    expect(screen.queryByText('No new attributes received in this range.')).not.toBeInTheDocument()
  })

  it('says the load failed with a Retry', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'receivedAttributes').mockRejectedValue(new ApiError(500, 'boom'))
    renderDialog()
    expect(await screen.findByText(/Couldn't load received attributes/)).toBeInTheDocument()
    expect(screen.queryByText('No new attributes received in this range.')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })

  it('says when the server listed only the busiest keys', async () => {
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({ ...answer, keys_total: 1234 })
    renderDialog()
    expect(await screen.findByText('Showing the 3 busiest of 1,234 keys.')).toBeInTheDocument()
  })

  it('says nothing about a cap when every received key is listed', async () => {
    renderDialog()
    await screen.findByRole('radio', { name: /tier/ })
    expect(screen.queryByText(/busiest of/)).not.toBeInTheDocument()
  })

  it('asks for nothing while closed', () => {
    renderWithProviders(<AddBreakdownDialog open={false} onOpenChange={vi.fn()} projectId={1} declared={[]} onAdd={vi.fn()} />)
    expect(endpoints.receivedAttributes).not.toHaveBeenCalled()
  })

  it('forgets the pick when it is reopened', async () => {
    const user = userEvent.setup()
    function Harness() {
      const [open, setOpen] = useState(true)
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>Reopen</button>
          <AddBreakdownDialog open={open} onOpenChange={setOpen} projectId={1} declared={['plan']} onAdd={vi.fn().mockResolvedValue(false)} />
        </>
      )
    }
    renderWithProviders(<Harness />)
    await user.click(await screen.findByRole('radio', { name: /tier/ }))
    expect(screen.getByRole('radio', { name: /tier/ })).toBeChecked()
    await user.keyboard('{Escape}')
    await vi.waitFor(() => expect(screen.queryByRole('radio', { name: /tier/ })).not.toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: 'Reopen' }))
    expect(await screen.findByRole('radio', { name: /tier/ })).not.toBeChecked()
    expect(screen.getByRole('button', { name: 'Add' })).toBeDisabled()
  })
})
