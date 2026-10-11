import { createContext, lazy, Suspense, useContext, useMemo, useState, type ReactNode } from 'react'
import { PlusIcon } from 'lucide-react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { useWidgetEdits } from '@/hooks/use-widget-edits'
import type { DashboardInfo, Widget } from '@/lib/api'
import { copyTargets } from '@/lib/arrange'
import CopyWidgetDialog from './CopyWidgetDialog'
import type { EditorDefaults } from './WidgetEditor'

/** What a user dashboard's page lets its header, empty state and cards do to its widgets. */
export interface WidgetEditing {
  /** Opens the editor on the widget, or on a new one. */
  edit(widget?: Widget): void
  /** Retitles the widget; resolves true when saved or unchanged, false after the refusal's toast. */
  rename(widget: Widget, title: string): Promise<boolean>
  /** Archives the widget at once, with an Undo toast. */
  archive(widget: Widget): void
  /** Copies the widget right after itself. */
  duplicate(widget: Widget): void
}

/** What a card of any dashboard, a built-in one included, offers to copy it elsewhere. */
export interface WidgetCopying {
  /** Opens "Copy to dashboard…" on the widget. */
  copy(widget: Widget): void
}

interface Props {
  dashboardId: number
  /**
   * Whether the page may edit its widgets now. The provider stays mounted
   * either way, so the grid under it keeps its state as the page freezes.
   */
  enabled: boolean
  defaults: EditorDefaults
  /**
   * Every dashboard, for "Copy to dashboard…" on each card; absent where
   * the page writes nothing (reporting dev, a page being left, an archived
   * dashboard).
   */
  copyTo?: DashboardInfo[]
  children: ReactNode
}

const Ctx = createContext<WidgetEditing | null>(null)
const CopyCtx = createContext<WidgetCopying | null>(null)
// The editor and what it loads (CodeMirror, the schema) wait for the first open.
const WidgetEditor = lazy(() => import('./WidgetEditor'))

/**
 * Makes a user dashboard's widgets editable from the page (spec D8): the
 * header's Add widget, the empty state's, and each card's Edit…, Rename,
 * Duplicate and Archive read this; outside it, or while it is not enabled
 * (a system dashboard, reporting dev, a page being left), they are not
 * shown. The editor loads on first open and saves to the dashboard it was
 * opened on. Given `copyTo`, each card also offers "Copy to dashboard…",
 * on a built-in dashboard too, whose copy stays on this page.
 */
export function WidgetEditingProvider({ dashboardId, enabled, defaults, copyTo, children }: Props) {
  const edits = useWidgetEdits()
  const navigate = useNavigate()
  // Kept while the dialog closes, so it closes on what it showed.
  const [editing, setEditing] = useState<{ open: boolean; dashboardId: number; widget?: Widget }>()
  const value = useMemo<WidgetEditing | null>(
    () =>
      enabled
        ? {
            edit: (widget) => setEditing({ open: true, dashboardId, widget }),
            rename: (widget, title) => edits.rename(widget, title),
            archive: (widget) => void edits.archive(widget),
            duplicate: (widget) => void edits.duplicate(widget),
          }
        : null,
    [enabled, dashboardId, edits]
  )
  // Kept while the dialog closes, so it closes on what it showed.
  const [copying, setCopying] = useState<{ open: boolean; widget: Widget }>()
  const canCopy = copyTo !== undefined
  const copyValue = useMemo<WidgetCopying | null>(() => (canCopy ? { copy: (widget) => setCopying({ open: true, widget }) } : null), [canCopy])
  const copied = (dashboard: { dashboard_id: number; title: string }) =>
    toast(`Copied to ${dashboard.title}`, { action: { label: 'Open', onClick: () => navigate(`/dashboards/${dashboard.dashboard_id}`) } })
  return (
    <Ctx.Provider value={value}>
      <CopyCtx.Provider value={copyValue}>{children}</CopyCtx.Provider>
      {copying && copyTo && (
        <CopyWidgetDialog
          open={copying.open}
          onOpenChange={(open) => !open && setCopying({ ...copying, open: false })}
          label={copying.widget.title ?? copying.widget.name}
          targets={copyTargets(copyTo, copying.widget.dashboard_id)}
          onCopy={async (dashboard) => {
            const ok = await edits.copy(copying.widget, dashboard.dashboard_id)
            if (ok) copied(dashboard)
            return ok
          }}
          onCreate={async (title) => {
            const id = await edits.copyToNew(copying.widget, title)
            if (id !== null) copied({ dashboard_id: id, title })
            return id !== null
          }}
        />
      )}
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

/** "Copy to dashboard…"; null where the page writes nothing. */
export function useWidgetCopying(): WidgetCopying | null {
  return useContext(CopyCtx)
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
