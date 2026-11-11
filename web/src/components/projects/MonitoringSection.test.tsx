import { beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type Monitor, type Monitors } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import MonitoringSection from './MonitoringSection'

const days = (n: number) => new Date(Date.now() + n * 86_400_000 + 3_600_000)

/** The `YYYY-MM-DD` n days after today in UTC, whatever the hour. */
function utcDay(n: number): string {
  const now = new Date()
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + n)).toISOString().slice(0, 10)
}

const A = 'https://a.example'
const B = 'https://b.example'
const C = 'https://c.example'
const SENTENCE = 'Check that the site is up, and when its certificate and domain expire.'
const ITEM = 'Look for text on the page'

/** Opens the row's text field the way a person does: the menu, then its item. */
async function openText(user: ReturnType<typeof userEvent.setup>, origin: string) {
  await user.click(screen.getByRole('button', { name: `More for ${origin}` }))
  await user.click(await screen.findByRole('menuitem', { name: ITEM }))
}

function monitor(origin: string, over: Partial<Monitor> = {}): Monitor {
  return {
    origin,
    enabled: false,
    check_every_seconds: 0,
    contains: '',
    redirect_to: '',
    redirects: 0,
    cert: { expires_at: '', issuer: '', valid: false, error: '', checked_at: '' },
    last: { checked_at: '', up: false, status: 0, reason: '', error: '', ms: 0 },
    check: { requested_at: '', finished_at: '' },
    ...over,
  }
}
const on = (origin: string, over: Partial<Monitor> = {}) => monitor(origin, { enabled: true, check_every_seconds: 120, ...over })

function list(monitors: Monitor[], over: Partial<Monitors> = {}): Monitors {
  return { interval_seconds: 60, timeout_seconds: 5, paused: false, monitors, domains: [], ...over }
}

function show(data: Monitors, onSave = vi.fn().mockResolvedValue(true), onCheck = vi.fn().mockResolvedValue(true), pending = false) {
  const read = vi.spyOn(endpoints, 'monitors').mockResolvedValue(data)
  vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [{ dashboard_id: 8, title: 'Availability', owner: 'system', group_id: 0 }] })
  const { client } = renderWithProviders(
    <MemoryRouter>
      <MonitoringSection projectId={4} pending={pending} onSave={onSave} onCheck={onCheck} />
    </MemoryRouter>
  )
  return { read, onSave, onCheck, client }
}

/** The row of an origin, once it is drawn. */
const row = async (origin: string) => within(await screen.findByRole('listitem', { name: origin }))

beforeEach(() => vi.restoreAllMocks())

describe('MonitoringSection', () => {
  it('nothing on: the sentence and a switch per origin, nothing else', async () => {
    show(list([monitor(A), monitor(B)]))
    expect(await screen.findByRole('switch', { name: `Monitor ${A}` })).not.toBeChecked()
    expect(screen.getByRole('switch', { name: `Monitor ${B}` })).not.toBeChecked()
    expect(screen.getByText(SENTENCE)).toBeInTheDocument()
    expect(screen.getByText(A)).toBeInTheDocument()
    expect(screen.queryByText(/each site is checked/)).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toEqual([])
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
  })

  it('names the empty state, with no rows', async () => {
    show(list([]))
    expect(await screen.findByText(/first add its address to Allowed origins above/)).toBeInTheDocument()
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
  })

  it('shows one row per origin: on rows with what was found, off rows with the origin and switch alone', async () => {
    const data = list(
      [
        on(A, { cert: { expires_at: days(54).toISOString(), issuer: 'R3', valid: true, error: '', checked_at: 'x' } }),
        on(B, { cert: { expires_at: days(12).toISOString(), issuer: 'R3', valid: true, error: '', checked_at: 'x' } }),
        monitor(C),
      ],
      { domains: [{ domain: 'a.example', origins: [A, B], expires_at: utcDay(285), checked_at: 'x' }] },
    )
    show(data)
    const a = await row(A)
    expect(a.getByText('Cert 54 days')).toBeInTheDocument()
    expect(a.getByText('Cert 54 days')).toHaveAttribute('title', expect.stringMatching(/^expires \S+ \(54 days\)$/))
    expect(a.getByRole('button', { name: `Check ${A} now` })).toBeEnabled()
    expect(a.getByRole('button', { name: `More for ${A}` })).toBeEnabled()
    expect(a.getByRole('switch')).toBeChecked()
    expect((await row(B)).getByText('Cert 12 days')).toBeInTheDocument()
    const c = await row(C)
    expect(c.getByRole('switch')).not.toBeChecked()
    expect(c.queryAllByRole('button')).toEqual([])
    expect(c.queryByText(/cert/i)).not.toBeInTheDocument()
    // The header is the link alone; the domains are another section's.
    expect(screen.queryByText(SENTENCE)).not.toBeInTheDocument()
    expect(screen.queryByText(/^Domain:/)).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open Availability' })).toHaveAttribute('href', '/projects/4/dashboards/8')
  })

  it('shows how often each monitor on is really checked, and nothing for one off or while paused', async () => {
    show(list([on(A, { check_every_seconds: 600 }), on(B, { check_every_seconds: 600 }), monitor(C)]))
    expect((await row(A)).getByText(', checked every 10 min')).toBeInTheDocument()
    expect((await row(B)).getByText(', checked every 10 min')).toBeInTheDocument()
    expect((await row(C)).queryByText(/checked every/)).not.toBeInTheDocument()
    cleanup()
    show(list([on(A, { check_every_seconds: 600 })], { paused: true }))
    expect((await row(A)).queryByText(/checked every/)).not.toBeInTheDocument()
  })

  it('lists the rows in the order given, off rows muted and bare, on rows with their switch first', async () => {
    show(list([monitor(C), on(A), on(B)]))
    await row(A)
    const items = screen.getAllByRole('listitem').map((li) => li.getAttribute('aria-label'))
    expect(items).toEqual([C, A, B])
    const li = screen.getByRole('listitem', { name: C })
    expect(li.firstElementChild).toHaveAttribute('role', 'switch')
    expect(within(li).getByText(C)).toHaveClass('text-muted-foreground')
  })

  describe('the switch', () => {
    it('saves at once, with no Save button, and shows it at once', async () => {
      const user = userEvent.setup()
      let done!: (saved: boolean) => void
      const onSave = vi.fn().mockReturnValue(new Promise((resolve) => { done = resolve }))
      const { read } = show(list([monitor(A), monitor(B)]), onSave)
      const sw = await screen.findByRole('switch', { name: `Monitor ${B}` })
      expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.click(sw)
      expect(onSave).toHaveBeenCalledWith({ origin: B, enabled: true })
      expect(sw).toBeChecked()
      expect(screen.getByRole('switch', { name: `Monitor ${A}` })).not.toBeChecked()
      // What the save's refetch brings stays, once it resolves.
      read.mockResolvedValue(list([monitor(A), on(B)]))
      done(true)
      await waitFor(() => expect(sw).toBeChecked())
    })

    it('goes back when the save fails', async () => {
      const user = userEvent.setup()
      const onSave = vi.fn().mockResolvedValue(false) // the toast is the action's
      show(list([monitor(A)]), onSave)
      const sw = await screen.findByRole('switch', { name: `Monitor ${A}` })
      await user.click(sw)
      await waitFor(() => expect(onSave).toHaveBeenCalled())
      await waitFor(() => expect(sw).not.toBeChecked())
    })

    it('turns off a row at once, which goes back to the origin alone once saved', async () => {
      const user = userEvent.setup()
      const { read, onSave, client } = show(list([on(A)]))
      const a = await row(A)
      read.mockResolvedValue(list([monitor(A)]))
      await user.click(a.getByRole('switch'))
      expect(onSave).toHaveBeenCalledWith({ origin: A, enabled: false })
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(a.queryByRole('button', { name: `Check ${A} now` })).not.toBeInTheDocument())
      expect(await screen.findByText(SENTENCE)).toBeInTheDocument()
      expect(screen.queryByRole('link', { name: 'Open Availability' })).not.toBeInTheDocument()
    })

    it('locks while anything is saving', async () => {
      show(list([on(A, { contains: 'x' })]), undefined, undefined, true)
      const a = await row(A)
      expect(a.getByRole('switch')).toBeDisabled()
      expect(a.getByRole('button', { name: `Check ${A} now` })).toBeDisabled()
      expect(a.getByRole('button', { name: `More for ${A}` })).toBeDisabled()
      expect(a.getByRole('button', { name: 'must contain "x"' })).toBeDisabled()
    })
  })

  describe('the install', () => {
    it('paused: shows the rows as they are, disables Check now, and says so once', async () => {
      show(list([on(A), monitor(B)], { paused: true }))
      const a = await row(A)
      expect(screen.getByText('Monitoring is paused on this install')).toBeInTheDocument()
      expect(a.getByRole('switch')).toBeEnabled()
      expect(a.getByRole('button', { name: `Check ${A} now` })).toBeDisabled()
      expect((await row(B)).getByRole('switch')).not.toBeChecked()
    })

    it('not paused: no such line', async () => {
      show(list([on(A)]))
      await row(A)
      expect(screen.queryByText(/paused/)).not.toBeInTheDocument()
    })
  })

  describe('a failed refetch', () => {
    it('with the monitors cached keeps the rows and an unsaved text, and says so inline', async () => {
      const user = userEvent.setup()
      const { read, client } = show(list([on(A)]))
      const a = await row(A)
      await openText(user, A)
      await user.type(a.getByLabelText('Page must contain'), 'Welcome')
      read.mockRejectedValue(new Error('server restarting'))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      expect(await screen.findByText(/Couldn't refresh the monitors/)).toBeInTheDocument()
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      expect(screen.getByRole('listitem', { name: A })).toBeInTheDocument()
      expect(a.getByLabelText('Page must contain')).toHaveValue('Welcome')
    })

    it('with nothing cached shows only the error and Retry', async () => {
      vi.spyOn(endpoints, 'monitors').mockRejectedValue(new Error('down'))
      vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
      renderWithProviders(
        <MemoryRouter>
          <MonitoringSection projectId={4} pending={false} onSave={vi.fn()} onCheck={vi.fn()} />
        </MemoryRouter>
      )
      expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the monitors")
      expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    })
  })

  describe('the text', () => {
    it('hides behind the row\'s menu, which reveals and focuses that row\'s field only, and saves what was typed', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A), on(B)]))
      const a = await row(A)
      const b = await row(B)
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
      expect(b.queryByRole('menuitem')).not.toBeInTheDocument()
      await openText(user, B)
      const input = b.getByLabelText('Page must contain')
      expect(input).toHaveFocus()
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
      // Opening it is no edit; typing is.
      expect(b.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.type(input, ' Welcome ')
      await user.click(b.getByRole('button', { name: 'Save' }))
      expect(onSave).toHaveBeenCalledWith({ origin: B, contains: 'Welcome' })
    })

    it('returns focus to the menu button on Escape, and to the field only when the item opened it', async () => {
      const user = userEvent.setup()
      show(list([on(A)]))
      const more = await screen.findByRole('button', { name: `More for ${A}` })
      await user.click(more)
      await screen.findByRole('menuitem', { name: ITEM })
      await user.keyboard('{Escape}')
      await waitFor(() => expect(more).toHaveFocus())
      await openText(user, A)
      expect((await row(A)).getByLabelText('Page must contain')).toHaveFocus()
    })

    it('shows a saved text under the origin, which opens the field when clicked', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A, { contains: 'Welcome' })]))
      const a = await row(A)
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
      await user.click(a.getByRole('button', { name: 'must contain "Welcome"' }))
      const input = a.getByLabelText('Page must contain')
      expect(input).toHaveValue('Welcome')
      expect(a.queryByRole('button', { name: 'must contain "Welcome"' })).not.toBeInTheDocument()
      expect(a.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.type(input, '{Enter}')
      expect(onSave).not.toHaveBeenCalled()
    })

    it('shows no saved text on an off row', async () => {
      show(list([monitor(A, { contains: 'Welcome' })]))
      const a = await row(A)
      expect(a.queryByText(/must contain/)).not.toBeInTheDocument()
    })

    it('keeps the field while a saved text is cleared, and removes the text once saved', async () => {
      const user = userEvent.setup()
      const { read, onSave, client } = show(list([on(A, { contains: 'Welcome' })]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: 'must contain "Welcome"' }))
      await user.clear(a.getByLabelText('Page must contain'))
      expect(a.getByLabelText('Page must contain')).toHaveValue('')
      await user.click(a.getByRole('button', { name: 'Save' }))
      expect(onSave).toHaveBeenCalledWith({ origin: A, contains: '' })
      read.mockResolvedValue(list([on(A)]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument())
      expect(a.queryByText(/must contain/)).not.toBeInTheDocument()
    })

    it('keeps an edit through a refetch, in its row, and takes what is saved once saved', async () => {
      const user = userEvent.setup()
      const onSave = vi.fn().mockResolvedValue(false)
      const { read, client } = show(list([on(A, { contains: 'first' }), on(B, { contains: 'other' })]), onSave)
      const a = await row(A)
      const b = await row(B)
      await user.click(a.getByRole('button', { name: 'must contain "first"' }))
      await user.click(b.getByRole('button', { name: 'must contain "other"' }))
      const input = a.getByLabelText('Page must contain')
      expect(input).toHaveValue('first')

      // Untouched, it follows the server.
      read.mockResolvedValue(list([on(A, { contains: 'second' }), on(B, { contains: 'other' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(input).toHaveValue('second'))

      // Edited, a refetch (window focus, another row's save) leaves it, and its
      // neighbour still follows the server.
      await user.type(input, ' edited')
      read.mockResolvedValue(list([on(A, { contains: 'third' }), on(B, { contains: 'other two' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(b.getByLabelText('Page must contain')).toHaveValue('other two'))
      expect(input).toHaveValue('second edited')

      // A refused save keeps the edit too.
      await user.click(a.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
      read.mockResolvedValue(list([on(A, { contains: 'fourth' }), on(B, { contains: 'other two' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      expect(input).toHaveValue('second edited')

      // A save that went through hands the field back to the server.
      onSave.mockResolvedValue(true)
      await user.click(a.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2))
      read.mockResolvedValue(list([on(A, { contains: 'saved' }), on(B, { contains: 'other two' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      // The field closes, and the saved text shows under the origin.
      expect(await a.findByRole('button', { name: 'must contain "saved"' })).toBeInTheDocument()
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
    })
  })

  describe('closing the text field', () => {
    it('Cancel puts the saved text back, closes the field and focuses the saved text', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A, { contains: 'Welcome' })]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: 'must contain "Welcome"' }))
      await user.type(a.getByLabelText('Page must contain'), ' more')
      expect(a.getByRole('button', { name: 'Save' })).toBeInTheDocument()
      await user.click(a.getByRole('button', { name: 'Cancel' }))
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
      const saved = a.getByRole('button', { name: 'must contain "Welcome"' })
      expect(saved).toHaveFocus()
      expect(onSave).not.toHaveBeenCalled()
      // Opened again, it starts from the saved text.
      await user.click(saved)
      expect(a.getByLabelText('Page must contain')).toHaveValue('Welcome')
    })

    it('with nothing saved, only Cancel shows on an empty field, and it returns focus to the menu button', async () => {
      const user = userEvent.setup()
      show(list([on(A)]))
      const a = await row(A)
      await openText(user, A)
      expect(a.getByLabelText('Page must contain')).toHaveFocus()
      expect(a.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.type(a.getByLabelText('Page must contain'), 'x')
      expect(a.getByRole('button', { name: 'Save' })).toBeInTheDocument()
      await user.clear(a.getByLabelText('Page must contain'))
      expect(a.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.click(a.getByRole('button', { name: 'Cancel' }))
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
      expect(a.getByRole('button', { name: `More for ${A}` })).toHaveFocus()
    })

    it('Escape in the input does the same as Cancel', async () => {
      const user = userEvent.setup()
      show(list([on(A)]))
      const a = await row(A)
      await openText(user, A)
      await user.type(a.getByLabelText('Page must contain'), 'draft')
      await user.keyboard('{Escape}')
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
      expect(a.getByRole('button', { name: `More for ${A}` })).toHaveFocus()
      await openText(user, A)
      expect(a.getByLabelText('Page must contain')).toHaveValue('')
    })

    it('an edit undone to the saved text follows the server again', async () => {
      const user = userEvent.setup()
      const { read, client } = show(list([on(A, { contains: 'first' })]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: 'must contain "first"' }))
      const input = a.getByLabelText('Page must contain')
      await user.type(input, '!')
      await user.type(input, '{Backspace}')
      expect(input).toHaveValue('first')
      read.mockResolvedValue(list([on(A, { contains: 'second' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(input).toHaveValue('second'))
    })
  })

  describe('the redirect', () => {
    const REDIRECT = 'Expect a redirect'
    const FIELD = 'Must redirect to'

    it('offers the other origins behind the menu, focuses the choice, and saves it', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A), on(B), monitor(C)]))
      const a = await row(A)
      expect(a.queryByLabelText(FIELD)).not.toBeInTheDocument()
      await user.click(screen.getByRole('button', { name: `More for ${A}` }))
      await user.click(await screen.findByRole('menuitem', { name: REDIRECT }))
      const select = a.getByLabelText(FIELD)
      await waitFor(() => expect(select).toHaveFocus())
      // A prompt, then the other origins, on or off; never the row's own.
      expect(within(select).getAllByRole('option').map((o) => o.getAttribute('value'))).toEqual(['', B, C])
      expect(select).toHaveValue('')
      expect(a.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
      await user.selectOptions(select, B)
      await user.click(a.getByRole('button', { name: 'Save' }))
      expect(onSave).toHaveBeenCalledWith({ origin: A, redirect_to: B })
    })

    it('shows a saved redirect under the origin, which opens the field with the origins alone', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A, { redirect_to: B }), on(B), on(C)]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: `must redirect to ${B}` }))
      const select = a.getByLabelText(FIELD)
      expect(select).toHaveValue(B)
      expect(within(select).getAllByRole('option').map((o) => o.getAttribute('value'))).toEqual([B, C])
      await user.selectOptions(select, C)
      await user.click(a.getByRole('button', { name: 'Save' }))
      expect(onSave).toHaveBeenCalledWith({ origin: A, redirect_to: C })
    })

    it('the X removes a saved redirect at once, and focus goes to the row menu', async () => {
      const user = userEvent.setup()
      const { onSave, read, client } = show(list([on(A, { redirect_to: B }), on(B)]))
      const a = await row(A)
            read.mockResolvedValue(list([on(A), on(B)]))
      await user.click(a.getByRole('button', { name: 'Remove the redirect' }))
      expect(onSave).toHaveBeenCalledWith({ origin: A, redirect_to: '' })
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(a.queryByText(/must redirect/)).not.toBeInTheDocument())
      expect(a.getByRole('button', { name: `More for ${A}` })).toHaveFocus()
      expect(a.queryByLabelText(FIELD)).not.toBeInTheDocument()
    })

    it('Cancel and Escape close the field without saving, focusing what opened it', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A, { redirect_to: B }), on(B)]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: `must redirect to ${B}` }))
      await user.click(a.getByRole('button', { name: 'Cancel' }))
      expect(a.queryByLabelText(FIELD)).not.toBeInTheDocument()
      const saved = a.getByRole('button', { name: `must redirect to ${B}` })
      expect(saved).toHaveFocus()
      await user.click(saved)
      expect(a.getByLabelText(FIELD)).toHaveValue(B)
      await user.keyboard('{Escape}')
      expect(a.queryByLabelText(FIELD)).not.toBeInTheDocument()
      expect(onSave).not.toHaveBeenCalled()
    })

    it('keeps offering a saved redirect to an origin the project no longer has', async () => {
      const user = userEvent.setup()
      show(list([on(A, { redirect_to: C })]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: `must redirect to ${C}` }))
      expect(a.getByLabelText(FIELD)).toHaveValue(C)
    })

    it('is not offered with no other origin, and not shown on an off row', async () => {
      const user = userEvent.setup()
      show(list([on(A), monitor(B, { redirect_to: A })]))
      // B, off, shows nothing; it is A's other origin, so A has the item.
      expect((await row(B)).queryByText(/must redirect/)).not.toBeInTheDocument()
      await user.click(await screen.findByRole('button', { name: `More for ${A}` }))
      await screen.findByRole('menuitem', { name: ITEM })
      expect(screen.getByRole('menuitem', { name: REDIRECT })).toBeInTheDocument()
      cleanup()
      show(list([on(A)]))
      await user.click(await screen.findByRole('button', { name: `More for ${A}` }))
      await screen.findByRole('menuitem', { name: ITEM })
      expect(screen.queryByRole('menuitem', { name: REDIRECT })).not.toBeInTheDocument()
    })
  })

  describe('every expectation', () => {
    it('has an X that removes the saved text at once, as the redirect has', async () => {
      const user = userEvent.setup()
      const { onSave } = show(list([on(A, { contains: 'Welcome' })]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: 'Remove the page text' }))
      expect(onSave).toHaveBeenCalledWith({ origin: A, contains: '' })
      expect(a.getByRole('button', { name: `More for ${A}` })).toHaveFocus()
    })

    it('drops an unsaved edit when the monitor is turned off', async () => {
      const user = userEvent.setup()
      const { read, client } = show(list([on(A, { contains: 'Welcome' })]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: 'must contain "Welcome"' }))
      await user.type(a.getByLabelText('Page must contain'), ' draft')
      read.mockResolvedValue(list([monitor(A, { contains: 'Welcome' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      await waitFor(() => expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument())
      read.mockResolvedValue(list([on(A, { contains: 'Welcome' })]))
      await client.invalidateQueries({ queryKey: ['monitor'] })
      expect(await a.findByRole('button', { name: 'must contain "Welcome"' })).toBeInTheDocument()
      expect(a.queryByLabelText('Page must contain')).not.toBeInTheDocument()
    })
  })

  describe('the certificate', () => {
    const certOf = (cert: Partial<Monitor['cert']>) => monitor(A, { enabled: true, cert: { ...monitor(A).cert, checked_at: 'x', ...cert } })

    it('says when it is unchecked, with no second line', async () => {
      show(list([on(A)]))
      const a = await row(A)
      expect(a.getByText('Cert unknown')).toBeInTheDocument()
      expect(a.queryByText(/^Certificate:/)).not.toBeInTheDocument()
    })

    it('has a second line for an error, known expiry or not', async () => {
      show(list([certOf({ error: 'x509: expired' })]))
      const a = await row(A)
      expect(a.getByText('Cert unknown')).toBeInTheDocument()
      expect(a.getByText('Certificate: x509: expired')).toBeInTheDocument()
    })

    it('does not repeat a failure the status line already says', async () => {
      const last = { checked_at: '2026-11-01T10:00:00Z', up: false, status: 0, reason: 'dns', error: 'no such host', ms: 3 }
      show(list([
        on(A, { last, cert: { ...monitor(A).cert, error: 'no such host' } }),
        on(B, { last, cert: { ...monitor(A).cert, error: 'TLS handshake failed' } }),
      ]))
      expect((await row(A)).queryByText(/^Certificate:/)).not.toBeInTheDocument()
      expect((await row(A)).getByText('Cert unknown')).toBeInTheDocument()
      expect((await row(B)).getByText('Certificate: TLS handshake failed')).toBeInTheDocument()
    })

    it('shows both a known expiry and the check that failed since', async () => {
      show(list([certOf({ expires_at: days(30).toISOString(), valid: true, error: 'connection refused' })]))
      const a = await row(A)
      expect(a.getByText('Cert 30 days')).toBeInTheDocument()
      expect(a.getByText(/^Certificate: expires \S+ \(30 days\); last check failed: connection refused$/)).toBeInTheDocument()
    })

    it('says a certificate that was read but does not verify is invalid', async () => {
      show(list([certOf({ expires_at: days(30).toISOString(), valid: false, error: 'x509: unknown authority' })]))
      const a = await row(A)
      expect(a.getByText('Cert invalid')).toHaveClass('text-destructive')
      expect(a.getByText(/^Certificate: invalid, expires \S+ \(30 days\): x509: unknown authority$/)).toBeInTheDocument()
    })

    it('says expired once the day has passed', async () => {
      show(list([certOf({ expires_at: days(-3).toISOString(), valid: false })]))
      const expired = (await row(A)).getByText('Cert expired')
      expect(expired).toHaveClass('text-destructive')
    })

    it('never wraps, and moves into the stacked block on a phone', async () => {
      show(list([certOf({ expires_at: days(30).toISOString(), valid: true })]))
      const cert = (await row(A)).getByText('Cert 30 days')
      expect(cert).toHaveClass('whitespace-nowrap', 'col-start-2', 'sm:col-start-3')
    })

    it('says nothing on an http:// origin, which has none', async () => {
      show(list([on('http://a.example', { cert: { ...monitor(A).cert, checked_at: 'x' } })]))
      const a = await row('http://a.example')
      expect(a.queryByText(/cert/i)).not.toBeInTheDocument()
    })
  })

  it('offers a retry when the read fails', async () => {
    const user = userEvent.setup()
    const read = vi.spyOn(endpoints, 'monitors').mockRejectedValueOnce(new Error('boom')).mockResolvedValue(list([monitor(A)]))
    vi.spyOn(endpoints, 'projectTabs').mockResolvedValue({ tabs: [] })
    renderWithProviders(<MemoryRouter><MonitoringSection projectId={4} onCheck={vi.fn()} onSave={vi.fn()} /></MemoryRouter>)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the monitors. boom")
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('switch', { name: `Monitor ${A}` })).toBeInTheDocument()
    expect(read).toHaveBeenCalledTimes(2)
  })

  describe('redirect warning', () => {
    it('warns at 3 hops, with the count', async () => {
      show(list([on(A, { redirects: 3 }), on(B, { redirects: 5 })]))
      expect((await row(A)).getByText('Redirects 3 times before answering, which slows every visit')).toBeInTheDocument()
      expect((await row(B)).getByText('Redirects 5 times before answering, which slows every visit')).toBeInTheDocument()
    })

    it('says nothing at 2 hops, or when the monitor is off', async () => {
      show(list([on(A, { redirects: 2 }), monitor(B, { redirects: 4 })]))
      await row(A)
      expect(screen.queryByText(/Redirects \d+ times/)).not.toBeInTheDocument()
    })
  })

  describe('the status line', () => {
    const checked = (minutes: number) => new Date(Date.now() - minutes * 60_000 - 1000).toISOString()
    /** The whole line a text is in. */
    const line = (el: HTMLElement) => el.closest('p') as HTMLElement

    it('reads Up with the status, the time and the age, from a scheduled check with no manual one', async () => {
      show(list([on(A, { last: { checked_at: checked(11), up: true, status: 200, reason: '', error: '', ms: 1234 } })]))
      const up = (await row(A)).getByText('Up, 200, 1,234 ms, 11 min ago')
      expect(line(up).querySelector('[aria-hidden]')).toHaveClass('bg-emerald-500')
      expect(line(up)).toHaveClass('text-muted-foreground')
    })

    it('reads Down with the failure text, the word and the dot in the destructive colour, and wraps', async () => {
      show(list([on(A, { last: { checked_at: checked(1), up: false, status: 0, reason: 'dns', error: 'no such host', ms: 3 } })]))
      const down = (await row(A)).getByText('Down')
      expect(down).toHaveClass('text-destructive')
      const p = line(down)
      expect(p).toHaveTextContent(/^Down, no such host, 1 min ago, checked every 2 min$/)
      expect(p.querySelector('[aria-hidden]')).toHaveClass('bg-destructive')
      expect(down.parentElement).toHaveClass('break-words')
    })

    it('reads Not checked yet before the first result, with no title', async () => {
      show(list([on(A)]))
      const text = (await row(A)).getByText('Not checked yet')
      expect(line(text)).not.toHaveAttribute('title')
      expect(line(text).querySelector('[aria-hidden]')).toHaveClass('bg-muted-foreground/40')
    })

    it('reads Checking… while a manual check runs', async () => {
      show(list([on(A, { check: { requested_at: '2026-11-01T10:00:00Z', finished_at: '' } })]))
      expect((await row(A)).getByText('Checking…')).toBeInTheDocument()
    })

    it('reads Paused on a paused install, whatever the last result', async () => {
      show(list([on(A, { last: { checked_at: checked(5), up: false, status: 503, reason: 'http_5xx', error: 'HTTP 503', ms: 3 } })], { paused: true }))
      const a = await row(A)
      expect(a.getByText('Paused')).toBeInTheDocument()
      expect(a.queryByText('Down')).not.toBeInTheDocument()
      expect(a.queryByText(/HTTP 503/)).not.toBeInTheDocument()
    })

    it('has the exact time and the HTTP status as its title', async () => {
      show(list([
        on(A, { last: { checked_at: '2026-11-01T19:30:12Z', up: true, status: 200, reason: '', error: '', ms: 5 } }),
        on(B, { last: { checked_at: '2026-11-01T19:30:12Z', up: false, status: 0, reason: 'dns', error: 'no such host', ms: 5 } }),
      ]))
      expect(line((await row(A)).getByText(/^Up,/))).toHaveAttribute('title', 'HTTP 200, 2026-11-01 19:30 UTC')
      expect(line((await row(B)).getByText('Down'))).toHaveAttribute('title', '2026-11-01 19:30 UTC')
    })

    it('stacks the origin, the text and the status line in that order, then the notes', async () => {
      show(list([on(A, { contains: 'Welcome', redirects: 3, last: { checked_at: checked(1), up: true, status: 200, reason: '', error: '', ms: 5 } })]))
      const a = await row(A)
      const li = screen.getByRole('listitem', { name: A })
      const order = [a.getByText(A), a.getByRole('button', { name: 'must contain "Welcome"' }), a.getByText(/^Up,/), a.getByText(/^Redirects 3/)]
      for (let i = 1; i < order.length; i++) {
        expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
      }
      expect(li).toContainElement(order[3])
    })

    it('is not on an off row, nor anything else but the switch and the origin', async () => {
      show(list([monitor(A, { contains: 'x', redirects: 4, last: { checked_at: checked(1), up: true, status: 200, reason: '', error: '', ms: 5 } })]))
      const a = await row(A)
      expect(a.getByText(A)).toHaveClass('text-muted-foreground')
      expect(a.queryByText(/^Up,|Not checked|Cert/)).not.toBeInTheDocument()
      expect(a.queryAllByRole('button')).toEqual([])
      expect(screen.getByRole('listitem', { name: A }).childElementCount).toBe(2)
    })
  })

  describe('Check now', () => {
    const at = '2026-11-01T10:00:00Z'
    const pendingCheck = { requested_at: at, finished_at: '' }
    const done = { requested_at: at, finished_at: '2026-11-01T10:00:02Z' }
    const result = (over: Partial<Monitor['last']>): Monitor['last'] => ({ ...monitor(A).last, checked_at: '2026-11-01T10:00:02Z', ...over })

    it('is an icon button with a tooltip, which spins while the check runs', async () => {
      const user = userEvent.setup()
      const { read } = show(list([on(A), on(B, { check: pendingCheck })]))
      const idle = (await row(A)).getByRole('button', { name: `Check ${A} now` })
      expect(idle.querySelector('svg')).not.toHaveClass('animate-spin')
      await user.hover(idle)
      expect(await screen.findByRole('tooltip')).toHaveTextContent('Check now')
      const busy = (await row(B)).getByRole('button', { name: `Check ${B} now` })
      expect(busy).toBeDisabled()
      expect(busy.querySelector('svg')).toHaveClass('animate-spin')
      expect(read).toHaveBeenCalled()
    })

    it('is per row, and asks for that origin\'s check', async () => {
      const user = userEvent.setup()
      const { onCheck } = show(list([on(A), on(B)]))
      await user.click((await row(B)).getByRole('button', { name: `Check ${B} now` }))
      expect(onCheck).toHaveBeenCalledTimes(1)
      expect(onCheck).toHaveBeenCalledWith(B)
    })

    it('reads Checking… in the row with a check pending only', async () => {
      show(list([on(A, { check: pendingCheck }), on(B)]))
      const a = await row(A)
      expect(a.getByRole('button', { name: `Check ${A} now` })).toBeDisabled()
      expect(a.getByText('Checking…')).toBeInTheDocument()
      expect((await row(B)).getByRole('button', { name: `Check ${B} now` })).toBeEnabled()
    })

    it('reads a request as pending while the previous result stands, and finished once the finish time reaches it', async () => {
      show(list([
        on(A, { check: pendingCheck, last: result({ up: true, status: 200, ms: 5 }) }),
        on(B, { check: { ...done, finished_at: at }, last: result({ up: true, status: 200, ms: 5 }) }),
      ]))
      const a = await row(A)
      expect(a.getByRole('button', { name: `Check ${A} now` })).toBeDisabled()
      expect(a.getByText('Checking…')).toBeInTheDocument()
      expect(a.queryByText(/^Up,/)).not.toBeInTheDocument()
      expect((await row(B)).getByRole('button', { name: `Check ${B} now` })).toBeEnabled()
    })

    it('polls while a row has one pending, shows its result there, and stops', async () => {
      const { read } = show(list([on(A, { check: pendingCheck }), on(B)]))
      const a = await row(A)
      read.mockResolvedValue(list([on(A, { check: done, last: result({ up: true, status: 200, ms: 123 }) }), on(B)]))
      expect(await a.findByText(/^Up, 200, 123 ms, .+ ago$/, undefined, { timeout: 4000 })).toBeInTheDocument()
      expect(a.getByRole('button', { name: `Check ${A} now` })).toBeEnabled()
      expect((await row(B)).queryByText(/^Up,/)).not.toBeInTheDocument()
      const calls = read.mock.calls.length
      await new Promise((r) => setTimeout(r, 2300))
      expect(read.mock.calls.length).toBe(calls)
    }, 10_000)

    it('keeps an edit through the polling', async () => {
      const user = userEvent.setup()
      const { read } = show(list([on(A, { contains: 'Welcome', check: pendingCheck })]))
      const a = await row(A)
      await user.click(a.getByRole('button', { name: 'must contain "Welcome"' }))
      const text = a.getByLabelText('Page must contain')
      await user.type(text, ' back')
      read.mockResolvedValue(list([on(A, { contains: 'Welcome', check: done, last: result({ up: true, status: 200, ms: 5 }) })]))
      await waitFor(() => expect(read.mock.results.length).toBeGreaterThan(1), { timeout: 4000 })
      expect(text).toHaveValue('Welcome back')
    }, 10_000)

    it('says so when the check it asked for did not run, under the result that stands', async () => {
      show(list([
        on(A, { check: done, last: result({ checked_at: '2026-11-01T09:58:00Z', up: true, status: 200, ms: 5 }) }),
        on(B, { check: done, last: result({ up: true, status: 200, ms: 5 }) }),
        on(C, { check: done }),
      ]))
      const a = await row(A)
      expect(a.getByText('The last check did not run')).toBeInTheDocument()
      expect(a.getByText(/^Up, 200, 5 ms/)).toBeInTheDocument()
      expect((await row(B)).queryByText(/did not run/)).not.toBeInTheDocument()
      expect((await row(C)).getByText('The last check did not run')).toBeInTheDocument()
    })

    it('gives up waiting on a row after 45 s with a message, and keeps looking every 15 s', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        const { read } = show(list([on(A, { check: pendingCheck }), on(B)]))
        const a = await row(A)
        expect(a.getByRole('button', { name: `Check ${A} now` })).toBeDisabled()
        expect(a.getByText('Checking…')).toBeInTheDocument()
        expect(a.queryByText(/has not finished/)).not.toBeInTheDocument()
        await vi.advanceTimersByTimeAsync(45_500)
        expect(await a.findByText(/has not finished/)).toBeInTheDocument()
        expect(a.getByRole('button', { name: `Check ${A} now` })).toBeEnabled()
        // It looks less often, not never: the text says the result will show.
        const calls = read.mock.calls.length
        await vi.advanceTimersByTimeAsync(10_000)
        expect(read.mock.calls.length).toBe(calls)
        await vi.advanceTimersByTimeAsync(5_500)
        expect(read.mock.calls.length).toBe(calls + 1)
        read.mockResolvedValue(list([on(A, { check: done, last: result({ up: true, status: 200, ms: 7 }) }), on(B)]))
        await vi.advanceTimersByTimeAsync(15_500)
        expect(await a.findByText(/^Up, 200, 7 ms/)).toBeInTheDocument()
        expect(a.queryByText(/has not finished/)).not.toBeInTheDocument()
        // Finished, it goes back to its once a minute.
        const finished = read.mock.calls.length
        await vi.advanceTimersByTimeAsync(30_000)
        expect(read.mock.calls.length).toBe(finished)
        await vi.advanceTimersByTimeAsync(31_000)
        expect(read.mock.calls.length).toBe(finished + 1)
      } finally {
        vi.useRealTimers()
      }
    })

    it('refreshes a monitored row once a minute, and an unmonitored section never', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        const { read } = show(list([on(A)]))
        const a = await row(A)
        const calls = read.mock.calls.length
        read.mockResolvedValue(list([on(A, { last: result({ up: false, reason: 'dns', error: 'no such host' }) })]))
        await vi.advanceTimersByTimeAsync(61_000)
        expect(read.mock.calls.length).toBe(calls + 1)
        expect(await a.findByText('no such host', { exact: false })).toBeInTheDocument()

        cleanup()
        const off = show(list([on(A, { enabled: false })]))
        await row(A)
        const none = off.read.mock.calls.length
        await vi.advanceTimersByTimeAsync(61_000)
        expect(off.read.mock.calls.length).toBe(none)
      } finally {
        vi.useRealTimers()
      }
    })

    it('keeps the age current on an open page when the poll brings nothing new', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        const at = new Date().toISOString()
        const data = () => list([on(A, { last: result({ up: true, status: 200, ms: 12, checked_at: at }) })])
        const { read } = show(data())
        const a = await row(A)
        expect(await a.findByText('Up, 200, 12 ms, just now')).toBeInTheDocument()
        const calls = read.mock.calls.length
        read.mockResolvedValue(data()) // the same JSON: the query keeps its data
        await vi.advanceTimersByTimeAsync(61_000)
        expect(read.mock.calls.length).toBe(calls + 1)
        expect(await a.findByText('Up, 200, 12 ms, 1 min ago')).toBeInTheDocument()
        await vi.advanceTimersByTimeAsync(120_000)
        expect(await a.findByText('Up, 200, 12 ms, 3 min ago')).toBeInTheDocument()
      } finally {
        vi.useRealTimers()
      }
    })

    it('polls fast again when another row starts a check after one gave up', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      try {
        const { read, client } = show(list([on(A, { check: pendingCheck }), on(B)]))
        await row(A)
        await vi.advanceTimersByTimeAsync(45_500)
        expect(await screen.findByText(/has not finished/)).toBeInTheDocument()
        client.setQueryData(['monitor', 4], list([on(A, { check: pendingCheck }), on(B, { check: { ...pendingCheck, requested_at: '2026-11-01T10:05:00Z' } })]))
        read.mockResolvedValue(list([on(A, { check: pendingCheck }), on(B, { check: { ...pendingCheck, requested_at: '2026-11-01T10:05:00Z' } })]))
        const calls = read.mock.calls.length
        await vi.advanceTimersByTimeAsync(2_500)
        expect(read.mock.calls.length).toBeGreaterThan(calls)
      } finally {
        vi.useRealTimers()
      }
    })

    it('reads the monitors again when the request is refused, showing the check that is running', async () => {
      const user = userEvent.setup()
      const onCheck = vi.fn().mockResolvedValue(false)
      const { read } = show(list([on(A)]), undefined, onCheck)
      const a = await row(A)
      read.mockResolvedValue(list([on(A, { check: pendingCheck })]))
      await user.click(a.getByRole('button', { name: `Check ${A} now` }))
      expect(await a.findByText('Checking…')).toBeInTheDocument()
      expect(a.getByRole('button', { name: `Check ${A} now` })).toBeDisabled()
    })

    it('does not read the monitors again when the request went through', async () => {
      const user = userEvent.setup()
      const { read } = show(list([on(A)]))
      const a = await row(A)
      const calls = read.mock.calls.length
      await user.click(a.getByRole('button', { name: `Check ${A} now` }))
      await new Promise((r) => setTimeout(r, 50))
      expect(read.mock.calls.length).toBe(calls)
    })
  })
})
