import { useLayoutEffect, useRef } from 'react'
import { useLocation, useNavigationType } from 'react-router'

/**
 * Opens every page the user goes to at its top. The window is the page's
 * scroller, and a client-side navigation keeps its position, so without this
 * a page opened from the bottom of a long one starts part way down. Only the
 * path counts: a query change (the range, a filter) stays where it is. Back
 * and forward (POP) are left to the browser's own restoration.
 */
export default function ScrollToTop() {
  const { pathname } = useLocation()
  const type = useNavigationType()
  const last = useRef(pathname)
  useLayoutEffect(() => {
    if (pathname === last.current) return
    last.current = pathname
    if (type !== 'POP') window.scrollTo(0, 0)
  }, [pathname, type])
  return null
}
