import { useEffect, useRef } from 'react'
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { markdown } from '@codemirror/lang-markdown'
import { sql, SQLite } from '@codemirror/lang-sql'
import { bracketMatching, defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { Compartment, EditorState, Prec } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, placeholder } from '@codemirror/view'
import { paramCompletions, sqlNamespace } from '@/lib/sql-completion'
import type { SchemaTable } from '@/lib/schema-views'

export interface CodeEditorProps {
  language: 'sql' | 'markdown'
  value: string
  onChange(next: string): void
  /** Called on Mod-Enter. */
  onRun?: () => void
  /** The views and columns sql completion offers. */
  tables?: SchemaTable[]
  /** The aria-label of the editable area. */
  label: string
}

// Colors from the app's tokens, so light and dark both follow the theme.
const theme = EditorView.theme({
  '&': { fontSize: '13px', backgroundColor: 'var(--background)', color: 'var(--foreground)', height: '100%' },
  '.cm-content': { fontFamily: 'var(--font-mono, ui-monospace, monospace)', caretColor: 'var(--foreground)' },
  '.cm-gutters': { backgroundColor: 'var(--muted)', color: 'var(--muted-foreground)', border: 'none' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { overflow: 'auto' },
  '.cm-tooltip': { backgroundColor: 'var(--popover)', color: 'var(--popover-foreground)', border: '1px solid var(--border)' },
})

/**
 * CodeMirror 6 for a widget's SQL (SQLite, completion of the views, their
 * columns and the three parameters) or its markdown. Mod-Enter runs.
 * Uncontrolled inside: `value` seeds it and replaces the text only when it
 * differs from what the editor holds (a reset), so typing never loops.
 */
export default function CodeEditor({ language, value, onChange, onRun, tables, label }: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const lang = useRef(new Compartment())
  const latest = useRef({ onChange, onRun })
  latest.current = { onChange, onRun }

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          history(),
          bracketMatching(),
          closeBrackets(),
          syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
          autocompletion(),
          keymap.of([...closeBracketsKeymap, ...completionKeymap, ...defaultKeymap, ...historyKeymap]),
          Prec.highest(keymap.of([{ key: 'Mod-Enter', run: () => (latest.current.onRun?.(), true) }])),
          EditorView.lineWrapping,
          theme,
          placeholder(language === 'sql' ? 'SELECT …' : '# Notes'),
          EditorView.contentAttributes.of({ 'aria-label': label }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) latest.current.onChange(u.state.doc.toString())
          }),
          lang.current.of([]),
        ],
      }),
    })
    view.current = v
    return () => v.destroy()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- created once; value, language and tables follow below
  }, [])

  useEffect(() => {
    view.current?.dispatch({
      effects: lang.current.reconfigure(
        language === 'sql'
          ? [
              sql({ dialect: SQLite, schema: sqlNamespace(tables ?? []), upperCaseKeywords: true }),
              SQLite.language.data.of({ autocomplete: paramCompletions }),
            ]
          : markdown(),
      ),
    })
  }, [language, tables])

  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
    }
  }, [value])

  return <div ref={host} data-slot="code-editor" className="h-full min-h-40 min-w-0 overflow-hidden rounded-md border" />
}
