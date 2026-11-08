import { beforeAll, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { DashboardTab } from '@/lib/api'
import ReportTabs, { dashboardTabs } from './ReportTabs'

const tabs = dashboardTabs([
  { dashboard_id: 13, title: 'Marketing' },
  { dashboard_id: 14, title: 'Funnel' },
  { dashboard_id: 15, title: 'Spend' },
])

// jsdom lacks the pointer-capture and scrolling calls Radix Select makes when it opens.
beforeAll(() => {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.releasePointerCapture ??= () => {}
  Element.prototype.scrollIntoView ??= () => {}
})

describe('ReportTabs', () => {
  it('renders plain tabs without onMove', () => {
    render(<ReportTabs tabs={tabs} currentId={13} onSelect={() => {}} />)
    for (const tab of screen.getAllByRole('tab')) expect(tab).not.toHaveAttribute('aria-roledescription')
  })

  it('makes every tab sortable with onMove, still as a tab', () => {
    render(<ReportTabs tabs={tabs} currentId={13} onSelect={() => {}} sortable onMove={async () => true} />)
    const all = screen.getAllByRole('tab')
    expect(all.map((t) => t.textContent)).toEqual(['Marketing', 'Funnel', 'Spend'])
    for (const tab of all) expect(tab).toHaveAttribute('aria-roledescription', 'sortable')
    // Radix keeps its roving tab stop (the list until a tab is focused),
    // not dnd-kit's tabindex 0 on every tab.
    expect(all.map((t) => t.getAttribute('tabindex'))).toEqual(['-1', '-1', '-1'])
    expect(screen.getByRole('tablist')).toHaveAttribute('tabindex', '0')
  })

  it('selects a sortable tab on a click, once', async () => {
    const onSelect = vi.fn()
    render(<ReportTabs tabs={tabs} currentId={13} onSelect={onSelect} sortable onMove={async () => true} />)

    await userEvent.click(screen.getByRole('tab', { name: 'Funnel' }))

    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith(14)
  })

  it('keeps arrow keys moving focus and Enter selecting on sortable tabs', async () => {
    const onSelect = vi.fn()
    render(<ReportTabs tabs={tabs} currentId={13} onSelect={onSelect} sortable onMove={async () => true} />)

    screen.getByRole('tab', { name: 'Marketing' }).focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(screen.getByRole('tab', { name: 'Funnel' })).toHaveFocus()
    expect(onSelect).not.toHaveBeenCalled()

    await userEvent.keyboard('{Enter}')
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith(14)
  })

  it('keeps a sortable list with no onMove (a frozen page) without the sortable description', () => {
    render(<ReportTabs tabs={tabs} currentId={13} onSelect={() => {}} sortable />)
    for (const tab of screen.getAllByRole('tab')) expect(tab).not.toHaveAttribute('aria-roledescription')
  })

  it('does not select on a modified click', async () => {
    const onSelect = vi.fn()
    const user = userEvent.setup()
    render(<ReportTabs tabs={tabs} currentId={13} onSelect={onSelect} sortable onMove={async () => true} />)

    await user.keyboard('{Control>}')
    await user.click(screen.getByRole('tab', { name: 'Funnel' }))
    await user.keyboard('{/Control}')

    expect(onSelect).not.toHaveBeenCalled()
  })

  it('drags every tab and renders trailing', () => {
    const projectTabs: DashboardTab[] = [
      { dashboard_id: 1, title: 'Views' },
      { dashboard_id: 1001, title: 'Mine' },
      { dashboard_id: 1002, title: 'Ours' },
    ]
    render(
      <ReportTabs
        tabs={dashboardTabs(projectTabs)}
        currentId={1}
        onSelect={() => {}}
        sortable
        onMove={async () => true}
        trailing={<button type="button">Add tab</button>}
      />
    )
    const all = screen.getAllByRole('tab')
    expect(all.map((t) => t.textContent)).toEqual(['Views', 'Mine', 'Ours'])
    expect(all.map((t) => t.getAttribute('aria-roledescription'))).toEqual(['sortable', 'sortable', 'sortable'])
    expect(screen.getByRole('button', { name: 'Add tab' })).toBeInTheDocument()
  })

  it('selects no tab, and the phone select says "Tabs", when the current id is none of them', () => {
    render(<ReportTabs tabs={tabs} currentId={0} onSelect={() => {}} />)
    expect(screen.queryByRole('tab', { selected: true })).toBeNull()
    expect(screen.getByRole('combobox', { name: 'Tab' })).toHaveTextContent('Tabs')
  })

  it('offers no phone select when there are no tabs, only the trailing control', () => {
    render(<ReportTabs tabs={[]} currentId={0} onSelect={() => {}} trailing={<button type="button">Add tab</button>} />)
    expect(screen.queryByRole('combobox', { name: 'Tab' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Add tab' })).toBeInTheDocument()
  })

  it("shows a tab's icon and badge, names it by its label, and lists the label in the phone select", async () => {
    const user = userEvent.setup()
    render(
      <ReportTabs
        tabs={[
          {
            id: 1,
            title: 'contact',
            icon: <svg data-testid="draft" />,
            badge: <span data-testid="new">3</span>,
            label: 'contact (draft, 3 new)',
          },
          { id: 2, title: 'survey' },
        ]}
        currentId={2}
        onSelect={() => {}}
        sortable
        onMove={async () => true}
      />
    )
    const tab = screen.getByRole('tab', { name: 'contact (draft, 3 new)' })
    expect(tab).toContainElement(screen.getByTestId('draft'))
    expect(tab).toContainElement(screen.getByTestId('new'))
    expect(screen.getByRole('tab', { name: 'survey' })).toBeInTheDocument()
    await user.click(screen.getByRole('combobox', { name: 'Tab' }))
    expect(await screen.findByRole('option', { name: 'contact (draft, 3 new)' })).toBeInTheDocument()
  })

  it('still selects on a virtual click after Space picked a tab up and Escape put it back', async () => {
    const onSelect = vi.fn()
    render(<ReportTabs tabs={tabs} currentId={13} onSelect={onSelect} sortable onMove={async () => true} />)
    const funnel = screen.getByRole('tab', { name: 'Funnel' })

    funnel.focus()
    await userEvent.keyboard(' ')
    await userEvent.keyboard('{Escape}')
    await new Promise((r) => setTimeout(r, 60))
    // A screen reader's click: no key press or pointer before it.
    fireEvent.click(funnel, { detail: 0 })

    await waitFor(() => expect(onSelect).toHaveBeenCalledWith(14))
  })
})
