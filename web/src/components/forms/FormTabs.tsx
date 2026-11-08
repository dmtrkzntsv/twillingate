import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { PencilLineIcon, PlusIcon, SearchIcon } from 'lucide-react'
import { useNavigate } from 'react-router'
import ReportTabs, { type TabItem } from '@/components/ReportTabs'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { endpoints, type Form, type Project } from '@/lib/api'
import { formAfter, formPath } from '@/lib/forms'
import { writeLastForm } from '@/lib/last-form'
import { formsQuery } from '@/lib/queries'
import { toastError } from '@/lib/toast-error'
import FindPerson from './FindPerson'
import NewFormDialog from './NewFormDialog'

interface Props {
  project: Project
  /** The form on screen (`/forms/:name`); none on `/forms` itself. */
  current?: string
  /** The page's range, kept across the tabs. */
  search: string
}

/** A draft's mark on its tab, said by its tooltip (the tab's label says it to screen readers). */
function DraftMark() {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span aria-hidden data-draft className="inline-flex text-muted-foreground">
          <PencilLineIcon className="size-3.5" />
        </span>
      </TooltipTrigger>
      <TooltipContent>Draft</TooltipContent>
    </Tooltip>
  )
}

function NewCount({ n }: { n: number }) {
  return (
    <span aria-hidden className="min-w-4 rounded-full bg-primary px-1 text-center text-[10px] leading-4 font-medium text-primary-foreground">
      {n > 99 ? '99+' : n}
    </span>
  )
}

/** A form's tab: its name, a draft's mark and its count of new submissions, all in its label. */
function tabOf(f: Form, id: number): TabItem {
  const notes = [f.status === 'draft' && 'draft', f.new_submissions > 0 && `${f.new_submissions.toLocaleString('en-US')} new`].filter(Boolean)
  return {
    id,
    title: f.name,
    icon: f.status === 'draft' ? <DraftMark /> : undefined,
    badge: f.new_submissions > 0 ? <NewCount n={f.new_submissions} /> : undefined,
    label: notes.length > 0 ? `${f.name} (${notes.join(', ')})` : undefined,
  }
}

/**
 * The Forms page's tab row: one tab per live form in the project's order
 * (move_form), dragged to a new place like a dashboard tab; "+" after them
 * opens New form, and the search button Find a person, across every form,
 * in a dialog. A select on phones, as for the dashboards.
 */
export default function FormTabs({ project, current, search }: Props) {
  const projectId = project.project_id
  const q = useQuery(formsQuery(projectId))
  const client = useQueryClient()
  const navigate = useNavigate()
  const [creating, setCreating] = useState(false)
  const [finding, setFinding] = useState(false)
  const forms = q.data?.forms ?? []
  const names = forms.map((f) => f.name)
  // Tabs take numbers: each name's rank in name order, which a move does
  // not change, so the dropped order shows until the list's new order arrives.
  const idOf = new Map([...names].sort().map((n, i) => [n, i + 1]))
  const nameOf = new Map([...idOf].map(([n, i]) => [i, n]))
  const tabs = forms.map((f) => tabOf(f, idOf.get(f.name)!))

  const move = async (id: number, to: number) => {
    const name = nameOf.get(id)!
    try {
      await endpoints.moveForm(projectId, name, formAfter(names, name, to))
    } catch (err) {
      toastError(err)
      return false
    }
    await client.invalidateQueries({ queryKey: ['forms', projectId] })
    return true
  }

  return (
    <>
      <ReportTabs
        tabs={tabs}
        currentId={(current !== undefined && idOf.get(current)) || 0}
        onSelect={(id) => {
          const name = nameOf.get(id)!
          // Remembered on the choice too, as a dashboard tab is, not only once its page has drawn.
          writeLastForm(projectId, name)
          void navigate(formPath(projectId, name, search))
        }}
        sortable
        onMove={move}
        trailing={
          <>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="ghost" size="icon" className="size-8 shrink-0" aria-label="New form" onClick={() => setCreating(true)}>
                  <PlusIcon />
                </Button>
              </TooltipTrigger>
              <TooltipContent>New form</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="ghost" size="icon" className="size-8 shrink-0" aria-label="Find a person" onClick={() => setFinding(true)}>
                  <SearchIcon />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Find a person</TooltipContent>
            </Tooltip>
          </>
        }
      />
      <NewFormDialog open={creating} onOpenChange={setCreating} project={project} />
      <Dialog open={finding} onOpenChange={setFinding}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Find a person</DialogTitle>
            <DialogDescription>Every form's submissions holding the text, archived ones too, for an access or erasure request.</DialogDescription>
          </DialogHeader>
          <FindPerson projectId={projectId} />
        </DialogContent>
      </Dialog>
    </>
  )
}
