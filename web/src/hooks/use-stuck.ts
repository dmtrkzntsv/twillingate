import { useEffect, useState } from 'react'

/**
 * Whether a `position: sticky` element with `top: <top>px` is pinned. The
 * element is watched against the viewport shrunk by `top + 1` pixels at the
 * top: pinned, it pokes a pixel out of that; at rest further down, it is
 * wholly inside. False where there is no IntersectionObserver.
 */
export function useStuck(el: Element | null, top: number): boolean {
  const [stuck, setStuck] = useState(false)
  useEffect(() => {
    if (!el || typeof IntersectionObserver === 'undefined') return
    const observer = new IntersectionObserver(
      ([entry]) => setStuck(entry.intersectionRatio < 1 && entry.boundingClientRect.top <= top + 1),
      { rootMargin: `-${top + 1}px 0px 0px 0px`, threshold: [1] }
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [el, top])
  return stuck
}
