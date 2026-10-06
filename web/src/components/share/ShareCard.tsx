import '@fontsource/inter/400.css'
import '@fontsource/inter/600.css'
import { forwardRef, Suspense, useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import IcebergLogo from '@/components/IcebergLogo'
import { widgets } from '@/components/widgets'
import { rangeInWords } from '@/lib/share'
import { CARD, CARD_TYPE, CardMode } from './card-mode'

export interface ShareCardProps {
  component: string
  data: unknown
  props: Record<string, unknown>
  title: string
  /** The project named under the title; left out for a widget that does not follow the page's (shareCaption). */
  projectName?: string
  /** The range's first and last day, YYYY-MM-DD; left out, like the project, when the widget does not follow it. */
  from?: string
  to?: string
  /** Called once the widget has drawn, under Inter, after a lazy one (map, markdown) has loaded. */
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
 * Whether the card's Inter faces have loaded. Only the card uses Inter
 * (font-display: swap), so on a cold page every measurement taken before
 * it arrives (the title's cut, a table's rows, recharts' axis widths) is
 * of the fallback font and wrong once Inter swaps in. `text` names the
 * characters drawn, so each unicode-range subset they need loads too.
 * Without the Font Loading API (jsdom) the card draws at once.
 */
function useCardFonts(text: { bold: string; regular: string }): boolean {
  const [ready, setReady] = useState(() => typeof document.fonts?.load !== 'function')
  useEffect(() => {
    if (ready) return
    let live = true
    Promise.all([document.fonts.load('600 44px Inter', text.bold), document.fonts.load('400 22px Inter', text.regular)])
      // A face that fails to load leaves the fallback font: draw with that rather than never.
      .catch(() => undefined)
      .then(() => live && setReady(true))
    return () => {
      live = false
    }
    // Once per card: the faces stay loaded for the page.
  }, [])
  return ready
}

/**
 * The title cut to what fits in two lines, ending in "…". line-clamp-2
 * does that on screen, but html-to-image copies the computed `display`,
 * which Chromium reports as flow-root, so the image would lose the
 * ellipsis; cutting the text itself keeps it. A word goes at a time (a
 * character, in one long word), each pass before paint. jsdom measures
 * nothing and keeps the whole title.
 */
function useTwoLineTitle(title: string, fontsReady: boolean) {
  const ref = useRef<HTMLHeadingElement>(null)
  const [shown, setShown] = useState(title)
  const [fitted, setFitted] = useState(false)
  const [of, setOf] = useState(title)
  if (of !== title) {
    setOf(title)
    setShown(title)
    setFitted(false)
  }
  useLayoutEffect(() => {
    const el = ref.current
    // Measured only in the card's own font: the fallback's wrapping is not the image's.
    if (!fontsReady || !el || fitted) return
    // Glyphs reach a few pixels past the last line box: overflow is a line's worth.
    const over = el.scrollHeight - el.clientHeight >= parseFloat(getComputedStyle(el).lineHeight) / 2
    const base = shown.endsWith('…') ? shown.slice(0, -1) : shown
    const cut = base.includes(' ') ? base.slice(0, base.lastIndexOf(' ')) : base.slice(0, -1)
    if (over && cut.trim()) setShown(`${cut.trimEnd()}…`)
    else setFitted(true)
  })
  return { ref, shown, fitted }
}

/**
 * A widget laid out afresh as a social card (D4): the title, the project
 * and range in words (whichever the widget follows), the widget drawn at card size in card mode, and the
 * small twillingate mark in its own row below, never over the chart. It
 * takes the console's theme, light or dark, from the `dark` class on <html>.
 */
export const ShareCard = forwardRef<HTMLDivElement, ShareCardProps>(function ShareCard(p, ref) {
  const Component = widgets[p.component]?.default
  const range = p.from && p.to ? rangeInWords(p.from, p.to) : undefined
  const fontsReady = useCardFonts({
    bold: p.title,
    // The data's own text too (labels, markdown), so a non-Latin label's subset is in before the widget draws.
    regular: `${p.projectName ?? ''} · ${range ?? ''} twillingate.dev 0123456789 ${JSON.stringify(p.data ?? '').slice(0, 2000)}`,
  })
  const title = useTwoLineTitle(p.title, fontsReady)
  return (
    <div
      ref={ref}
      data-share-card
      className="share-card flex flex-col bg-background text-foreground"
      style={{ width: CARD.width, height: CARD.height, padding: 56, '--card-type': `${CARD_TYPE}px` } as CSSProperties}
    >
      <h2
        ref={title.ref}
        className="line-clamp-2 text-[44px] leading-[1.15] font-semibold tracking-tight [overflow-wrap:anywhere]"
      >
        {title.shown}
      </h2>
      {/* Only what the widget follows; with neither, no line at all. A long project name gives way to the range. */}
      {(p.projectName || range) && (
        <p data-share-meta className="mt-2 flex min-w-0 text-[22px] whitespace-pre text-muted-foreground">
          {p.projectName && <span className="truncate">{p.projectName}</span>}
          {range && <span className="shrink-0">{p.projectName ? ` · ${range}` : range}</span>}
        </p>
      )}
      <div className="relative mt-6 min-h-0 flex-1">
        {/* The widget measures itself as it mounts (a table's rows, recharts'
            axes), so it mounts only once Inter is in and the title, which
            sets the room left for it, has its final lines. */}
        {fontsReady && title.fitted && (
          <CardMode.Provider value={true}>
            <Suspense fallback={null}>
              {Component && <Component data={p.data as never} props={p.props} />}
              <Ready onReady={p.onReady} />
            </Suspense>
          </CardMode.Provider>
        )}
      </div>
      <div className="mt-4 flex items-center justify-end gap-2 text-[16px] text-muted-foreground opacity-55">
        <IcebergLogo className="size-5" /> twillingate.dev
      </div>
    </div>
  )
})
