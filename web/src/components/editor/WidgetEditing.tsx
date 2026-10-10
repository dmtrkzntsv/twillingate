import { createContext, lazy, Suspense, useContext, useMemo, useState, type ReactNode } from 'react'
import { PlusIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useWidgetEdits } from '@/hooks/use-widget-edits'
import type { Widget } from '@/lib/api'
import type { EditorDefaults } from './WidgetEditor'

/** What a user dashboard's page lets its header, empty state and cards do to its widgets. */
export interface WidgetEditing {
  /** Opens the editor on the widget, or on a new one. */
  edit(widget?: Widget): void
  /** Retitles the widget; resolves true when saved or unchanged, false after the refusal's toast. */
  rename(widget: Widget, title: string): Promise<boolean>
  /** Archives the widget at once, with an Undo toast. */
  archive(widget: Widget): void
}

interface Props {
  dashboardId: number
  /**
   * Whether the page may edit its widgets now. The provider stays mounted
   * either way, so the grid under it keeps its state as the page freezes.
   */
  enabled: boolean
  defaults: EditorDefaults
  children: ReactNode
}

const Ctx = createContext<WidgetEditing | null>(null)
// The editor and what it loads (CodeMirror, the schema) wait for the first open.
const WidgetEditor = lazy(() => import('./WidgetEditor'))

/**
 * Makes a user dashboard's widgets editable from the page (spec D8): the
 * header's Add widget, the empty state's, and each card's Edit…, Rename
 * and Archive read this; outside it, or while it is not enabled (a system
 * dashboard, reporting dev, a page being left), they are not shown. The
 * editor loads on first open and saves to the dashboard it was opened on.
 */
export function WidgetEditingProvider({ dashboardId, enabled, defaults, children }: Props) {
  const edits = useWidgetEdits()
  // Kept while the dialog closes, so it closes on what it showed.
  const [editing, setEditing] = useState<{ open: boolean; dashboardId: number; widget?: Widget }>()
  const value = useMemo<WidgetEditing | null>(
    () =>
      enabled
        ? {
            edit: (widget) => setEditing({ open: true, dashboardId, widget }),
            rename: (widget, title) => edits.rename(widget, title),
            archive: (widget) => void edits.archive(widget),
          }
        : null,
    [enabled, dashboardId, edits]
  )
  return (
    <Ctx.Provider value={value}>
      {children}
      {editing && (
        <Suspense fallback={null}>
          <WidgetEditor
            open={editing.open}
            onOpenChange={(open) => !open && setEditing({ ...editing, open: false })}
            widget={editing.widget}
            defaults={defaults}
            onSave={(draft) => (editing.widget ? edits.update(editing.widget, draft) : edits.add(editing.dashboardId, draft))}
          />
        </Suspense>
      )}
    </Ctx.Provider>
  )
}

/** The page's widget edits; null outside an editable dashboard. */
export function useWidgetEditing(): WidgetEditing | null {
  return useContext(Ctx)
}

/** Opens the editor on a new widget; nothing outside an editable dashboard. */
export function AddWidgetButton() {
  const editing = useWidgetEditing()
  if (!editing) return null
  return (
    <Button variant="outline" size="sm" onClick={() => editing.edit()}>
      <PlusIcon />
      Add widget
    </Button>
  )
}
