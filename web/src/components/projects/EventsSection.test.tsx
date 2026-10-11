import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, endpoints, type EventName, type EventRule, type Project } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import EventsSection from './EventsSection'

const project = { project_id: 1, name: 'dev', allowed_origins: [] } as Project
const range = { from: '2026-09-05', to: '2026-10-04' }
const names: EventName[] = [
  { family: 'product', name: 'checkout', events: 12000 },
  { family: 'product', name: 'signup', events: 340 },
  { family: 'measures', name: 'checkout', events: 55 },
  { family: 'product', name: 'old_signup', events: 7 },
]
const rules: EventRule[] = [
  { family: 'product', name: 'a', action: 'rename', to: 'b' },
  { family: 'product', name: 'x', action: 'block' },
]

function renderSection(props: Partial<Parameters<typeof EventsSection>[0]> = {}) {
  const onRename = vi.fn().mockResolvedValue(true)
  const onBlock = vi.fn().mockResolvedValue(true)
  const onRemove = vi.fn().mockResolvedValue(true)
  renderWithProviders(<EventsSection project={project} range={range} rawDays={7} onRename={onRename} onBlock={onBlock} onRemove={onRemove} {...props} />)
  return { onRename, onBlock, onRemove }
}

async function openMenu(user: ReturnType<typeof userEvent.setup>, name: string, item: string, family = 'product') {
  await user.click(await screen.findByRole('button', { name: `Actions for ${name} (${family})` }))
  await user.click(await screen.findByRole('menuitem', { name: item }))
}

describe('EventsSection', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'eventNames').mockResolvedValue({ events: names })
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [] })
  })

  it('lists the names with their counts and family, and badges a blocked one', async () => {
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'signup', action: 'block' }] })
    renderSection()
    const section = screen.getByRole('region', { name: 'Events' })
    expect(await within(section).findByText('12,000')).toBeInTheDocument()
    expect(endpoints.eventNames).toHaveBeenCalledWith(1, { from: '2026-09-05', to: '2026-10-04' })
    const rows = within(section).getAllByRole('row').slice(1)
    expect(rows.map((r) => within(r).getAllByRole('cell')[0].textContent)).toEqual([
      'checkoutproduct', 'signupproductblocked', 'checkoutmeasures', 'old_signupproduct',
    ])
    expect(within(rows[0]).getByText('12,000')).toBeInTheDocument()
    expect(within(rows[1]).getByText('340')).toBeInTheDocument()
    expect(within(rows[1]).getByText('blocked')).toBeInTheDocument()
    expect(within(rows[0]).queryByText('blocked')).not.toBeInTheDocument()
  })

  it('renames to a new name without merge', async () => {
    const user = userEvent.setup()
    const { onRename } = renderSection()
    await openMenu(user, 'signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByRole('textbox')
    await user.type(input, 'register')
    expect(within(dialog).queryByText(/cannot be undone/)).not.toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Rename' }))
    expect(onRename).toHaveBeenCalledWith({ family: 'product', from: 'signup', to: 'register' })
    expect(onRename.mock.calls[0][0]).not.toHaveProperty('merge')
  })

  it('warns and merges when the new name is already in the same family', async () => {
    const user = userEvent.setup()
    const { onRename } = renderSection()
    await openMenu(user, 'old_signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'signup')
    expect(within(dialog).getByText(/merges .* cannot be undone/)).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: 'Rename' })).not.toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Merge into signup' }))
    expect(onRename).toHaveBeenCalledWith({ family: 'product', from: 'old_signup', to: 'signup', merge: true })
  })

  it('merges onto a name the family has even when the other family has it too', async () => {
    const user = userEvent.setup()
    renderSection()
    await openMenu(user, 'signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'checkout')
    expect(within(dialog).getByRole('button', { name: 'Merge into checkout' })).toBeInTheDocument()
  })

  it('treats a name that exists only in the other family as a plain rename', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventNames').mockResolvedValue({ events: [
      { family: 'product', name: 'a', events: 3 }, { family: 'measures', name: 'lcp', events: 9 },
    ] })
    renderSection()
    await openMenu(user, 'a', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'lcp')
    expect(within(dialog).getByRole('button', { name: 'Rename' })).toBeEnabled()
  })

  it('disables the button for an empty name, a $ name and the same name', async () => {
    const user = userEvent.setup()
    renderSection()
    await openMenu(user, 'signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    const button = within(dialog).getByRole('button', { name: 'Rename' })
    const input = within(dialog).getByRole('textbox')
    expect(button).toBeDisabled()
    await user.type(input, '$x')
    expect(button).toBeDisabled()
    await user.clear(input)
    await user.type(input, '  signup  ')
    expect(button).toBeDisabled()
    await user.clear(input)
    await user.type(input, ' fresh ')
    expect(button).toBeEnabled()
  })

  it('turns the dialog into a merge confirmation when the server refuses a name the range does not list', async () => {
    const user = userEvent.setup()
    const { onRename } = renderSection()
    onRename.mockResolvedValueOnce('conflict').mockResolvedValue(true)
    await openMenu(user, 'signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'older')
    expect(within(dialog).queryByText(/cannot be undone/)).not.toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Rename' }))
    expect(onRename).toHaveBeenLastCalledWith({ family: 'product', from: 'signup', to: 'older' })
    expect(await within(dialog).findByText(/merges .* cannot be undone/)).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Merge into older' }))
    expect(onRename).toHaveBeenLastCalledWith({ family: 'product', from: 'signup', to: 'older', merge: true })
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('warns that renaming onto a blocked name drops the history, and sends merge', async () => {
    const user = userEvent.setup()
    // x is blocked in product and has no events listed; fresh is blocked in measures only.
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [
      { family: 'product', name: 'x', action: 'block' }, { family: 'measures', name: 'fresh', action: 'block' },
    ] })
    const { onRename } = renderSection()
    await openMenu(user, 'signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByRole('textbox')
    await user.type(input, 'fresh')
    expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Rename' })).toBeEnabled()
    await user.clear(input)
    await user.type(input, ' x ')
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      "x is blocked: signup's events are dropped from now on, and the next daily pass deletes signup's aggregated history.")
    expect(within(dialog).queryByRole('button', { name: 'Rename' })).not.toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Rename into blocked x' }))
    expect(onRename).toHaveBeenCalledWith({ family: 'product', from: 'signup', to: 'x', merge: true })
  })

  it('drops the merge confirmation when the name is typed over', async () => {
    const user = userEvent.setup()
    const { onRename } = renderSection()
    onRename.mockResolvedValueOnce('conflict')
    await openMenu(user, 'signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByRole('textbox')
    await user.type(input, 'older')
    await user.click(within(dialog).getByRole('button', { name: 'Rename' }))
    await within(dialog).findByRole('button', { name: 'Merge into older' })
    await user.type(input, 'x')
    expect(within(dialog).getByRole('button', { name: 'Rename' })).toBeEnabled()
    expect(within(dialog).queryByText(/cannot be undone/)).not.toBeInTheDocument()
  })

  it('names the family of a measure in the actions label and the dialogs', async () => {
    const user = userEvent.setup()
    renderSection()
    await openMenu(user, 'checkout', 'Rename…', 'measures')
    expect(await screen.findByText(/\(measures\)/)).toBeInTheDocument()
  })

  it('offers no Block for a name that already has a block rule', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'signup', action: 'block' }] })
    renderSection()
    await screen.findByText('blocked', { selector: 'div *' })
    await user.click(screen.getByRole('button', { name: 'Actions for signup (product)' }))
    expect(await screen.findByRole('menuitem', { name: 'Rename…' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: 'Block…' })).not.toBeInTheDocument()
  })

  it('always says widget SQL is not rewritten', async () => {
    const user = userEvent.setup()
    renderSection()
    await openMenu(user, 'signup', 'Rename…')
    expect(await screen.findByText(/Widgets whose SQL names the old event are not changed\./)).toBeInTheDocument()
  })

  it('confirms before blocking, naming the daily pass and the raw days', async () => {
    const user = userEvent.setup()
    const { onBlock } = renderSection({ rawDays: 14 })
    await openMenu(user, 'signup', 'Block…')
    expect(await screen.findByText('Block signup?')).toBeInTheDocument()
    expect(screen.getByText(/next daily pass \(03:00 UTC\)/)).toBeInTheDocument()
    expect(screen.getByText(/within 14 days/)).toBeInTheDocument()
    expect(onBlock).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Block event' }))
    expect(onBlock).toHaveBeenCalledWith({ family: 'product', name: 'signup' })
  })

  it('names the retention window when the raw days are unknown, and blocks nothing on Cancel', async () => {
    const user = userEvent.setup()
    const { onBlock } = renderSection({ rawDays: undefined })
    await openMenu(user, 'signup', 'Block…')
    expect(await screen.findByText(/within the raw retention window/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onBlock).not.toHaveBeenCalled()
  })

  it('lists the rules, each removable', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules })
    const { onRemove } = renderSection()
    expect(await screen.findByText('a → b')).toBeInTheDocument()
    expect(screen.getByText('x blocked')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Remove rule for a' }))
    expect(onRemove).toHaveBeenCalledWith({ family: 'product', name: 'a' })
    await user.click(screen.getByRole('button', { name: 'Remove rule for x' }))
    expect(onRemove).toHaveBeenCalledWith({ family: 'product', name: 'x' })
  })

  it('shows no Rules heading without rules', async () => {
    renderSection()
    await screen.findByText('12,000')
    expect(screen.queryByText('Rules')).not.toBeInTheDocument()
  })

  it('says an empty range has none', async () => {
    vi.spyOn(endpoints, 'eventNames').mockResolvedValue({ events: [] })
    renderSection()
    expect(await screen.findByText('No product events or measures in this range.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('says the load failed with a Retry', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'eventNames').mockRejectedValue(new ApiError(500, 'boom'))
    renderSection()
    expect(await screen.findByText(/Couldn't load event names/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })
})
