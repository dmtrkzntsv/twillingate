import { useId, type ReactNode } from 'react'

/**
 * A page that is not there: a heading, what to do about it, and the sea
 * where the page would have been. The dashboard, project and catch-all
 * 404s use it; the server's own 404 pages draw the same scene
 * (internal/reporting/not_found.html).
 */
export default function NotFound({
  title,
  children,
  actions,
  inPage = false,
}: {
  title: string
  children: ReactNode
  actions?: ReactNode
  /** Inside a page that has its own heading (a project's tab): the title is a level below it. */
  inPage?: boolean
}) {
  const Heading = inPage ? 'h2' : 'h1'
  const headingId = useId()
  return (
    <section aria-labelledby={headingId} className="flex w-full max-w-5xl flex-col gap-5">
      <div className="flex flex-col gap-2">
        <Heading id={headingId} className="text-2xl font-semibold tracking-tight sm:text-3xl">
          {title}
        </Heading>
        <p className="max-w-prose text-muted-foreground">{children}</p>
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
      <SunkenIceberg />
    </section>
  )
}

/** An iceberg almost all under water, with "404" sounded on a chart's depth contour. */
export function SunkenIceberg() {
  const id = useId()
  const sky = `${id}-sky`
  const sea = `${id}-sea`
  const lit = `${id}-lit`
  const shade = `${id}-shade`
  return (
    <svg
      viewBox="0 0 1200 630"
      role="img"
      aria-label="An iceberg, almost all of it under water"
      className="block h-auto w-full rounded-xl shadow-[0_1px_3px_rgb(15_27_45/12%),0_8px_24px_rgb(15_27_45/8%)] dark:shadow-[0_0_0_1px_rgb(255_255_255/8%),0_8px_24px_rgb(0_0_0/50%)]"
    >
      <defs>
        <linearGradient id={sky} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" className="[stop-color:#eef8ff] dark:[stop-color:#0d1f38]" />
          <stop offset="1" className="[stop-color:#a9dbfa] dark:[stop-color:#1b3f69]" />
        </linearGradient>
        <linearGradient id={sea} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" className="[stop-color:#1478c9] dark:[stop-color:#0d4f91]" />
          <stop offset="1" className="[stop-color:#062f5e] dark:[stop-color:#020c1a]" />
        </linearGradient>
        <linearGradient id={lit} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#54c8f7" stopOpacity=".75" />
          <stop offset="1" stopColor="#1478c9" stopOpacity="0" />
        </linearGradient>
        <linearGradient id={shade} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#073f7c" stopOpacity=".45" />
          <stop offset="1" stopColor="#073f7c" stopOpacity="0" />
        </linearGradient>
      </defs>
      <rect width="1200" height="260" fill={`url(#${sky})`} />
      <path d="M0 70H1200M0 135H1200M0 200H1200" className="stroke-[#8fd3ff] [stroke-opacity:.45] dark:[stroke-opacity:.12]" />
      <rect y="260" width="1200" height="370" fill={`url(#${sea})`} />
      <path
        d="M372 300C330 380 380 520 520 588C640 640 790 600 850 500C905 410 880 320 840 280"
        fill="none"
        stroke="#8fd3ff"
        strokeOpacity=".55"
        strokeWidth="2"
        strokeDasharray="2 7"
        strokeLinecap="round"
      />
      <text x="862" y="438" fill="#b9e4ff" fontSize="26" fontWeight="500" letterSpacing=".02em" className="font-sans">
        404
      </text>
      <g className="berg-bob">
        <path d="M450 260H760L812 360L738 500L612 590L494 528L414 390Z" fill={`url(#${lit})`} />
        <path d="M600 260L760 260L812 360L738 500L612 590Z" fill={`url(#${shade})`} />
        <path d="M528 260L567 226L588 236L612 182L636 218L654 208L694 260Z" fill="#f8fcff" />
        <path d="M528 260L567 226L588 236L562 260Z" fill="#cfe9fb" />
        <path d="M588 236L612 182L620 240L604 260H562Z" fill="#d5f1ff" />
        <path d="M612 182L636 218L620 240Z" fill="#2e9fe5" />
        <path d="M636 218L654 208L672 260H620Z" fill="#62c3f3" />
      </g>
      <path
        d="M0 260C40 254 80 266 120 260S200 254 240 260S320 266 360 260S440 254 480 260S560 266 600 260S680 254 720 260S800 266 840 260S920 254 960 260S1040 266 1080 260S1160 254 1200 260"
        fill="none"
        stroke="#f8fcff"
        strokeOpacity=".8"
        strokeWidth="2"
      />
    </svg>
  )
}
