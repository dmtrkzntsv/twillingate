import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { CheckIcon, CodeIcon, ExternalLinkIcon, LinkIcon, TriangleAlertIcon } from 'lucide-react'
import { Link } from 'react-router'
import CopyButton from '@/components/projects/CopyButton'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCopy } from '@/hooks/use-copy'
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
 * on one click uploads the card's two PNGs to make the link and copies it.
 * The dialog keeps its layout: the button then copies the link again, the
 * embed code and Open join it, and the archive choice changes the made
 * link's date. Where the clipboard fails (plain http), the link and embed
 * code show as text to select. The card is drawn once per opening, out of
 * sight, and captured from there (never from the preview).
 */
export function ShareDialog({ open, onOpenChange, widget, data, share }: Props) {
  const actions = useWidgetShareActions()
  const [captured, setCaptured] = useState<Captured>()
  const [failed, setFailed] = useState(false)
  const [archiveAfter, setArchiveAfter] = useState<ArchiveAfter>(DEFAULT_ARCHIVE_AFTER)
  const [created, setCreated] = useState<WidgetShare>()
  // A copy failed: the link and embed code show as text instead.
  const [manual, setManual] = useState(false)
  const { state: copied, copy } = useCopy()
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
      setManual(false)
    }
    onOpenChange(o)
  }

  const make = async (mine: number): Promise<string | undefined> => {
    if (!captured) return undefined
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
    const out = await actions.create(form)
    // Closed while it was sent: the link belongs to an opening that is over, and is not copied.
    if (!out || mine !== opening.current) return undefined
    setCreated(out)
    return out.url
  }

  /** Makes the link and copies it, or copies the made one. The copy starts in the click, while the link is still on its way. */
  const copyLink = () => {
    const mine = opening.current
    void copy(created ? created.url : make(mine)).then((out) => {
      if (out === 'failed' && mine === opening.current) setManual(true)
    })
  }

  const changeArchiveAfter = async (v: ArchiveAfter) => {
    if (!created) return setArchiveAfter(v)
    if (await actions.setArchiveAfter(created.id, v)) setArchiveAfter(v)
  }

  const linkLabel = copied === 'copied' ? 'Copied' : copied === 'failed' ? "Couldn't copy" : created ? 'Copy link' : 'Create and copy link'
  const linkIcon = copied === 'copied' ? <CheckIcon /> : copied === 'failed' ? <TriangleAlertIcon /> : <LinkIcon />

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
            <DialogDescription>Anyone with the link sees this picture. It never updates.</DialogDescription>
          </DialogHeader>
          {preview ? (
            <img className="w-full rounded-md border" src={preview} alt="Preview of the share card" />
          ) : (
            <div className="flex aspect-[1200/630] w-full items-center justify-center rounded-md border text-sm text-muted-foreground">
              {failed ? <span className="text-destructive">Could not capture the card</span> : 'Drawing the card…'}
            </div>
          )}
          {created && manual && (
            <div className="flex flex-col gap-2">
              <Input readOnly autoFocus aria-label="Share link" value={created.url} onFocus={(e) => e.target.select()} />
              <Input readOnly aria-label="Embed code" value={embedCode(created)} onFocus={(e) => e.target.select()} className="font-mono text-xs" />
            </div>
          )}
          <div className="flex flex-wrap items-center gap-x-4 gap-y-3">
            <div className="flex items-center gap-2">
              <Label htmlFor="share-archive-after" className="whitespace-nowrap">
                Archive after
              </Label>
              <ArchiveAfterSelect id="share-archive-after" value={archiveAfter} disabled={actions.pending} onChange={(v) => void changeArchiveAfter(v)} />
            </div>
            <div className="ml-auto flex items-center gap-1">
              {created && (
                <>
                  <CopyButton value={embedCode(created)} label="Copy embed code" icon={<CodeIcon />} onFail={() => setManual(true)} />
                  <Button asChild variant="ghost" size="icon">
                    <a href={created.url} target="_blank" rel="noopener" aria-label="Open" title="Open">
                      <ExternalLinkIcon />
                    </a>
                  </Button>
                </>
              )}
              <Button type="button" disabled={!captured || actions.pending} onClick={copyLink}>
                {linkIcon}
                {linkLabel}
              </Button>
            </div>
          </div>
          {otherCount > 0 && (
            <Link
              to={`/shares?widget=${widget.widget_id}`}
              title="This widget's other links, on the Shares page"
              className="self-start text-xs text-muted-foreground underline underline-offset-2"
            >
              {otherCount} other {otherCount === 1 ? 'link' : 'links'}
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
