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
  const onMerge = vi.fn().mockResolvedValue(true)
  const onBlock = vi.fn().mockResolvedValue(true)
  const onRemove = vi.fn().mockResolvedValue(true)
  const onAddAlias = vi.fn().mockResolvedValue(true)
  renderWithProviders(<EventsSection project={project} range={range} onRename={onRename} onMerge={onMerge} onBlock={onBlock} onRemove={onRemove} onAddAlias={onAddAlias} {...props} />)
  return { onRename, onMerge, onBlock, onRemove, onAddAlias }
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

  it('lists the names, prefixed with their family, with their counts, by family and then name', async () => {
    renderSection()
    const section = screen.getByRole('region', { name: 'Events' })
    expect(await within(section).findByText('12,000')).toBeInTheDocument()
    expect(endpoints.eventNames).toHaveBeenCalledWith(1, { from: '2026-09-05', to: '2026-10-04' })
    const rows = within(section).getAllByRole('row').slice(1)
    expect(rows.map((r) => within(r).getAllByRole('cell')[0].textContent)).toEqual([
      'product/checkout', 'product/old_signup', 'product/signup', 'measures/checkout',
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
      'product/checkout', 'product/old_signup', 'product/signup', 'product/x', 'measures/checkout',
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
    const checkout = rows.find((r) => r.textContent?.startsWith('product/checkout'))!
    expect(within(checkout).getByText('also from')).toBeInTheDocument()
    expect(within(checkout).getByRole('button', { name: 'Remove alias a' })).toBeInTheDocument()
    expect(within(checkout).getByRole('button', { name: 'Remove alias old' })).toBeInTheDocument()
    // The measure of the same name has none: aliases are per family.
    const measure = rows.find((r) => r.textContent?.startsWith('measures/checkout'))!
    expect(within(measure).queryByText('also from')).not.toBeInTheDocument()
    // A target with nothing in the range still shows, with its alias.
    const ghost = rows.find((r) => r.querySelector('code')?.textContent === 'product/ghost')!
    expect(within(ghost).getByText('0')).toBeInTheDocument()
    expect(within(ghost).getByRole('button', { name: 'Remove alias z' })).toBeInTheDocument()
    await user.click(within(checkout).getByRole('button', { name: 'Remove alias old' }))
    expect(onRemove).not.toHaveBeenCalled()
    const confirm = await screen.findByRole('alertdialog', { name: 'Remove alias old?' })
    expect(within(confirm).getByText(/Events sent as old arrive under their own name again/)).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: 'Remove alias' }))
    expect(onRemove).toHaveBeenCalledWith({ family: 'product', name: 'old' })
  })

  it('keeps the alias when its removal is cancelled', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'a', action: 'rename', to: 'signup' }] })
    const { onRemove } = renderSection()
    await user.click(await screen.findByRole('button', { name: 'Remove alias a' }))
    await user.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(onRemove).not.toHaveBeenCalled()
  })

  it('adds another alias from the plus beside the existing ones', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'a', action: 'rename', to: 'signup' }] })
    const { onAddAlias } = renderSection()
    const section = screen.getByRole('region', { name: 'Events' })
    const plus = await within(section).findByRole('button', { name: 'Add alias to signup' })
    // Only rows with aliases have the plus; the menu adds the first one.
    expect(within(section).getAllByRole('button', { name: /^Add alias to / })).toHaveLength(1)
    await user.click(plus)
    const dialog = await screen.findByRole('dialog', { name: 'Add an alias to signup' })
    await user.type(within(dialog).getByLabelText('Old name'), 'b')
    await user.click(within(dialog).getByRole('button', { name: 'Add alias' }))
    expect(onAddAlias).toHaveBeenCalledWith({ family: 'product', alias: 'b', to: 'signup' })
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
    const row = (name: string) => within(section).getAllByRole('row').find((r) => r.querySelector('code')?.textContent === `product/${name}`)!
    expect(row('checkout')).toHaveClass('bg-sky-500/5')
    expect(row('signup')).not.toHaveClass('bg-sky-500/5')
    expect(row('signup')).toHaveClass('bg-destructive/5')
    expect(row('old_signup')).not.toHaveClass('bg-sky-500/5')
  })

  it('offers Rename, Merge into, Add alias and Block on an event', async () => {
    const user = userEvent.setup()
    renderSection()
    await user.click(await screen.findByRole('button', { name: 'Actions for signup (product)' }))
    const items = await screen.findAllByRole('menuitem')
    expect(items.map((i) => i.textContent)).toEqual(['Rename…', 'Merge into…', 'Add alias…', 'Block…'])
  })

  it('adds an alias to an event with none yet from its menu', async () => {
    const user = userEvent.setup()
    const { onAddAlias } = renderSection()
    await openMenu(user, 'signup', 'Add alias…')
    const dialog = await screen.findByRole('dialog', { name: 'Add an alias to signup' })
    await user.type(within(dialog).getByLabelText('Old name'), ' sign_up_v1 ')
    expect(within(dialog).getByText(/Events sent as sign_up_v1 are stored as signup\./)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Add alias' }))
    expect(onAddAlias).toHaveBeenCalledWith({ family: 'product', alias: 'sign_up_v1', to: 'signup' })
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('warns that an alias with events of its own merges them into the event', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'a', action: 'rename', to: 'signup' }] })
    const { onAddAlias } = renderSection()
    await user.click(await screen.findByRole('button', { name: 'Add alias to signup' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add an alias to signup' })
    await user.type(within(dialog).getByLabelText('Old name'), 'old_signup')
    expect(within(dialog).getByRole('alert')).toHaveTextContent('old_signup already has events: every report counts them under signup from now on.')
    await user.click(within(dialog).getByRole('button', { name: 'Add alias' }))
    expect(onAddAlias).toHaveBeenCalledWith({ family: 'product', alias: 'old_signup', to: 'signup' })
  })

  it('refuses an empty alias, a $ name, the event itself and an existing alias', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'a', action: 'rename', to: 'signup' }] })
    renderSection()
    await user.click(await screen.findByRole('button', { name: 'Add alias to signup' }))
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

  it('renames to a new name', async () => {
    const user = userEvent.setup()
    const { onRename } = renderSection()
    await openMenu(user, 'signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'register')
    expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Rename' }))
    expect(onRename).toHaveBeenCalledWith({ family: 'product', from: 'signup', to: 'register' })
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('refuses a name the family already has, pointing to Merge into', async () => {
    const user = userEvent.setup()
    const { onRename } = renderSection()
    await openMenu(user, 'old_signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'signup')
    expect(within(dialog).getByRole('alert')).toHaveTextContent('signup already has events: use Merge into… to fold old_signup into it.')
    expect(within(dialog).getByRole('button', { name: 'Rename' })).toBeDisabled()
    expect(onRename).not.toHaveBeenCalled()
  })

  it('treats a name that exists only in the other family as free', async () => {
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

  it('says a name the server refuses as taken has events, until it is typed over', async () => {
    const user = userEvent.setup()
    const { onRename } = renderSection()
    onRename.mockResolvedValueOnce('conflict')
    await openMenu(user, 'signup', 'Rename…')
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByRole('textbox')
    await user.type(input, 'older')
    await user.click(within(dialog).getByRole('button', { name: 'Rename' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('older already has events: use Merge into…')
    expect(within(dialog).getByRole('button', { name: 'Rename' })).toBeDisabled()
    await user.type(input, 'x')
    expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Rename' })).toBeEnabled()
  })

  it('refuses a name the family blocks', async () => {
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
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('x is blocked: nothing is renamed onto a blocked event. To stop receiving signup, block it.')
    expect(within(dialog).getByRole('button', { name: 'Rename' })).toBeDisabled()
    expect(onRename).not.toHaveBeenCalled()
  })

  it('merges into an event picked from the family, never itself or a blocked one', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'eventRules').mockResolvedValue({ rules: [{ family: 'product', name: 'x', action: 'block' }] })
    const { onMerge } = renderSection()
    await screen.findByText('Blocked')
    await openMenu(user, 'old_signup', 'Merge into…')
    const dialog = await screen.findByRole('dialog', { name: 'Merge old_signup into…' })
    expect(within(dialog).getByText(/Every report counts old_signup's events \(product\) under the event you pick, past ones included/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Removing the rule later shows past events as sent again/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Widgets whose SQL names old_signup are not changed\./)).toBeInTheDocument()
    expect(within(dialog).getAllByRole('option').map((o) => o.querySelector('span')?.textContent)).toEqual(['checkout', 'signup'])
    expect(within(dialog).getByRole('button', { name: 'Merge' })).toBeDisabled()
    await user.click(within(dialog).getByRole('option', { name: /signup/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Merge into signup' }))
    expect(onMerge).toHaveBeenCalledWith({ family: 'product', from: 'old_signup', to: 'signup' })
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('says when the family has no other event to merge into', async () => {
    const user = userEvent.setup()
    const { onMerge } = renderSection()
    await openMenu(user, 'checkout', 'Merge into…', 'measures')
    const dialog = await screen.findByRole('dialog', { name: 'Merge checkout into…' })
    expect(within(dialog).queryAllByRole('option')).toHaveLength(0)
    expect(within(dialog).getByText('No other measures events.')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Merge' })).toBeDisabled()
    expect(onMerge).not.toHaveBeenCalled()
  })

  it('keeps the merge dialog open when the merge is refused', async () => {
    const user = userEvent.setup()
    const { onMerge } = renderSection()
    onMerge.mockResolvedValueOnce(false)
    await openMenu(user, 'signup', 'Merge into…')
    const dialog = await screen.findByRole('dialog', { name: 'Merge signup into…' })
    await user.click(within(dialog).getByRole('option', { name: /old_signup/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Merge into old_signup' }))
    expect(onMerge).toHaveBeenCalledWith({ family: 'product', from: 'signup', to: 'old_signup' })
    expect(screen.getByRole('dialog', { name: 'Merge signup into…' })).toBeInTheDocument()
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

  it('confirms before blocking, saying past events are hidden at once', async () => {
    const user = userEvent.setup()
    const { onBlock } = renderSection()
    await openMenu(user, 'signup', 'Block…')
    expect(await screen.findByText('Block signup?')).toBeInTheDocument()
    expect(screen.getByText(/hidden from every report at once, past events included; new ones are not stored/)).toBeInTheDocument()
    expect(screen.queryByText(/daily pass|expire|retention/)).not.toBeInTheDocument()
    expect(onBlock).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Block event' }))
    expect(onBlock).toHaveBeenCalledWith({ family: 'product', name: 'signup' })
  })

  it('blocks nothing on Cancel', async () => {
    const user = userEvent.setup()
    const { onBlock } = renderSection()
    await openMenu(user, 'signup', 'Block…')
    await user.click(await screen.findByRole('button', { name: 'Cancel' }))
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
