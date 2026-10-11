import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { DashboardInfo } from '@/lib/api'
import type { CopyTarget } from '@/lib/arrange'
import CopyWidgetDialog from './CopyWidgetDialog'

function info(dashboard_id: number, title: string, group_id: number): DashboardInfo {
  return { dashboard_id, title, owner: 'user', group_id, widgets: 1, sidebar: true, project_tab: false }
}

const targets: CopyTarget[] = [
  { groupId: 13, name: 'Growth', tabs: [info(13, 'Marketing', 13), info(14, 'Funnel', 13)] },
  { groupId: 10, name: 'Launch week', tabs: [info(10, 'Launch week', 10)] },
]

function renderDialog(over: Partial<Parameters<typeof CopyWidgetDialog>[0]> = {}) {
  const props = {
    open: true,
    onOpenChange: vi.fn(),
    label: 'Visitors',
    targets,
    onCopy: vi.fn().mockResolvedValue(true),
    onCreate: vi.fn().mockResolvedValue(true),
    ...over,
  }
  render(<CopyWidgetDialog {...props} />)
  return props
}

describe('CopyWidgetDialog', () => {
  it('heads a group of several tabs with its name, and shows a lone one by itself', () => {
    renderDialog()
    const group = screen.getByRole('group', { name: 'Growth' })
    expect(within(group).getAllByRole('button').map((b) => b.textContent)).toEqual(['Marketing', 'Funnel'])
    expect(screen.queryByRole('group', { name: 'Launch week' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Launch week' })).toBeInTheDocument()
  })

  it('copies on a click and closes once the server took it', async () => {
    const props = renderDialog()
    await userEvent.click(screen.getByRole('button', { name: 'Funnel' }))
    expect(props.onCopy).toHaveBeenCalledWith(targets[0].tabs[1])
    await waitFor(() => expect(props.onOpenChange).toHaveBeenCalledWith(false))
  })

  it('stays open when the copy is refused', async () => {
    const props = renderDialog({ onCopy: vi.fn().mockResolvedValue(false) })
    await userEvent.click(screen.getByRole('button', { name: 'Funnel' }))
    await waitFor(() => expect(props.onCopy).toHaveBeenCalled())
    expect(props.onOpenChange).not.toHaveBeenCalled()
  })

  it('makes a new dashboard from a title of at least 2 characters', async () => {
    const props = renderDialog({ targets: [] })
    expect(screen.getByText('No other dashboards of your own yet')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'New dashboard…' }))
    const field = screen.getByRole('textbox', { name: 'Dashboard title' })
    await userEvent.type(field, 'W')
    expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled()
    await userEvent.type(field, 'eekly {Enter}')
    expect(props.onCreate).toHaveBeenCalledWith('Weekly')
    await waitFor(() => expect(props.onOpenChange).toHaveBeenCalledWith(false))
  })
})
