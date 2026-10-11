import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { widgets } from '@/components/widgets'
import { ApiError, endpoints, type Widget } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import { componentIcons } from './component-icons'
import WidgetEditor, { shouldKeepOpenOnEscape } from './WidgetEditor'

vi.mock('./CodeEditor', () => ({
  default: ({ value, onChange, label, onRun }: { value: string; onChange: (v: string) => void; label: string; onRun?: () => void }) => (
    <textarea
      aria-label={label}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={(e) => e.key === 'Enter' && e.metaKey && onRun?.()}
    />
  ),
}))

const defaults = { projectId: 1, range: { range: '7d' as const, from: '2026-09-08', to: '2026-09-14' }, timezone: 'UTC' }
const line: Widget = {
  widget_id: 7,
  dashboard_id: 3,
  name: 'v',
  component: 'line',
  title: 'Visits',
  width: 6,
  height: 8,
  props: {},
  source: { type: 'sql', content: 'SELECT day AS x, views AS y FROM v WHERE project_id=:project' },
  follows_project: true,
  follows_range: false,
}

// jsdom lacks the pointer-capture and scrolling calls Radix Select makes when it opens.
beforeAll(() => {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.releasePointerCapture ??= () => {}
  Element.prototype.scrollIntoView ??= () => {}
})

beforeEach(() => {
  vi.restoreAllMocks()
  vi.spyOn(endpoints, 'schemaViews').mockResolvedValue('  v_views_daily(project_id, day, views)\n')
  // An edit runs its query as it opens: a test that cares about the answer sets its own.
  vi.spyOn(endpoints, 'previewWidget').mockResolvedValue({ columns: [], rows: [], truncated: false, elapsed_ms: 1 })
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [{ project_id: 1, name: 'Acme', allowed_origins: [] }] } as never)
})

function open(props: Partial<Parameters<typeof WidgetEditor>[0]> = {}) {
  const onSave = vi.fn().mockResolvedValue(undefined)
  const onOpenChange = vi.fn()
  renderWithProviders(<WidgetEditor open onOpenChange={onOpenChange} defaults={defaults} onSave={onSave} {...props} />)
  return { onSave, onOpenChange }
}

describe('WidgetEditor', () => {
  it('disables Save until a visualization is chosen', async () => {
    open()
    expect(await screen.findByRole('button', { name: 'Save' })).toBeDisabled()
    expect(screen.getByText('Choose a visualization to run')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Run query/ })).toBeDisabled()
  })

  it('runs the stored query as an edit opens, showing preview, rows and problem', async () => {
    const preview = vi.spyOn(endpoints, 'previewWidget').mockResolvedValue({
      columns: ['x'],
      rows: [['2026-09-01']],
      truncated: false,
      problem: 'line needs y (number); columns are x',
      elapsed_ms: 4,
    })
    open({ widget: line })
    expect(await screen.findByText('line needs y (number); columns are x')).toBeInTheDocument()
    expect(preview).toHaveBeenCalledTimes(1)
    expect(preview).toHaveBeenCalledWith(
      expect.objectContaining({ component: 'line', project_id: 1, source: { type: 'sql', content: line.source.content } })
    )
    expect(screen.getByText(/1 row · 4 ms/)).toBeInTheDocument()
    expect(screen.getByRole('cell', { name: '2026-09-01' })).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Preview' })).getByLabelText('Title')).toHaveValue('Visits')
    expect(preview.mock.calls[0][0]).not.toHaveProperty('from') // the sql reads no range
  })

  it('runs nothing as an add opens', async () => {
    const preview = vi.spyOn(endpoints, 'previewWidget').mockResolvedValue({ columns: [], rows: [], truncated: false, elapsed_ms: 1 })
    open()
    expect(await screen.findByText(/Choose a visualization to preview/)).toBeInTheDocument()
    expect(preview).not.toHaveBeenCalled()
  })

  it('does not run again while the sql is typed', async () => {
    const answer = { columns: ['x', 'y'], rows: [['a', '1']], truncated: false, elapsed_ms: 1 }
    const preview = vi.spyOn(endpoints, 'previewWidget').mockResolvedValue(answer)
    open({ widget: line })
    await screen.findByText(/1 row/)
    await userEvent.type(screen.getByLabelText('SQL'), ' LIMIT 1')
    await new Promise((r) => setTimeout(r, 500))
    expect(preview).toHaveBeenCalledTimes(1)
    await userEvent.click(screen.getByRole('button', { name: /Run query/ }))
    expect(preview).toHaveBeenCalledTimes(2)
    expect(preview.mock.calls[1][0].source.content).toBe(line.source.content + ' LIMIT 1')
  })

  it('runs nothing on its own once the sql is edited, even as its parameters or the visualization change', async () => {
    const answer = { columns: ['x', 'y'], rows: [['a', '1']], truncated: false, elapsed_ms: 1 }
    const preview = vi.spyOn(endpoints, 'previewWidget').mockResolvedValue(answer)
    open({ widget: line })
    await screen.findByText(/1 row/)
    // Reading :from and :to brings the range in: what an automatic run would read changes.
    await userEvent.type(screen.getByLabelText('SQL'), ' AND day BETWEEN :from AND :to')
    await userEvent.click(screen.getByRole('combobox', { name: 'Visualization' }))
    await userEvent.click(await screen.findByRole('option', { name: /^bar$/ }))
    await new Promise((r) => setTimeout(r, 500))
    expect(preview).toHaveBeenCalledTimes(1)
    expect(screen.getByText('Changed since last run')).toBeInTheDocument()
  })

  it('sends the range and shows its switcher when the sql reads :from and :to', async () => {
    const preview = vi.spyOn(endpoints, 'previewWidget').mockResolvedValue({ columns: [], rows: [], truncated: false, elapsed_ms: 1 })
    open({ widget: { ...line, source: { type: 'sql', content: 'SELECT day AS x, 1 AS y FROM v WHERE day BETWEEN :from AND :to' } } })
    await waitFor(() => expect(preview).toHaveBeenCalled())
    expect(preview.mock.calls[0][0]).toMatchObject({ from: '2026-09-08', to: '2026-09-14' })
    expect(preview.mock.calls[0][0]).not.toHaveProperty('project_id')
    expect(screen.queryByRole('combobox', { name: 'Project' })).toBeNull()
  })

  it('shows a refusal in place of the results', async () => {
    vi.spyOn(endpoints, 'previewWidget').mockRejectedValue(new ApiError(400, 'no such table: v', 'invalid'))
    open({ widget: line })
    expect(await screen.findByText('no such table: v')).toBeInTheDocument()
    expect(screen.getByText(/The query was refused/)).toBeInTheDocument()
  })

  it('marks results stale when the sql changes after a run', async () => {
    vi.spyOn(endpoints, 'previewWidget').mockResolvedValue({ columns: ['x', 'y'], rows: [['a', '1']], truncated: false, elapsed_ms: 1 })
    open({ widget: line })
    await screen.findByText(/1 row/)
    expect(screen.queryByText('Changed since last run')).toBeNull()
    await userEvent.type(screen.getByLabelText('SQL'), ' ')
    expect(screen.getByText('Changed since last run')).toBeInTheDocument()
  })

  it("drops the run's warning once the visualization changes, then checks the rows against the new one", async () => {
    const preview = vi.spyOn(endpoints, 'previewWidget').mockResolvedValueOnce({
      columns: ['x'],
      rows: [['2026-09-01']],
      truncated: false,
      problem: 'line needs y (number); columns are x',
      elapsed_ms: 4,
    })
    preview.mockResolvedValueOnce({
      columns: ['x'],
      rows: [['2026-09-01']],
      truncated: false,
      problem: 'bar_list needs label (text); columns are x',
      elapsed_ms: 4,
    })
    open({ widget: line })
    await screen.findByText('line needs y (number); columns are x')
    await userEvent.click(screen.getByRole('combobox', { name: 'Visualization' }))
    await userEvent.click(await screen.findByRole('option', { name: /bar_list/ }))
    expect(screen.queryByText('line needs y (number); columns are x')).toBeNull()
    expect(await screen.findByText('bar_list needs label (text); columns are x')).toBeInTheDocument()
    expect(preview).toHaveBeenCalledTimes(2)
    expect(preview.mock.calls[1][0]).toMatchObject({ component: 'bar_list' })
  })

  it('redraws from the same rows when the visualization changes', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.spyOn(endpoints, 'previewWidget').mockResolvedValue({ columns: ['x', 'y'], rows: [['a', '1']], truncated: false, elapsed_ms: 1 })
    open({ widget: line })
    await screen.findByText(/1 row/)
    await userEvent.click(screen.getByRole('combobox', { name: 'Visualization' }))
    await userEvent.click(await screen.findByRole('option', { name: /calendar/ }))
    expect(screen.getByText("This visualization can't draw these rows")).toBeInTheDocument()
  })

  it('saves once, then closes', async () => {
    let resolve!: () => void
    const onSave = vi.fn(() => new Promise<void>((r) => (resolve = r)))
    const { onOpenChange } = open({ widget: line, onSave })
    await userEvent.clear(await screen.findByLabelText('Title'))
    await userEvent.type(screen.getByLabelText('Title'), 'Daily{Enter}')
    const save = screen.getByRole('button', { name: 'Save' })
    await userEvent.click(save)
    expect(save).toBeDisabled()
    await userEvent.click(save)
    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ title: 'Daily', component: 'line' }))
    resolve()
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('keeps the input and shows a refusal in a banner', async () => {
    const onSave = vi.fn().mockRejectedValue(new ApiError(400, 'line needs y (number); columns are x', 'invalid'))
    open({ widget: line, onSave })
    await userEvent.type(await screen.findByLabelText('Title'), '!')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('line needs y')
    expect(screen.getByLabelText('Title')).toHaveValue('Visits!')
  })

  it('closes without asking when nothing changed', async () => {
    const { onOpenChange } = open({ widget: line })
    await userEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(screen.queryByText('Discard changes?')).toBeNull()
  })

  it('asks before discarding changes', async () => {
    const { onOpenChange } = open({ widget: line })
    await userEvent.type(await screen.findByLabelText('Title'), '!')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(await screen.findByText('Discard changes?')).toBeInTheDocument()
    expect(onOpenChange).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('asks before Escape discards changes', async () => {
    const { onOpenChange } = open({ widget: line })
    await userEvent.type(await screen.findByLabelText('Title'), '!')
    await userEvent.keyboard('{Escape}')
    expect(await screen.findByText('Discard changes?')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Keep editing' }))
    expect(onOpenChange).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Title')).toHaveValue('Visits!')
  })

  it('switching component keeps fitting props and the sql', async () => {
    open({ widget: { ...line, props: { format: 'percent', curve: 'step' } } })
    await userEvent.click(await screen.findByRole('combobox', { name: 'Visualization' }))
    await userEvent.click(await screen.findByRole('option', { name: /pie/ }))
    expect(screen.getByLabelText('format')).toHaveValue('percent')
    expect(screen.queryByLabelText('curve')).toBeNull()
    expect(screen.getByLabelText('SQL')).toHaveValue(line.source.content)
  })

  it('a markdown component has no Run and previews the text', async () => {
    open({ widget: { ...line, component: 'markdown', source: { type: 'md', content: '# Hello' }, props: {} } })
    expect(await screen.findByRole('heading', { name: 'Hello' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Run query/ })).toBeNull()
    expect(screen.getByLabelText('Markdown')).toHaveValue('# Hello')
  })
})

describe('shouldKeepOpenOnEscape', () => {
  it('keeps the dialog open while an autocomplete list is showing', () => {
    document.body.innerHTML = '<div class="cm-editor"><div class="cm-content"></div><div class="cm-tooltip-autocomplete"></div></div>'
    expect(shouldKeepOpenOnEscape(document.querySelector('.cm-content'), document)).toBe(true)
    document.body.innerHTML = '<div class="cm-editor"><div class="cm-content"></div></div>'
    expect(shouldKeepOpenOnEscape(document.querySelector('.cm-content'), document)).toBe(false)
    document.body.innerHTML = '<input /><div class="cm-tooltip-autocomplete"></div>'
    expect(shouldKeepOpenOnEscape(document.querySelector('input'), document)).toBe(false)
  })
})

describe('componentIcons', () => {
  it('has an icon for every component', () => {
    expect(Object.keys(widgets).filter((name) => !componentIcons[name])).toEqual([])
  })
})
