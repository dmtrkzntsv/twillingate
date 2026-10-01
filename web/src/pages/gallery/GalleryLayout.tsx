import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import AppShell, { TopBar } from '@/components/AppShell'
import { dashboardsQuery } from '@/lib/queries'

interface Props {
  title: string
  description: string
  /** The jump list: one link per section, in page order. */
  sections: { id: string; label: string }[]
  children: ReactNode
}

/** A gallery page: the app shell, a title, a jump list, then the entries. */
export default function GalleryLayout({ title, description, sections, children }: Props) {
  const list = useQuery(dashboardsQuery)
  return (
    <AppShell dashboards={list.data?.dashboards ?? []} currentId={0} readOnly={list.data?.dev === true}>
      <TopBar>
        <span className="text-sm text-muted-foreground">Gallery</span>
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
          <p className="max-w-prose text-sm text-muted-foreground">{description}</p>
          <nav aria-label={title} className="flex flex-wrap gap-1.5">
            {sections.map((s) => (
              <a
                key={s.id}
                href={`#${s.id}`}
                className="rounded-md border border-border/70 bg-background/60 px-2 py-0.5 font-mono text-xs hover:bg-accent"
              >
                {s.label}
              </a>
            ))}
          </nav>
        </header>
        {children}
      </div>
    </AppShell>
  )
}
