import { useRef } from 'react'
import { createPortal } from 'react-dom'
import { ShareCard, type ShareCardProps } from './ShareCard'

/**
 * A full-size ShareCard out of sight, for capture: in a portal on <body>,
 * so it sits under <html class="dark"> when the console is dark and takes
 * the sharer's theme, and nothing around it scales or clips it.
 */
export function OffscreenCard({ onNode, ...card }: ShareCardProps & { onNode(node: HTMLDivElement): void }) {
  const ref = useRef<HTMLDivElement>(null)
  // Once the widget is in (a lazy one has loaded): the caller captures what it is handed.
  const ready = () => {
    if (ref.current) onNode(ref.current)
  }
  return createPortal(
    <div aria-hidden style={{ position: 'fixed', left: -10000, top: 0, pointerEvents: 'none' }}>
      <ShareCard ref={ref} {...card} onReady={ready} />
    </div>,
    document.body
  )
}
