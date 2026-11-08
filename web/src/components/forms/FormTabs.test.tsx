import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import type { DndContextProps } from '@dnd-kit/core'
import { endpoints, type Project } from '@/lib/api'
import { readLastForm } from '@/lib/last-form'
import { draft, form } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import FormTabs from './FormTabs'

// jsdom has no layout for dnd-kit to measure: keep the last DndContext's
// props, to drop a tab the way DndContext would.
const dnd = vi.hoisted(() => ({ props: undefined as DndContextProps | undefined }))
vi.mock('@dnd-kit/core', async (importActual) => {
  const actual = await importActual<typeof import('@dnd-kit/core')>()
  return {
    ...actual,
    DndContext: (props: DndContextProps) => {
      dnd.props = props
      return <actual.DndContext {...props} />
    },
  }
})

const project: Project = { project_id: 4, name: 'shop', allowed_origins: ['https://shop.example'] }

beforeEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
  dnd.props = undefined
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [] })
  vi.spyOn(endpoints, 'forms').mockResolvedValue({
    action_base: '',
    forms: [draft('signup', 3, { new_submissions: 2 }), form('contact'), form('survey', { new_submissions: 120 })],
  })
})

function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}

/** Renders the row on form `current`'s tab, or on `/forms` itself with null. */
function renderTabs(on: string | null = 'contact') {
  const current = on ?? undefined
  return renderWithProviders(
    <MemoryRouter initialEntries={[`/projects/4/forms${current ? `/${current}` : ''}?range=7d`]}>
      <Routes>
        <Route path="/projects/:id/forms/:name?" element={<FormTabs project={project} current={current} search="range=7d" />} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>
  )
}

/** Drops the tab of `id` on the tab of `over`, as DndContext calls it. */
function drop(active: number, over: number) {
  dnd.props?.onDragEnd?.({ active: { id: active }, over: { id: over } } as never)
}

// jsdom lacks the pointer-capture and scrolling calls Radix Select makes when it opens.
beforeAll(() => {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.releasePointerCapture ??= () => {}
  Element.prototype.scrollIntoView ??= () => {}
})

describe('FormTabs', () => {
  it("shows one tab per form in the server's order, a draft marked and new submissions counted", async () => {
    renderTabs()
    await screen.findByRole('tab', { name: 'contact' })
    const tabs = screen.getAllByRole('tab')
    expect(tabs.map((t) => t.getAttribute('aria-label') ?? t.textContent)).toEqual(['signup (draft, 2 new)', 'contact', 'survey (120 new)'])
    expect(tabs[0].querySelector('[data-draft]')).not.toBeNull()
    expect(tabs[0]).toHaveTextContent('signup2')
    expect(tabs[1].querySelector('[data-draft]')).toBeNull()
    expect(tabs[2]).toHaveTextContent('survey99+')
    expect(screen.getByRole('tab', { name: 'contact' })).toHaveAttribute('aria-selected', 'true')
  })

  it('says Draft in a tooltip on the draft mark', async () => {
    const user = userEvent.setup()
    renderTabs()
    const tab = await screen.findByRole('tab', { name: 'signup (draft, 2 new)' })
    await user.hover(tab.querySelector('[data-draft]')!)
    expect(await screen.findByRole('tooltip')).toHaveTextContent('Draft')
  })

  it('lists drafts in the phone select too', async () => {
    const user = userEvent.setup()
    renderTabs()
    await user.click(await screen.findByRole('combobox', { name: 'Tab' }))
    expect(await screen.findByRole('option', { name: 'signup (draft, 2 new)' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'contact' })).toBeInTheDocument()
  })

  it("opens a form's tab, keeping the range, and remembers it", async () => {
    const user = userEvent.setup()
    renderTabs()
    await user.click(await screen.findByRole('tab', { name: 'survey (120 new)' }))
    expect(screen.getByTestId('location')).toHaveTextContent('/projects/4/forms/survey?range=7d')
    expect(readLastForm(4)).toBe('survey')
  })

  it('moves a dropped tab with move_form after the form before its new place, then reads the order again', async () => {
    const move = vi.spyOn(endpoints, 'moveForm').mockResolvedValue({ status: 'moved' })
    renderTabs()
    await screen.findByRole('tab', { name: 'contact' })
    const idOf = (name: string) => {
      const tab = screen.getAllByRole('tab').find((t) => (t.getAttribute('aria-label') ?? t.textContent)!.startsWith(name))!
      return Number(tab.id.replace(/.*-trigger-/, ''))
    }
    vi.mocked(endpoints.forms).mockResolvedValue({
      action_base: '',
      forms: [form('survey', { new_submissions: 120 }), draft('signup', 3, { new_submissions: 2 }), form('contact')],
    })
    drop(idOf('survey'), idOf('signup'))
    await waitFor(() => expect(move).toHaveBeenCalledWith(4, 'survey', ''))
    await waitFor(() =>
      expect(screen.getAllByRole('tab').map((t) => t.getAttribute('aria-label') ?? t.textContent)).toEqual([
        'survey (120 new)',
        'signup (draft, 2 new)',
        'contact',
      ])
    )
    expect(endpoints.forms).toHaveBeenCalledTimes(2)

    drop(idOf('survey'), idOf('contact'))
    await waitFor(() => expect(move).toHaveBeenLastCalledWith(4, 'survey', 'contact'))
  })

  it('snaps a refused move back', async () => {
    vi.spyOn(endpoints, 'moveForm').mockRejectedValue(new Error('no'))
    renderTabs()
    await screen.findByRole('tab', { name: 'contact' })
    const ids = screen.getAllByRole('tab').map((t) => Number(t.id.replace(/.*-trigger-/, '')))
    drop(ids[0], ids[2])
    await waitFor(() => expect(endpoints.moveForm).toHaveBeenCalledWith(4, 'signup', 'survey'))
    await waitFor(() =>
      expect(screen.getAllByRole('tab').map((t) => t.getAttribute('aria-label') ?? t.textContent)).toEqual([
        'signup (draft, 2 new)',
        'contact',
        'survey (120 new)',
      ])
    )
  })

  it('opens New form from "+"', async () => {
    const user = userEvent.setup()
    renderTabs()
    await user.click(await screen.findByRole('button', { name: 'New form' }))
    expect(await screen.findByRole('dialog', { name: 'New form' })).toBeInTheDocument()
  })

  it('opens Find a person, across every form, in a dialog', async () => {
    const user = userEvent.setup()
    renderTabs()
    await user.click(await screen.findByRole('button', { name: 'Find a person' }))
    const dialog = await screen.findByRole('dialog', { name: 'Find a person' })
    expect(within(dialog).getByRole('searchbox', { name: 'Find a person' })).toBeInTheDocument()
  })

  it('selects no tab on /forms itself', async () => {
    renderTabs(null)
    await screen.findByRole('tab', { name: 'contact' })
    expect(screen.queryByRole('tab', { selected: true })).toBeNull()
  })
})
