import { toBlob } from 'html-to-image'
import { CARD } from '@/components/share/card-mode'

/** Resolves after `n` animation frames: recharts lays out on the frame after mount. */
const frames = (n: number) =>
  new Promise<void>((resolve) => {
    const step = (k: number) => (k ? requestAnimationFrame(() => step(k - 1)) : resolve())
    step(n)
  })

/** What an SVG mark takes from CSS rather than its attributes: paint, type and visibility. */
const SVG_STYLES = [
  'fill',
  'fill-opacity',
  'stroke',
  'stroke-width',
  'stroke-opacity',
  'stroke-dasharray',
  'stroke-linecap',
  'stroke-linejoin',
  'opacity',
  'color',
  'font-family',
  'font-size',
  'font-weight',
  'letter-spacing',
  'text-anchor',
  'dominant-baseline',
  'visibility',
]

/**
 * Writes each SVG descendant's computed paint and type into its own style.
 * html-to-image copies an <svg> wholesale and inlines the computed style of
 * the <svg> alone, so a mark styled by a class (the chart grid's faint
 * stroke, the card's larger axis labels) would lose it in the image.
 */
export function inlineSvgStyles(root: HTMLElement): void {
  for (const el of root.querySelectorAll<SVGElement>('svg *')) {
    const computed = getComputedStyle(el)
    for (const name of SVG_STYLES) el.style.setProperty(name, computed.getPropertyValue(name))
  }
}

/**
 * A share card (D4) as its two PNGs: 1200×630 for og:image and 2400×1260
 * for the page, the embed and Download PNG. Waits for the card's font, so
 * every machine draws the same card, and inlines it into the image. It
 * writes the SVG styles into `node` (see inlineSvgStyles), so hand it a
 * card drawn for the capture, such as an OffscreenCard.
 */
export async function captureCard(node: HTMLElement): Promise<{ image: Blob; image2x: Blob }> {
  await document.fonts.ready
  await frames(2)
  inlineSvgStyles(node)
  const opts = {
    width: CARD.width,
    height: CARD.height,
    cacheBust: true,
    backgroundColor: getComputedStyle(node).backgroundColor,
  }
  const [image, image2x] = await Promise.all([
    toBlob(node, { ...opts, pixelRatio: 1 }),
    toBlob(node, { ...opts, pixelRatio: 2 }),
  ])
  if (!image || !image2x) throw new Error('Could not capture the card')
  return { image, image2x }
}

/** Saves `blob` as `filename` through a throwaway link. */
export function downloadBlob(blob: Blob, filename: string): void {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = filename
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}
