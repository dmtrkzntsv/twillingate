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
  const onAddAlias = vi.fn().mockResolvedValue(true)
  renderWithProviders(<EventsSection project={project} range={range} rawDays={7} onRename={onRename} onBlock={onBlock} onRemove={onRemove} onAddAlias={onAddAlias} {...props} />)
  return { onRename, onBlock, onRemove, onAddAlias }
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

  it('lists the names with their counts and family, by family and then name', async () => {
    renderSection()
    const section = screen.getByRole('region', { name: 'Events' })
    expect(await within(section).findByText('12,000')).toBeInTheDocument()
    expect(endpoints.eventNames).toHaveBeenCalledWith(1, { from: '2026-09-05', to: '2026-10-04' })
    const rows = within(section).getAllByRole('row').slice(1)
    expect(rows.map((r) => within(r).getAllByRole('cell')[0].textContent)).toEqual([
      'checkoutproduct', 'old_signupproduct', 'signupproduct', 'checkoutmeasures',
    ])
    expect(within(rows[2]).getByText('340')).toBeInTheDocument()
    expect(within(section).queryByText('Blocked')).not.toBeInTheDocument()
  })

  it('marks a blocked event, and lists every blocked one in its place, even with nothing in the range', async () => {
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [
      { family: 'product', name: 'signup', action: 'block' }, { family: 'product', name: 'x', action: 'block' },
    ] })
    renderSection()
    const section = screen.getByRole('region', { name: 'Events' })
    await within(section).findAllByText('Blocked')
    const rows = within(section).getAllByRole('row').slice(1)
    expect(rows.map((r) => within(r).getAllByRole('cell')[0].querySelector('code')?.textContent)).toEqual([
      'checkout', 'old_signup', 'signup', 'x', 'checkout',
    ])
    for (const row of rows.slice(2, 4)) {
      expect(within(row).getByText('Blocked')).toBeInTheDocument()
      expect(within(row).getByText('no longer stored')).toBeInTheDocument()
      expect(row.querySelector('code')).toHaveClass('line-through')
    }
    expect(within(rows[2]).getByText('340')).toBeInTheDocument()
    expect(within(rows[3]).getByText('0')).toBeInTheDocument()
    expect(within(rows[0]).queryByText('Blocked')).not.toBeInTheDocument()
  })

  it('unblocks from a blocked row, which offers no Block', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'signup', action: 'block' }] })
    const { onRemove } = renderSection()
    await screen.findByText('Blocked')
    await user.click(screen.getByRole('button', { name: 'Actions for signup (product)' }))
    expect(await screen.findByRole('menuitem', { name: 'Rename…' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: 'Block…' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('menuitem', { name: 'Unblock' }))
    expect(onRemove).toHaveBeenCalledWith({ family: 'product', name: 'signup' })
  })

  it('shows the old names under the event they map to, each removable', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [
      { family: 'product', name: 'a', action: 'rename', to: 'checkout' },
      { family: 'product', name: 'old', action: 'rename', to: 'checkout' },
      { family: 'product', name: 'z', action: 'rename', to: 'ghost' },
    ] })
    const { onRemove } = renderSection()
    const section = screen.getByRole('region', { name: 'Events' })
    await within(section).findByRole('button', { name: 'Remove alias a' })
    const rows = within(section).getAllByRole('row').slice(1)
    const checkout = rows.find((r) => r.textContent?.startsWith('checkoutproduct'))!
    expect(within(checkout).getByText('also from')).toBeInTheDocument()
    expect(within(checkout).getByRole('button', { name: 'Remove alias a' })).toBeInTheDocument()
    expect(within(checkout).getByRole('button', { name: 'Remove alias old' })).toBeInTheDocument()
    // The measure of the same name has none: aliases are per family.
    const measure = rows.find((r) => r.textContent?.startsWith('checkoutmeasures'))!
    expect(within(measure).queryByText('also from')).not.toBeInTheDocument()
    // A target with nothing in the range still shows, with its alias.
    const ghost = rows.find((r) => r.querySelector('code')?.textContent === 'ghost')!
    expect(within(ghost).getByText('0')).toBeInTheDocument()
    expect(within(ghost).getByRole('button', { name: 'Remove alias z' })).toBeInTheDocument()
    await user.click(within(checkout).getByRole('button', { name: 'Remove alias old' }))
    expect(onRemove).toHaveBeenCalledWith({ family: 'product', name: 'old' })
  })

  it('tints a row with aliases, unless it is blocked', async () => {
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [
      { family: 'product', name: 'a', action: 'rename', to: 'checkout' },
      { family: 'product', name: 'b', action: 'rename', to: 'signup' },
      { family: 'product', name: 'signup', action: 'block' },
    ] })
    renderSection()
    const section = screen.getByRole('region', { name: 'Events' })
    await within(section).findByRole('button', { name: 'Remove alias a' })
    const row = (name: string) => within(section).getAllByRole('row').find((r) => r.textContent?.startsWith(`${name}product`))!
    expect(row('checkout')).toHaveClass('bg-sky-500/5')
    expect(row('signup')).not.toHaveClass('bg-sky-500/5')
    expect(row('signup')).toHaveClass('bg-destructive/5')
    expect(row('old_signup')).not.toHaveClass('bg-sky-500/5')
  })

  it('adds an alias to an event from its menu', async () => {
    const user = userEvent.setup()
    const { onAddAlias } = renderSection()
    await openMenu(user, 'signup', 'Add alias…')
    const dialog = await screen.findByRole('dialog', { name: 'Add an alias to signup' })
    await user.type(within(dialog).getByLabelText('Old name'), ' sign_up_v1 ')
    expect(within(dialog).getByText(/Events sent as sign_up_v1 are stored as signup\./)).toBeInTheDocument()
    expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Add alias' }))
    expect(onAddAlias).toHaveBeenCalledWith({ family: 'product', alias: 'sign_up_v1', to: 'signup' })
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('warns that an alias with events of its own merges them into the event', async () => {
    const user = userEvent.setup()
    renderSection()
    await openMenu(user, 'signup', 'Add alias…')
    const dialog = await screen.findByRole('dialog', { name: 'Add an alias to signup' })
    await user.type(within(dialog).getByLabelText('Old name'), 'old_signup')
    expect(within(dialog).getByRole('alert')).toHaveTextContent('old_signup already has events: they merge into signup, and this cannot be undone.')
  })

  it('refuses an empty alias, a $ name, the event itself and an existing alias', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'a', action: 'rename', to: 'signup' }] })
    renderSection()
    await screen.findByRole('button', { name: 'Remove alias a' })
    await openMenu(user, 'signup', 'Add alias…')
    const dialog = await screen.findByRole('dialog', { name: 'Add an alias to signup' })
    const input = within(dialog).getByLabelText('Old name')
    const button = within(dialog).getByRole('button', { name: 'Add alias' })
    expect(button).toBeDisabled()
    for (const bad of ['$x', 'signup', 'a']) {
      await user.clear(input)
      await user.type(input, bad)
      expect(button).toBeDisabled()
    }
    await user.clear(input)
    await user.type(input, 'b')
    expect(button).toBeEnabled()
  })

  it('has no separate rules list', async () => {
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules })
    renderSection()
    await screen.findByRole('button', { name: 'Remove alias a' })
    expect(screen.queryByText('Rules')).not.toBeInTheDocument()
    expect(screen.queryByText('a → b')).not.toBeInTheDocument()
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
