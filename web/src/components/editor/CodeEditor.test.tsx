import { describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import { EditorView, runScopeHandlers } from '@codemirror/view'
import { CompletionContext } from '@codemirror/autocomplete'
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

  it('replaces the text when value changes, without echoing it back', () => {
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

  it('runs on Mod-Enter', () => {
    const onRun = vi.fn()
    const { container } = render(<CodeEditor language="sql" label="SQL" value="SELECT 1" onChange={() => {}} onRun={onRun} />)
    const v = viewOf(container)
    for (const mod of [{ ctrlKey: true }, { metaKey: true }]) {
      runScopeHandlers(v, new KeyboardEvent('keydown', { key: 'Enter', ...mod }), 'editor')
    }
    expect(onRun).toHaveBeenCalled()
  })
})
