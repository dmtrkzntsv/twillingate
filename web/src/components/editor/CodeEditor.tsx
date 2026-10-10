import { useEffect, useRef, useState } from 'react'
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { markdown } from '@codemirror/lang-markdown'
import { sql, SQLite } from '@codemirror/lang-sql'
import { bracketMatching, HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'
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
  /** The views and columns sql completion offers. Compared by content, so a new array with the same views does not rebuild the editor's language. */
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

// Token colors are CSS variables (--code-*, index.css), so the light and dark
// themes each get colors with contrast on their own background.
const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.modifier], color: 'var(--code-keyword)' },
  { tag: [t.string, t.special(t.string)], color: 'var(--code-string)' },
  { tag: [t.number, t.bool, t.null], color: 'var(--code-number)' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: 'var(--code-comment)', fontStyle: 'italic' },
  { tag: [t.typeName, t.standard(t.name), t.className], color: 'var(--code-type)' },
  { tag: [t.name, t.propertyName, t.variableName], color: 'var(--code-name)' },
  { tag: [t.heading, t.strong], color: 'var(--code-keyword)', fontWeight: 'bold' },
  { tag: t.emphasis, fontStyle: 'italic' },
  { tag: [t.link, t.url], color: 'var(--code-string)', textDecoration: 'underline' },
  { tag: t.monospace, color: 'var(--code-number)' },
])

/**
 * CodeMirror 6 for a widget's SQL (SQLite, completion of the views, their
 * columns and the three parameters) or its markdown. Mod-Enter runs.
 * Uncontrolled inside: `value` seeds it and replaces the text only when it
 * differs from what the editor holds (a reset), so the text typed in does not
 * come back through the prop as a second change.
 */
export default function CodeEditor({ language, value, onChange, onRun, tables, label }: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const [lang] = useState(() => new Compartment())
  const latest = useRef({ onChange, onRun, tables })
  latest.current = { onChange, onRun, tables }
  // The language is rebuilt when the views change, not when the array does.
  const tablesKey = JSON.stringify(tables ?? [])

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
          syntaxHighlighting(highlight),
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
          lang.of([]),
        ],
      }),
    })
    view.current = v
    return () => v.destroy()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- created once; value, language and tables follow below
  }, [])

  useEffect(() => {
    view.current?.dispatch({
      effects: lang.reconfigure(
        language === 'sql'
          ? [
              sql({ dialect: SQLite, schema: sqlNamespace(latest.current.tables ?? []), upperCaseKeywords: true }),
              SQLite.language.data.of({ autocomplete: paramCompletions }),
            ]
          : markdown(),
      ),
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- tables is read through latest; tablesKey is its content
  }, [language, tablesKey, lang])

  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
    }
  }, [value])

  return <div ref={host} data-slot="code-editor" className="h-full min-h-40 min-w-0 overflow-hidden rounded-md border border-input focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50" />
}
