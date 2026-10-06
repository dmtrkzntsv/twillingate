import '@fontsource/inter/400.css'
import '@fontsource/inter/600.css'
import { forwardRef, Suspense, useEffect, useLayoutEffect, useRef, useState } from 'react'
import IcebergLogo from '@/components/IcebergLogo'
import { widgets } from '@/components/widgets'
import { rangeInWords } from '@/lib/share'
import { CARD, CardMode } from './card-mode'

export interface ShareCardProps {
  component: string
  data: unknown
  props: Record<string, unknown>
  title: string
  projectName: string
  /** The range's first and last day, YYYY-MM-DD. */
  from: string
  to: string
  /** Called once the widget has drawn, after a lazy one (map, markdown) has loaded. */
  onReady?: () => void
}

/** Mounts with the widget beside it in one Suspense boundary, so its effect runs once the widget is in. */
function Ready({ onReady }: { onReady?: () => void }) {
  useEffect(() => {
    onReady?.()
  }, [])
  return null
}

/**
 * The title cut to what fits in two lines, ending in "…". line-clamp-2
 * does that on screen, but html-to-image copies the computed `display`,
 * which Chromium reports as flow-root, so the image would lose the
 * ellipsis; cutting the text itself keeps it. A word goes at a time (a
 * character, in one long word), each pass before paint. jsdom measures
 * nothing and keeps the whole title.
 */
function useTwoLineTitle(title: string) {
  const ref = useRef<HTMLHeadingElement>(null)
  const [shown, setShown] = useState(title)
  const [of, setOf] = useState(title)
  if (of !== title) {
    setOf(title)
    setShown(title)
  }
  useLayoutEffect(() => {
    const el = ref.current
    // Glyphs reach a few pixels past the last line box: overflow is a line's worth.
    if (!el || !(el.scrollHeight - el.clientHeight >= parseFloat(getComputedStyle(el).lineHeight) / 2)) return
    const base = shown.endsWith('…') ? shown.slice(0, -1) : shown
    const cut = base.includes(' ') ? base.slice(0, base.lastIndexOf(' ')) : base.slice(0, -1)
    if (cut.trim()) setShown(`${cut.trimEnd()}…`)
  })
  return { ref, shown }
}

/**
 * A widget laid out afresh as a social card (D4): the title, the project
 * and range in words, the widget drawn at card size in card mode, and the
 * small twillingate mark in its own row below, never over the chart. It
 * takes the console's theme, light or dark, from the `dark` class on <html>.
 */
export const ShareCard = forwardRef<HTMLDivElement, ShareCardProps>(function ShareCard(p, ref) {
  const Component = widgets[p.component]?.default
  const title = useTwoLineTitle(p.title)
  return (
    <div
      ref={ref}
      data-share-card
      className="share-card flex flex-col bg-background text-foreground"
      style={{ width: CARD.width, height: CARD.height, padding: 56 }}
    >
      <h2
        ref={title.ref}
        className="line-clamp-2 text-[44px] leading-[1.15] font-semibold tracking-tight [overflow-wrap:anywhere]"
      >
        {title.shown}
      </h2>
      {/* A long project name gives way; the range always shows. */}
      <p data-share-meta className="mt-2 flex min-w-0 text-[22px] whitespace-pre text-muted-foreground">
        <span className="truncate">{p.projectName}</span>
        <span className="shrink-0"> · {rangeInWords(p.from, p.to)}</span>
      </p>
      <div className="relative mt-6 min-h-0 flex-1">
        <CardMode.Provider value={true}>
          <Suspense fallback={null}>
            {Component && <Component data={p.data as never} props={p.props} />}
            <Ready onReady={p.onReady} />
          </Suspense>
        </CardMode.Provider>
      </div>
      <div className="mt-4 flex items-center justify-end gap-2 text-[16px] text-muted-foreground opacity-55">
        <IcebergLogo className="size-5" /> twillingate.dev
      </div>
    </div>
  )
})
