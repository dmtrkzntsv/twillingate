import { describe, expect, it } from 'vitest'
import { EditorState } from '@codemirror/state'
import { CompletionContext } from '@codemirror/autocomplete'
import { paramCompletions, sqlNamespace } from './sql-completion'

describe('sql completion', () => {
  it('maps views to their columns', () => {
    expect(sqlNamespace([{ name: 'v_a', columns: ['day', 'n'] }])).toEqual({ v_a: ['day', 'n'] })
  })
  it('offers the three parameters after a colon', () => {
    const state = EditorState.create({ doc: 'WHERE project_id = :pr' })
    const res = paramCompletions(new CompletionContext(state, state.doc.length, false))
    expect(res?.from).toBe(19)
    expect(res?.options.map((o) => o.label)).toEqual([':project', ':from', ':to'])
  })
  it('stays quiet without a colon', () => {
    const state = EditorState.create({ doc: 'SELECT pr' })
    expect(paramCompletions(new CompletionContext(state, state.doc.length, false))).toBeNull()
  })
})
