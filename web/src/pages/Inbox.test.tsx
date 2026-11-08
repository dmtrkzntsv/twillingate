import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type Notification } from '@/lib/api'
import { dashboardsList } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Inbox from './Inbox'

function note(id: number, over: Partial<Notification> = {}): Notification {
  return {
    id,
    type: 'uptime_down',
    project_id: 3,
    project: 'econumo.com',
    subject: 'https://econumo.com',
    title: 'https://econumo.com is down',
    body: 'econumo.com: 2 checks failed: timeout',
    path: '/projects/3/settings/domains',
    fields: {},
    created_at: '2026-10-06T11:00:00Z',
    delivery: 'sent',
    ...over,
  }
}

const down = note(2)
const draft = note(1, {
  type: 'form_draft_created',
  subject: 'contact',
  title: 'New form contact is waiting for approval',
  body: 'econumo.com',
  path: '/projects/3/forms/contact',
  created_at: '2026-10-06T09:00:00Z',
  delivery: 'failed',
  delivery_error: 'telegram: 401 Unauthorized',
})

/** The server's inbox: `active` and `archived` move as the page archives and restores. */
let active: Notification[]
let archived: Notification[]

function serve() {
  vi.spyOn(endpoints, 'listNotifications').mockImplementation(async (q) => ({
    notifications: q.archived ? archived : active,
    active: active.length,
  }))
  vi.spyOn(endpoints, 'archiveNotifications').mockImplementation(async (body) => {
    const ids = 'all' in body ? active.map((n) => n.id) : body.ids
    archived = [...active.filter((n) => ids.includes(n.id)), ...archived]
    active = active.filter((n) => !ids.includes(n.id))
    return { archived: ids.length }
  })
  vi.spyOn(endpoints, 'restoreNotifications').mockImplementation(async (ids) => {
    active = [...archived.filter((n) => ids.includes(n.id)), ...active]
    archived = archived.filter((n) => !ids.includes(n.id))
    return { restored: ids.length }
  })
}

function renderInbox(path = '/inbox') {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Inbox />
    </MemoryRouter>
  )
}

/** The inbox item titled `title`. */
async function item(title: string): Promise<HTMLElement> {
  return screen.findByRole('listitem', { name: title })
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-06T12:00:00Z'))
  vi.restoreAllMocks()
  active = [down, draft]
  archived = []
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(dashboardsList())
  serve()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('Inbox', () => {
  it('lists the active items, newest first, each with its project, when, and a link to its page', async () => {
    renderInbox()
    const first = await item(down.title)
    expect(within(first).getByText(down.body)).toBeInTheDocument()
    expect(within(first).getByText(/econumo\.com ·/)).toBeInTheDocument()
    const when = within(first).getByText('1 h ago')
    expect(when).toHaveAttribute('title')
    expect(when.getAttribute('title')).not.toBe('')
    expect(within(first).getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/projects/3/settings/domains')
    const items = within(screen.getByRole('list', { name: 'Notifications' })).getAllByRole('listitem')
    expect(items.map((li) => li.getAttribute('aria-label'))).toEqual([down.title, draft.title])
    expect(endpoints.listNotifications).toHaveBeenCalledWith(expect.not.objectContaining({ archived: true }))
  })

  it('says why an item never reached the channel', async () => {
    renderInbox()
    expect(within(await item(draft.title)).getByText('Not delivered: telegram: 401 Unauthorized')).toBeInTheDocument()
    expect(within(await item(down.title)).queryByText(/Not delivered/)).not.toBeInTheDocument()
  })

  it('archives one item by its id and drops its row', async () => {
    const user = userEvent.setup()
    renderInbox()
    await user.click(within(await item(down.title)).getByRole('button', { name: 'Archive' }))
    expect(endpoints.archiveNotifications).toHaveBeenCalledWith({ ids: [down.id] })
    await waitFor(() => expect(screen.queryByRole('listitem', { name: down.title })).not.toBeInTheDocument())
    expect(screen.getByRole('listitem', { name: draft.title })).toBeInTheDocument()
  })

  it('archives everything only once confirmed, then shows the empty inbox', async () => {
    const user = userEvent.setup()
    renderInbox()
    await item(down.title)
    await user.click(screen.getByRole('button', { name: 'Archive all' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(endpoints.archiveNotifications).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Archive all' }))
    expect(endpoints.archiveNotifications).toHaveBeenCalledWith({ all: true })
    expect(await screen.findByText('Nothing in your inbox')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Archive all' })).toBeDisabled()
  })

  it('shows the archived items on their tab, each restored by its id', async () => {
    archived = [note(9, { title: 'https://old.example is up', type: 'uptime_up', archived_at: '2026-10-06T10:00:00Z' })]
    const user = userEvent.setup()
    renderInbox()
    await item(down.title)
    await user.click(screen.getByRole('tab', { name: 'Archived' }))
    const old = await item('https://old.example is up')
    expect(screen.queryByRole('button', { name: 'Archive all' })).not.toBeInTheDocument()
    await user.click(within(old).getByRole('button', { name: 'Restore' }))
    expect(endpoints.restoreNotifications).toHaveBeenCalledWith([9])
    await waitFor(() => expect(screen.queryByRole('listitem', { name: 'https://old.example is up' })).not.toBeInTheDocument())
  })

  it('loads the next page with the cursor the last one ended on', async () => {
    const older = note(5, { title: 'An older one' })
    vi.mocked(endpoints.listNotifications).mockImplementation(async (q) =>
      q.cursor === 'c2' ? { notifications: [older], active: 3 } : { notifications: [down, draft], active: 3, next_cursor: 'c2' }
    )
    const user = userEvent.setup()
    renderInbox()
    await item(down.title)
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await item('An older one')).toBeInTheDocument()
    expect(endpoints.listNotifications).toHaveBeenCalledWith(expect.objectContaining({ cursor: 'c2' }))
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
  })
})
