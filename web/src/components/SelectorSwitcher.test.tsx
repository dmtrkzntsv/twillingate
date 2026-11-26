import { useState } from 'react'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { endpoints, type SelectorValue } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import SelectorSwitcher from './SelectorSwitcher'

const deployment = { name: 'deployment', label: 'Deployment', column: 'attr_deployment' }

function values(n: number): SelectorValue[] {
  return Array.from({ length: n }, (_, i) => ({ value: `v${i + 1}`, count: 100 - i }))
}

/** The switcher holding its own value, as a page holds it in the URL. */
function Harness({ initial = [], onChange }: { initial?: string[]; onChange?: (v: string[]) => void }) {
  const [value, setValue] = useState<string[]>(initial)
  return (
    <SelectorSwitcher
      dashboardId={12}
      selector={deployment}
      value={value}
      projectIds={[7, 1]}
      from="2026-09-20"
      to="2026-09-26"
      onChange={(v) => {
        setValue(v)
        onChange?.(v)
      }}
    />
  )
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('SelectorSwitcher', () => {
  it('reads All, then the one value picked, then how many', async () => {
    const user = userEvent.setup()
    const load = vi.spyOn(endpoints, 'selectorValues').mockResolvedValue({
      values: [
        { value: 'cloud', count: 40 },
        { value: 'self-hosted', count: 12 },
      ],
    })
    renderWithProviders(<Harness />)

    await user.click(screen.getByRole('button', { name: 'Deployment: All' }))
    const dialog = await screen.findByRole('dialog')
    // The counts from selector_values, beside each value.
    expect(await within(dialog).findByText('40')).toBeInTheDocument()
    expect(load).toHaveBeenCalledWith(12, 'deployment', { project_id: [7, 1], from: '2026-09-20', to: '2026-09-26', picked: [] })

    await user.click(within(dialog).getByRole('checkbox', { name: 'cloud' }))
    expect(screen.getByRole('button', { name: 'Deployment: cloud' })).toBeInTheDocument()
    await user.click(within(dialog).getByRole('checkbox', { name: 'self-hosted' }))
    expect(screen.getByRole('button', { name: 'Deployment: 2 selected' })).toBeInTheDocument()
    // The values picked go back as `picked`, so they stay listed.
    await waitFor(() => expect(load).toHaveBeenLastCalledWith(12, 'deployment', expect.objectContaining({ picked: ['cloud', 'self-hosted'] })))
  })

  it('clears the list with Clear', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'selectorValues').mockResolvedValue({ values: [{ value: 'cloud', count: 4 }, { value: 'edge', count: 2 }] })
    const onChange = vi.fn()
    renderWithProviders(<Harness initial={['cloud', 'edge']} onChange={onChange} />)

    await user.click(screen.getByRole('button', { name: 'Deployment: 2 selected' }))
    await user.click(await screen.findByRole('button', { name: 'Clear' }))
    expect(onChange).toHaveBeenLastCalledWith([])
    expect(screen.getByRole('button', { name: 'Deployment: All' })).toBeInTheDocument()
  })

  it('shows no search box for 10 values, and one past 10 that searches on the server', async () => {
    const user = userEvent.setup()
    const load = vi.spyOn(endpoints, 'selectorValues').mockResolvedValue({ values: values(10) })
    const { unmount } = renderWithProviders(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Deployment: All' }))
    expect(await screen.findByRole('checkbox', { name: 'v10' })).toBeInTheDocument()
    expect(screen.queryByRole('searchbox')).not.toBeInTheDocument()
    unmount()

    load.mockImplementation(async (_id, _name, q) => ({
      values: q.search ? values(11).filter((v) => v.value.includes(q.search!)) : values(11),
    }))
    renderWithProviders(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Deployment: All' }))
    const search = await screen.findByRole('searchbox', { name: 'Search Deployment' })
    await user.type(search, 'v11')
    await waitFor(() => expect(load).toHaveBeenLastCalledWith(12, 'deployment', expect.objectContaining({ search: 'v11' })))
    await waitFor(() => expect(screen.queryByRole('checkbox', { name: 'v1' })).not.toBeInTheDocument())
    // Narrowed to one value, the box stays, so typing can go on.
    expect(screen.getByRole('checkbox', { name: 'v11' })).toBeInTheDocument()
    expect(screen.getByRole('searchbox', { name: 'Search Deployment' })).toBeInTheDocument()
  })
})
