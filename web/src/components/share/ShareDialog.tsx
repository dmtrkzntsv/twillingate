import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import CopyButton from '@/components/projects/CopyButton'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useWidgetShareActions } from '@/hooks/use-widget-share-actions'
import type { ArchiveAfter, Widget, WidgetShare } from '@/lib/api'
import { captureCard } from '@/lib/capture'
import { widgetSharesQuery } from '@/lib/queries'
import { DEFAULT_ARCHIVE_AFTER, embedCode, shareCaption, type ShareContext } from '@/lib/share'
import { ArchiveAfterSelect } from './ArchiveAfterSelect'
import { OffscreenCard } from './OffscreenCard'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  widget: Widget
  /** The widget's current answer, drawn on the card. */
  data: unknown
  /** With its project: only a page that shows one offers Share…. */
  share: ShareContext & { project: NonNullable<ShareContext['project']> }
}

type Captured = { image: Blob; image2x: Blob }

const png = (blob: Blob) => new File([blob], 'image.png', { type: 'image/png' })

/**
 * Previews the widget's share card, asks when the link archives itself, and
 * uploads the card's two PNGs to make the link; then shows the link with
 * its copy and open actions. The card is drawn once per opening, out of
 * sight, and captured from there (never from the preview).
 */
export function ShareDialog({ open, onOpenChange, widget, data, share }: Props) {
  const actions = useWidgetShareActions()
  const [captured, setCaptured] = useState<Captured>()
  const [failed, setFailed] = useState(false)
  const [archiveAfter, setArchiveAfter] = useState<ArchiveAfter>(DEFAULT_ARCHIVE_AFTER)
  const [created, setCreated] = useState<WidgetShare>()
  const [preview, setPreview] = useState<string>()
  // Counts openings, so a capture that finishes after the dialog closed is dropped.
  const opening = useRef(0)

  // The preview's object URL lives as long as its capture.
  useEffect(() => {
    if (!captured) return
    const url = URL.createObjectURL(captured.image2x)
    setPreview(url)
    return () => {
      URL.revokeObjectURL(url)
      setPreview(undefined)
    }
  }, [captured])

  const caption = shareCaption(widget, share)

  const others = useQuery({
    ...widgetSharesQuery({ widget_id: widget.widget_id, state: 'live' }),
    enabled: open,
  })
  const otherCount = (others.data?.shares ?? []).filter((s) => s.id !== created?.id).length

  const close = (o: boolean) => {
    if (!o) {
      opening.current++
      setCaptured(undefined)
      setFailed(false)
      setArchiveAfter(DEFAULT_ARCHIVE_AFTER)
      setCreated(undefined)
    }
    onOpenChange(o)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!captured) return
    const form = new FormData()
    form.set('widget_id', String(widget.widget_id))
    form.set('project_id', String(share.project.id))
    form.set('from', share.from)
    form.set('to', share.to)
    form.set('archive_after', archiveAfter)
    // The page names only what the card does.
    form.set('caption_project', caption.projectName !== undefined ? '1' : '0')
    form.set('caption_range', caption.from !== undefined ? '1' : '0')
    form.set('image', png(captured.image))
    form.set('image_2x', png(captured.image2x))
    const mine = opening.current
    const out = await actions.create(form)
    // Closed while it was sent: the link belongs to an opening that is over.
    if (out && mine === opening.current) setCreated(out)
  }

  const capture = async (node: HTMLDivElement) => {
    const mine = opening.current
    try {
      const out = await captureCard(node)
      if (mine === opening.current) setCaptured(out)
    } catch {
      if (mine === opening.current) setFailed(true)
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={close}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Share widget</DialogTitle>
            <DialogDescription>
              Anyone with the link sees this picture of the widget, as it is now. It never updates, and the link archives itself on the date
              you pick.
            </DialogDescription>
          </DialogHeader>
          {preview ? (
            <img className="w-full rounded-md border" src={preview} alt="Preview of the share card" />
          ) : (
            <div className="flex aspect-[1200/630] w-full items-center justify-center rounded-md border text-sm text-muted-foreground">
              {failed ? <span className="text-destructive">Could not capture the card</span> : 'Drawing the card…'}
            </div>
          )}
          {created ? (
            <div className="flex flex-col gap-3">
              <div className="flex items-center gap-2">
                <Input readOnly aria-label="Share link" value={created.url} onFocus={(e) => e.target.select()} className="min-w-0 flex-1" />
                <CopyButton value={created.url} label="Copy link" />
              </div>
              <div className="flex items-center gap-2">
                <Input
                  readOnly
                  aria-label="Embed code"
                  value={embedCode(created)}
                  onFocus={(e) => e.target.select()}
                  className="min-w-0 flex-1 font-mono text-xs"
                />
                <CopyButton value={embedCode(created)} label="Copy embed code" />
              </div>
              <DialogFooter>
                <Button asChild variant="outline">
                  <a href={created.url} target="_blank" rel="noopener">
                    Open
                  </a>
                </Button>
              </DialogFooter>
            </div>
          ) : (
            <form onSubmit={submit} className="flex flex-col gap-4">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="share-archive-after">Archive after</Label>
                <ArchiveAfterSelect id="share-archive-after" value={archiveAfter} onChange={setArchiveAfter} />
                <p className="text-xs text-muted-foreground">Feeds keep the preview they already fetched.</p>
              </div>
              <DialogFooter>
                <Button type="submit" disabled={!captured || actions.pending}>
                  Create link
                </Button>
              </DialogFooter>
            </form>
          )}
          {otherCount > 0 && (
            <Link to={`/shares?widget=${widget.widget_id}`} className="text-sm text-muted-foreground underline underline-offset-2">
              This widget has {otherCount} other {otherCount === 1 ? 'link' : 'links'}
            </Link>
          )}
        </DialogContent>
      </Dialog>
      {open && !captured && !failed && (
        <OffscreenCard
          component={widget.component ?? ''}
          data={data}
          props={widget.props}
          title={widget.title ?? widget.name}
          {...caption}
          onNode={(node) => void capture(node)}
        />
      )}
    </>
  )
}
