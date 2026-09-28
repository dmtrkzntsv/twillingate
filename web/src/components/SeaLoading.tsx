import { LoaderCircleIcon } from 'lucide-react'

/** The connect page's background (internal/api/oauth_page.html), so passing between the two shows no seam. */
export const SEA = 'bg-[radial-gradient(ellipse_at_50%_42%,#0b3a66_0%,#061a30_48%,#030b15_100%)] bg-[#030b15]'

/**
 * What the app shows while it hands over to the connect page or back from
 * it: the connect page's sea and a spinner, which fades in only if the
 * hop takes long enough to notice.
 */
export default function SeaLoading() {
  return (
    <main className={`flex min-h-svh items-center justify-center ${SEA}`}>
      <div role="status" className="fade-in-late flex items-center gap-2.5 text-sm text-[#8fd3ff]">
        <LoaderCircleIcon className="size-4 animate-spin" aria-hidden="true" />
        Loading…
      </div>
    </main>
  )
}
