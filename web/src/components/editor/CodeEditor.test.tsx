import { describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { EditorView, runScopeHandlers } from '@codemirror/view'
import { CompletionContext, completionStatus, startCompletion } from '@codemirror/autocomplete'
import CodeEditor from './CodeEditor'

function viewOf(container: HTMLElement) {
  const v = EditorView.findFromDOM(container.querySelector('.cm-editor') as HTMLElement)
  if (!v) throw new Error('no editor mounted')
  return v
}

describe('CodeEditor', () => {
  it('labels the editable area', () => {
    const { container } = render(<CodeEditor language="sql" label="SQL" value="SELECT 1" onChange={() => {}} />)
    expect(container.querySelector('.cm-content')?.getAttribute('aria-label')).toBe('SQL')
  })

  it('reports typing through onChange', () => {
    const onChange = vi.fn()
    const { container } = render(<CodeEditor language="markdown" label="Markdown" value="" onChange={onChange} />)
    viewOf(container).dispatch({ changes: { from: 0, insert: '# hi' } })
    expect(onChange).toHaveBeenLastCalledWith('# hi')
  })

  it('replaces the text when value changes, reporting it once', () => {
    const onChange = vi.fn()
    const { container, rerender } = render(<CodeEditor language="sql" label="SQL" value="a" onChange={onChange} />)
    rerender(<CodeEditor language="sql" label="SQL" value="b" onChange={onChange} />)
    expect(viewOf(container).state.doc.toString()).toBe('b')
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith('b')
  })

  it('offers the tables, their columns and the parameters', async () => {
    const tables = [{ name: 'v_hits', columns: ['day', 'visitors'] }]
    const { container } = render(<CodeEditor language="sql" label="SQL" value="SELECT * FROM v_h" onChange={() => {}} tables={tables} />)
    const state = viewOf(container).state
    const sources = state.languageDataAt<(c: CompletionContext) => unknown>('autocomplete', state.doc.length)
    const labels = async (doc: string) => {
      const s = state.update({ changes: { from: 0, to: state.doc.length, insert: doc } }).state
      const out: string[] = []
      for (const src of s.languageDataAt<(c: CompletionContext) => unknown>('autocomplete', doc.length)) {
        const r = (await src(new CompletionContext(s, doc.length, true))) as { options: { label: string }[] } | null
        out.push(...(r?.options.map((o) => o.label) ?? []))
      }
      return out
    }
    expect(sources.length).toBeGreaterThan(1)
    expect(await labels('SELECT * FROM v_h')).toContain('v_hits')
    expect(await labels('SELECT v_hits.')).toContain('visitors')
    expect(await labels('WHERE project_id = :pr')).toContain(':project')
  })

  it('keeps its configuration when tables is an equal new array', () => {
    const t = () => [{ name: 'v_hits', columns: ['day'] }]
    const { container, rerender } = render(<CodeEditor language="sql" label="SQL" value="" onChange={() => {}} tables={t()} />)
    const dispatch = vi.spyOn(viewOf(container), 'dispatch')
    rerender(<CodeEditor language="sql" label="SQL" value="" onChange={() => {}} tables={t()} />)
    expect(dispatch).not.toHaveBeenCalled()
    rerender(<CodeEditor language="sql" label="SQL" value="" onChange={() => {}} tables={[{ name: 'v_hits', columns: ['day', 'n'] }]} />)
    expect(dispatch).toHaveBeenCalledTimes(1)
  })

  it('colors its highlighting from css tokens', () => {
    const { container } = render(<CodeEditor language="sql" label="SQL" value="SELECT 1 -- hi" onChange={() => {}} />)
    const css = Array.from(document.querySelectorAll('style')).map((s) => s.textContent).join('\n')
    expect(css).toContain('var(--code-keyword')
    expect(css).not.toMatch(/#708|#a11/)
    expect(container.querySelector('.cm-editor')).not.toBeNull()
  })

  it('runs on Mod-Enter', () => {
    const onRun = vi.fn()
    const { container } = render(<CodeEditor language="sql" label="SQL" value="SELECT 1" onChange={() => {}} onRun={onRun} />)
    const v = viewOf(container)
    for (const mod of [{ ctrlKey: true }, { metaKey: true }]) {
      runScopeHandlers(v, new KeyboardEvent('keydown', { key: 'Enter', ...mod }), 'editor')
    }
    expect(onRun).toHaveBeenCalled()
  })

  it('closes its completion list on an Escape a dialog around it already took', async () => {
    const tables = [{ name: 'v_hits', columns: ['day'] }]
    const { container } = render(<CodeEditor language="sql" label="SQL" value="SELECT * FROM v_h" onChange={() => {}} tables={tables} />)
    const v = viewOf(container)
    v.dispatch({ selection: { anchor: v.state.doc.length } })
    startCompletion(v)
    await waitFor(() => expect(completionStatus(v.state)).toBe('active'))
    // A radix dialog listens on the document in the capture phase and
    // prevents the default, which keeps the editor's own keymap from it.
    const take = (e: Event) => e.preventDefault()
    document.addEventListener('keydown', take, { capture: true })
    v.contentDOM.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', keyCode: 27, bubbles: true, cancelable: true }))
    document.removeEventListener('keydown', take, { capture: true })
    expect(completionStatus(v.state)).toBeNull()
  })
})
