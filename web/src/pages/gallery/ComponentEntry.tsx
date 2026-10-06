import { Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { CheckIcon, ChevronDownIcon, CopyIcon, DownloadIcon, TriangleAlertIcon } from 'lucide-react'
import LayoutGrid from '@/components/LayoutGrid'
import { CARD } from '@/components/share/card-mode'
import { OffscreenCard } from '@/components/share/OffscreenCard'
import { ShareCard } from '@/components/share/ShareCard'
import WidgetFrame from '@/components/WidgetFrame'
import WidgetSkeleton from '@/components/WidgetSkeleton'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import type { Example, WidgetModule } from '@/components/widgets/types'
import { useElementWidth } from '@/hooks/use-element-width'
import { captureCard, downloadBlob } from '@/lib/capture'
import { rowsPx } from '@/lib/grid'

/** Tiles: each example as a dashboard draws it. Cards: as its share card (D4). */
export type GalleryView = 'tiles' | 'cards'

/** What the gallery's share cards name as their project and range. */
const CARD_PROJECT = 'example project'
const CARD_FROM = '2026-09-05'
const CARD_TO = '2026-10-04'

/** What to paste into a request to an agent: `add_widget`'s component and props. */
export function addWidgetJson(name: string, example: Example): string {
  return JSON.stringify({ component: name, props: example.props }, null, 2)
}

type CopyState = 'idle' | 'copied' | 'failed'

/** A button that copies `text`; plain http has no clipboard, so failing is a state, not an error. */
function CopyButton({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<CopyState>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const copy = async () => {
    // A second click before the first reset fires must not let the old
    // timer clear a label the new click just set.
    clearTimeout(timer.current)
    try {
      if (!navigator.clipboard) throw new Error('no clipboard')
      await navigator.clipboard.writeText(text)
      setState('copied')
    } catch {
      setState('failed')
    }
    timer.current = setTimeout(() => setState('idle'), 2000)
  }
  return (
    <Button
      variant="outline"
      size="sm"
      // h-auto/whitespace-normal: on a narrow card (a 3-wide widget's copy
      // row, see the comment below), the label wraps instead of forcing
      // the button past the card's edge.
      className="h-auto min-h-7 max-w-full justify-start text-left whitespace-normal"
      onClick={() => void copy()}
    >
      {state === 'copied' ? <CheckIcon /> : state === 'failed' ? <TriangleAlertIcon /> : <CopyIcon />}
      <span aria-live="polite">{state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed' : label}</span>
    </Button>
  )
}

interface PropSchema {
  type?: string
  enum?: unknown[]
}

function propSummary(schema: PropSchema): string {
  if (schema.enum) return schema.enum.map(String).join(' · ')
  return schema.type ?? 'any'
}

/**
 * An example's share card, scaled down to the column's width at its real
 * 1200:630 proportions, with buttons that capture it at full size, from an
 * off-screen copy rather than the scaled preview, and download the PNG.
 *
 * Recharts measures its labels and legend with getBoundingClientRect, which
 * a transform scales, so the preview is drawn at full size first, hidden,
 * and only scaled once drawn; it never redraws after that (memoised, and
 * deaf to the pointer), so the scaled picture is the full-size layout.
 */
function CardPreview({ name, index, example }: { name: string; index: number; example: Example }) {
  const box = useRef<HTMLDivElement>(null)
  const width = useElementWidth(box)
  const [ratio, setRatio] = useState<1 | 2 | null>(null)
  const [failed, setFailed] = useState(false)
  const card = {
    component: name,
    data: example.data,
    props: example.props,
    title: example.title,
    projectName: CARD_PROJECT,
    from: CARD_FROM,
    to: CARD_TO,
  }
  const [drawn, setDrawn] = useState(false)
  const preview = useMemo(
    () => <ShareCard {...card} onReady={() => requestAnimationFrame(() => requestAnimationFrame(() => setDrawn(true)))} />,
    // The example is a fixed fixture: one drawing per card.
    [name, example]
  )
  const capture = async (node: HTMLDivElement) => {
    try {
      const { image, image2x } = await captureCard(node)
      downloadBlob(ratio === 2 ? image2x : image, `${name}-${index + 1}-${ratio}x.png`)
      setFailed(false)
    } catch {
      setFailed(true)
    } finally {
      setRatio(null)
    }
  }
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div
        ref={box}
        role="img"
        aria-label={`${example.title}, as a share card`}
        className="relative w-full overflow-hidden rounded-lg border shadow-sm"
        style={{ aspectRatio: `${CARD.width} / ${CARD.height}` }}
      >
        <div
          className="pointer-events-none absolute top-0 left-0 origin-top-left"
          style={drawn && width > 0 ? { transform: `scale(${width / CARD.width})` } : { visibility: 'hidden' }}
        >
          {preview}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {([1, 2] as const).map((r) => (
          <Button
            key={r}
            variant="outline"
            size="sm"
            data-testid={`card-png-${r}x`}
            disabled={ratio !== null}
            onClick={() => setRatio(r)}
          >
            <DownloadIcon />
            PNG {r}x
          </Button>
        ))}
        {failed && <span className="text-sm text-destructive">Could not capture the card</span>}
      </div>
      {ratio !== null && <OffscreenCard {...card} onNode={(node) => void capture(node)} />}
    </div>
  )
}

/** One component: what it is for, what it reads and takes, and its examples as they render. */
export default function ComponentEntry({
  name,
  module,
  view = 'tiles',
}: {
  name: string
  module: WidgetModule
  view?: GalleryView
}) {
  const { contract, examples } = module
  const props = Object.entries((contract.props as { properties?: Record<string, PropSchema> }).properties ?? {})
  const headingId = `component-${name}-heading`
  return (
    <section
      id={`component-${name}`}
      aria-labelledby={headingId}
      className="flex scroll-mt-16 flex-col gap-3"
      // Read by e2e/gallery.spec.ts to check each card renders at the
      // component's default height, in px, like a dashboard does.
      data-default-height={contract.defaultHeight}
    >
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <h2 id={headingId} className="font-mono text-base font-semibold">
            {name}
          </h2>
          <Badge variant="outline" className="text-muted-foreground">
            {contract.defaultWidth} × {contract.defaultHeight}
          </Badge>
          <CopyButton text={name} label="Copy name" />
        </div>
        <p className="max-w-prose text-sm text-muted-foreground">{contract.description}</p>
        <Collapsible>
          <CollapsibleTrigger asChild>
            <Button variant="link" size="sm" className="group h-6 px-0 text-muted-foreground">
              Contract
              <ChevronDownIcon className="transition-transform group-data-[state=open]:rotate-180" />
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent forceMount className="data-[state=closed]:hidden">
            <dl className="grid max-w-full grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
              <dt className="text-muted-foreground">Inputs</dt>
              <dd className="min-w-0 break-words">
                {contract.inputs.open
                  ? 'any columns, shown in query order'
                  : contract.inputs.columns.map((c) => (
                      <span key={c.name} className="mr-3 inline-flex gap-1">
                        <code>{c.name}</code>
                        <span className="text-muted-foreground">{c.types.join(' or ')}</span>
                        {c.optional && <span className="text-muted-foreground italic">optional</span>}
                      </span>
                    ))}
              </dd>
              <dt className="text-muted-foreground">Props</dt>
              <dd className="min-w-0 break-words">
                {props.length === 0
                  ? 'none'
                  : props.map(([key, schema]) => (
                      <span key={key} className="mr-3 inline-flex gap-1">
                        <code>{key}</code>
                        <span className="text-muted-foreground">{propSummary(schema)}</span>
                      </span>
                    ))}
              </dd>
              <dt className="text-muted-foreground">Accepts</dt>
              <dd>{contract.accepts.join(', ')}</dd>
            </dl>
          </CollapsibleContent>
        </Collapsible>
      </div>
      {view === 'cards' ? (
        <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
          {examples.map((example, i) => (
            <CardPreview key={i} name={name} index={i} example={example} />
          ))}
        </div>
      ) : (
        <LayoutGrid
          cells={examples.map((example, i) => {
            const Component = module.default
            const json = addWidgetJson(name, example)
            return {
              key: i,
              width: contract.defaultWidth,
              // 3 extra rows beyond the widget's own default height: the
              // copy row below it, which stacks on a phone and needs about
              // 116px there.
              height: contract.defaultHeight + 3,
              node: (
                <div className="flex h-full min-w-0 flex-col gap-2">
                  {/* Fixed at rowsPx(defaultHeight), not flex-1, so the card
                      is exactly the height a dashboard draws it at (spec
                      decision 4), with the copy row below taking what's left. */}
                  <div className="min-h-0 shrink-0" style={{ height: rowsPx(contract.defaultHeight) }}>
                    <WidgetFrame title={example.title}>
                      <Suspense fallback={<WidgetSkeleton widget={{ component: name, props: example.props }} />}>
                        <Component data={example.data} props={example.props} />
                      </Suspense>
                    </WidgetFrame>
                  </div>
                  {/* Below sm, a 3-wide card (see lib/grid.ts's span()) is
                      narrower than this button's label, so it stacks instead
                      of forcing the row wider than the viewport. */}
                  <div className="flex min-h-0 min-w-0 flex-1 flex-col items-start gap-2 sm:flex-row">
                    <CopyButton text={json} label="Copy add_widget JSON" />
                    <pre className="max-h-16 min-w-0 flex-1 overflow-y-auto rounded-md bg-muted px-2 py-1 font-mono text-xs break-all whitespace-pre-wrap select-all">
                      {json}
                    </pre>
                  </div>
                </div>
              ),
            }
          })}
        />
      )}
    </section>
  )
}
