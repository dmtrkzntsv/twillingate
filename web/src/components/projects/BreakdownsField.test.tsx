import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { endpoints } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import BreakdownsField, { budget, describeKey } from './BreakdownsField'

const answer = {
  project_id: 1, from: '2026-09-05', to: '2026-10-04', values_cap: 50, breakdowns_used: 3, breakdowns_max: 3,
  keys: [
    { key: 'order_id', events: 980, max_values: 412, declared: false },
    { key: 'plan', events: 900, max_values: 3, declared: true },
    { key: '$path', events: 900, max_values: 38, declared: false },
  ],
}

function Harness({ initial, onProblem }: { initial: string[]; onProblem: (p: string | null) => void }) {
  const [value, setValue] = useState(initial)
  return <BreakdownsField projectId={1} initial={initial} value={value} onChange={setValue} onProblem={onProblem} />
}

describe('budget', () => {
  it('counts the dialog edits against the server total', () => {
    expect(budget(3, 3, ['plan'], ['plan'])).toEqual({ used: 3, over: false })
    expect(budget(3, 3, ['plan'], ['plan', 'order_id'])).toEqual({ used: 4, over: true })
    expect(budget(3, 3, ['plan'], ['order_id'])).toEqual({ used: 3, over: false })
    expect(budget(3, 0, [], ['a', 'b'])).toEqual({ used: 5, over: false })
  })
})

describe('describeKey', () => {
  it('says what a key received, and when it is not received', () => {
    expect(describeKey({ key: 'plan', events: 900, max_values: 3, declared: true }, 50)).toBe('900 events · 3 values')
    expect(describeKey({ key: 'k', events: 5, max_values: 1, declared: true }, 50)).toBe('5 events · 1 value')
    expect(describeKey({ key: 'k', events: 5, max_values: null, declared: true }, 50)).toBe('5 events · values counted tonight')
    expect(describeKey({ key: 'k', events: 0, max_values: null, declared: true }, 50)).toBe('not received')
  })
})

describe('BreakdownsField', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue(answer)
  })

  it('lists received keys with events and values, flags a key that folds, and counts the budget', async () => {
    const user = userEvent.setup()
    const onProblem = vi.fn()
    renderWithProviders(<Harness initial={['plan']} onProblem={onProblem} />)
    expect(await screen.findByRole('checkbox', { name: /order_id/ })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: /plan/ })).toBeChecked()
    expect(screen.getByText(/412 values · folds past 50/)).toBeInTheDocument()
    expect(screen.getByText(/3 of 3 in use/)).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: /order_id/ }))
    expect(screen.getByText(/4 of 3 in use/)).toBeInTheDocument()
    expect(onProblem).toHaveBeenLastCalledWith(expect.stringMatching(/over the limit/))
    // Unchecking a selected key frees its slot before saving.
    await user.click(screen.getByRole('checkbox', { name: /plan/ }))
    expect(onProblem).toHaveBeenLastCalledWith(null)
  })

  it('adds a key not received yet, and refuses a $ key there', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Harness initial={[]} onProblem={vi.fn()} />)
    const input = await screen.findByRole('textbox', { name: 'Key not received yet' })
    await user.type(input, '$host{Enter}')
    expect(screen.getByText(/\$ keys appear in the list once received/)).toBeInTheDocument()
    await user.clear(input)
    await user.type(input, 'tier{Enter}')
    expect(screen.getByRole('checkbox', { name: /tier/ })).toBeChecked()
  })
})
