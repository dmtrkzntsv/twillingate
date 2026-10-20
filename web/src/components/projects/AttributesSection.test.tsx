import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, endpoints, type ProjectAttributes, type Project } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import AttributesSection from './AttributesSection'

const project = { project_id: 1, name: 'dev', allowed_origins: [], attributes: ['plan', 'never_sent'] } as Project
const range = { from: '2026-09-05', to: '2026-10-04' }
const answer: ProjectAttributes = {
  project_id: 1, from: '2026-09-05', to: '2026-10-04', keys_total: 4,
  attributes: [
    { key: 'order_id', events: 980, values: 412, received: true, blocked: false },
    { key: 'plan', events: 900, values: 3, received: true, blocked: false, aliases: ['planType'] },
    { key: 'tier', events: 20, values: 2, received: true, blocked: false },
    { key: '$host', events: 15, values: 1, received: true, blocked: false },
    { key: 'debug', events: 0, values: 0, received: false, blocked: true },
  ],
}

function renderSection(props: Partial<Parameters<typeof AttributesSection>[0]> = {}) {
  const onSaveBreakdowns = vi.fn().mockResolvedValue(true)
  const onRename = vi.fn().mockResolvedValue(true)
  const onMerge = vi.fn().mockResolvedValue(true)
  const onBlock = vi.fn().mockResolvedValue(true)
  const onRemove = vi.fn().mockResolvedValue(true)
  const onAddAlias = vi.fn().mockResolvedValue(true)
  renderWithProviders(
    <AttributesSection project={project} range={range} onSaveBreakdowns={onSaveBreakdowns}
      onRename={onRename} onMerge={onMerge} onBlock={onBlock} onRemove={onRemove} onAddAlias={onAddAlias} {...props} />,
  )
  return { onSaveBreakdowns, onRename, onMerge, onBlock, onRemove, onAddAlias }
}

const row = (key: string) => screen.getAllByRole('row').find((r) => r.querySelector('code')?.textContent === key)!

async function openMenu(user: ReturnType<typeof userEvent.setup>, key: string, item: string) {
  await user.click(await screen.findByRole('button', { name: `Actions for ${key}` }))
  await user.click(await screen.findByRole('menuitem', { name: item }))
}

describe('AttributesSection', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'listAttributes').mockResolvedValue(answer)
  })

  it('lists every key busiest first with what it received and its switch, then the breakdowns nothing carried', async () => {
    renderSection()
    const section = screen.getByRole('region', { name: 'Attributes' })
    await within(section).findByRole('switch', { name: 'Break down plan' })
    expect(endpoints.listAttributes).toHaveBeenCalledWith(1, { from: '2026-09-05', to: '2026-10-04' })
    const rows = within(section).getAllByRole('row').slice(1)
    expect(rows.map((r) => r.querySelector('code')?.textContent)).toEqual(['order_id', 'plan', 'tier', '$host', 'debug', 'never_sent'])
    // The Received text sits in its column and, folded, under the key for phones.
    expect(within(row('plan')).getAllByText('900 events · 3 values')).toHaveLength(2)
    expect(within(row('order_id')).getAllByText('980 events · 412 values')).toHaveLength(2)
    expect(within(row('never_sent')).getAllByText('not received')).toHaveLength(2)
    expect(within(section).getByRole('switch', { name: 'Break down plan' })).toBeChecked()
    expect(within(section).getByRole('switch', { name: 'Break down never_sent' })).toBeChecked()
    expect(within(section).getByRole('switch', { name: 'Break down order_id' })).not.toBeChecked()
    expect(within(section).getByRole('switch', { name: 'Break down $host' })).not.toBeChecked()
    expect(within(section).getByRole('heading')).toHaveTextContent(/^Attributes$/)
  })

  it('switching a key on saves the breakdowns with it added', async () => {
    const user = userEvent.setup()
    const { onSaveBreakdowns } = renderSection()
    await user.click(await screen.findByRole('switch', { name: 'Break down tier' }))
    expect(onSaveBreakdowns).toHaveBeenCalledWith(['plan', 'never_sent', 'tier'])
  })

  it('switches a $ key on too', async () => {
    const user = userEvent.setup()
    const { onSaveBreakdowns } = renderSection()
    await user.click(await screen.findByRole('switch', { name: 'Break down $host' }))
    expect(onSaveBreakdowns).toHaveBeenCalledWith(['plan', 'never_sent', '$host'])
  })

  it('reads the switches from the project\'s breakdowns', async () => {
    const user = userEvent.setup()
    const { onSaveBreakdowns } = renderSection({ project: { ...project, attributes: ['tier'] } })
    expect(await screen.findByRole('switch', { name: 'Break down tier' })).toBeChecked()
    expect(screen.getByRole('switch', { name: 'Break down plan' })).not.toBeChecked()
    expect(screen.queryByRole('switch', { name: 'Break down never_sent' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('switch', { name: 'Break down plan' }))
    expect(onSaveBreakdowns).toHaveBeenCalledWith(['tier', 'plan'])
  })

  it('asks before switching a key off, then saves the list without it', async () => {
    const user = userEvent.setup()
    const { onSaveBreakdowns } = renderSection()
    await user.click(await screen.findByRole('switch', { name: 'Break down plan' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Stop breaking down plan?' })
    expect(within(confirm).getByText(/adding it back later won't fill the gap/)).toBeInTheDocument()
    expect(onSaveBreakdowns).not.toHaveBeenCalled()
    await user.click(within(confirm).getByRole('button', { name: 'Remove breakdown' }))
    expect(onSaveBreakdowns).toHaveBeenCalledWith(['never_sent'])
  })

  it('keeps the breakdown on Cancel, and never titles the confirm with a missing key', async () => {
    const user = userEvent.setup()
    const { onSaveBreakdowns } = renderSection()
    await user.click(await screen.findByRole('switch', { name: 'Break down plan' }))
    await user.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(onSaveBreakdowns).not.toHaveBeenCalled()
    expect(screen.queryByText('Stop breaking down undefined?')).not.toBeInTheDocument()
  })

  it('disables a blocked key', async () => {
    renderSection()
    expect(await screen.findByRole('switch', { name: 'Break down debug' })).toBeDisabled()
    expect(screen.getByRole('switch', { name: 'Break down tier' })).toBeEnabled()
  })

  it('disables every switch while a write is pending', async () => {
    renderSection({ pending: true })
    expect(await screen.findByRole('switch', { name: 'Break down tier' })).toBeDisabled()
    expect(screen.getByRole('switch', { name: 'Break down plan' })).toBeDisabled()
  })

  it('marks a blocked key as Events marks a blocked event', async () => {
    renderSection()
    await screen.findByText('Blocked')
    expect(within(row('debug')).getByText('Blocked')).toBeInTheDocument()
    expect(row('debug').querySelector('code')).toHaveClass('line-through')
    expect(row('debug')).toHaveClass('bg-destructive/5')
    expect(within(row('debug')).getByText('no longer stored')).toBeInTheDocument()
    expect(within(row('plan')).queryByText('Blocked')).not.toBeInTheDocument()
  })

  it('gives a $ key its switch and no menu', async () => {
    renderSection()
    await screen.findByRole('switch', { name: 'Break down $host' })
    expect(screen.queryByRole('button', { name: 'Actions for $host' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Actions for plan' })).toBeInTheDocument()
  })

  it('offers Rename, Merge into, Add alias and Block on a key, and Unblock on a blocked one', async () => {
    const user = userEvent.setup()
    const { onRemove } = renderSection()
    await user.click(await screen.findByRole('button', { name: 'Actions for tier' }))
    expect((await screen.findAllByRole('menuitem')).map((i) => i.textContent)).toEqual(['Rename…', 'Merge into…', 'Add alias…', 'Block…'])
    await user.keyboard('{Escape}')
    await openMenu(user, 'debug', 'Unblock')
    expect(onRemove).toHaveBeenCalledWith({ key: 'debug' })
  })

  it('renames a key, with attribute wording, refusing one with data or blocked', async () => {
    const user = userEvent.setup()
    const { onRename } = renderSection()
    await openMenu(user, 'tier', 'Rename…')
    const dialog = await screen.findByRole('dialog', { name: 'Rename tier' })
    expect(within(dialog).getByText(/Events carrying tier store it under the new name/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Widgets whose SQL names the old attribute are not changed\./)).toBeInTheDocument()
    const input = within(dialog).getByRole('textbox')
    await user.type(input, 'plan')
    expect(within(dialog).getByRole('alert')).toHaveTextContent('plan already has events: use Merge into… to fold tier into it.')
    await user.clear(input)
    await user.type(input, 'debug')
    expect(within(dialog).getByRole('alert')).toHaveTextContent('debug is blocked: nothing is renamed onto a blocked attribute. To stop receiving tier, block it.')
    expect(within(dialog).getByRole('button', { name: 'Rename' })).toBeDisabled()
    await user.clear(input)
    await user.type(input, 'level')
    await user.click(within(dialog).getByRole('button', { name: 'Rename' }))
    expect(onRename).toHaveBeenCalledWith({ from: 'tier', to: 'level' })
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('merges into another key, never itself, a blocked one or a $ key', async () => {
    const user = userEvent.setup()
    const { onMerge } = renderSection()
    await openMenu(user, 'tier', 'Merge into…')
    const dialog = await screen.findByRole('dialog', { name: 'Merge tier into…' })
    expect(within(dialog).getByText(/stored values move to the attribute you pick/)).toBeInTheDocument()
    expect(within(dialog).getByText(/cannot be undone/)).toBeInTheDocument()
    expect(within(dialog).getByRole('combobox', { name: 'Search attributes' })).toBeInTheDocument()
    expect(within(dialog).getAllByRole('option').map((o) => o.querySelector('span')?.textContent)).toEqual(['order_id', 'plan', 'never_sent'])
    await user.click(within(dialog).getByRole('option', { name: /plan/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Merge into plan' }))
    expect(onMerge).toHaveBeenCalledWith({ from: 'tier', to: 'plan' })
  })

  it('adds an alias from the menu, with attribute wording, warning when it has data', async () => {
    const user = userEvent.setup()
    const { onAddAlias } = renderSection()
    await openMenu(user, 'plan', 'Add alias…')
    const dialog = await screen.findByRole('dialog', { name: 'Add an alias to plan' })
    expect(within(dialog).getByText('Events carrying the alias store it as plan.')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('Old name'), 'tier')
    expect(within(dialog).getByText('Events carrying tier store it as plan.')).toBeInTheDocument()
    expect(within(dialog).getByRole('alert')).toHaveTextContent('tier already has events: they merge into plan, and this cannot be undone.')
    await user.clear(within(dialog).getByLabelText('Old name'))
    await user.type(within(dialog).getByLabelText('Old name'), 'planType')
    expect(within(dialog).getByRole('button', { name: 'Add alias' })).toBeDisabled()
    await user.clear(within(dialog).getByLabelText('Old name'))
    await user.type(within(dialog).getByLabelText('Old name'), 'plan_v1')
    await user.click(within(dialog).getByRole('button', { name: 'Add alias' }))
    expect(onAddAlias).toHaveBeenCalledWith({ alias: 'plan_v1', to: 'plan' })
  })

  it('shows alias chips under the key, removes one after confirming, and adds another from the plus', async () => {
    const user = userEvent.setup()
    const { onRemove, onAddAlias } = renderSection()
    await screen.findByRole('button', { name: 'Remove alias planType' })
    expect(within(row('plan')).getByText('also from')).toBeInTheDocument()
    expect(row('plan')).toHaveClass('bg-sky-500/5')
    expect(screen.getAllByRole('button', { name: /^Add alias to / })).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Remove alias planType' }))
    expect(onRemove).not.toHaveBeenCalled()
    const confirm = await screen.findByRole('alertdialog', { name: 'Remove alias planType?' })
    expect(within(confirm).getByText(/Events carrying planType keep it under its own name again; the ones already renamed stay under plan\./)).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: 'Remove alias' }))
    expect(onRemove).toHaveBeenCalledWith({ key: 'planType' })
    await user.click(screen.getByRole('button', { name: 'Add alias to plan' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add an alias to plan' })
    await user.type(within(dialog).getByLabelText('Old name'), 'p')
    await user.click(within(dialog).getByRole('button', { name: 'Add alias' }))
    expect(onAddAlias).toHaveBeenCalledWith({ alias: 'p', to: 'plan' })
  })

  it('confirms before blocking, saying past events are hidden at once', async () => {
    const user = userEvent.setup()
    const { onBlock } = renderSection()
    await openMenu(user, 'tier', 'Block…')
    const confirm = await screen.findByRole('alertdialog', { name: 'Block tier?' })
    expect(within(confirm).getByText(/tier is hidden from every report at once, past events included; new ones are not stored/)).toBeInTheDocument()
    expect(within(confirm).queryByText(/daily pass|expire|retention/)).not.toBeInTheDocument()
    expect(onBlock).not.toHaveBeenCalled()
    await user.click(within(confirm).getByRole('button', { name: 'Block attribute' }))
    expect(onBlock).toHaveBeenCalledWith({ key: 'tier' })
  })

  it('blocks nothing on Cancel', async () => {
    const user = userEvent.setup()
    const { onBlock } = renderSection()
    await openMenu(user, 'tier', 'Block…')
    await user.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(onBlock).not.toHaveBeenCalled()
  })

  it('says an empty range has none', async () => {
    vi.spyOn(endpoints, 'listAttributes').mockResolvedValue({ ...answer, attributes: [], keys_total: 0 })
    renderSection({ project: { ...project, attributes: [] } })
    expect(await screen.findByText('No attributes received in this range.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('says when the server listed only the busiest keys', async () => {
    vi.spyOn(endpoints, 'listAttributes').mockResolvedValue({ ...answer, keys_total: 1234 })
    renderSection()
    expect(await screen.findByText('Showing the 4 busiest of 1,234 keys.')).toBeInTheDocument()
  })

  it('says the load failed with a Retry', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'listAttributes').mockRejectedValue(new ApiError(500, 'boom'))
    renderSection()
    expect(await screen.findByText(/Couldn't load attributes/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })
})
