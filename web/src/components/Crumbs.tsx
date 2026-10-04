import { Fragment } from 'react'
import { Link } from 'react-router'
import { Breadcrumb, BreadcrumbItem, BreadcrumbLink, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from '@/components/ui/breadcrumb'

export interface Crumb {
  label: string
  /** Where the crumb goes; the last crumb is the page itself and never links. */
  to?: string
}

/** Where a page sits, for the top bar: every crumb but the last links to its page. */
export default function Crumbs({ items }: { items: Crumb[] }) {
  return (
    <Breadcrumb className="min-w-0">
      <BreadcrumbList className="flex-nowrap">
        {items.map((c, i) => {
          const last = i === items.length - 1
          return (
            <Fragment key={`${i}-${c.label}`}>
              <BreadcrumbItem className="min-w-0">
                {!last && c.to ? (
                  <BreadcrumbLink asChild>
                    <Link to={c.to}>{c.label}</Link>
                  </BreadcrumbLink>
                ) : (
                  <BreadcrumbPage className="truncate">{c.label}</BreadcrumbPage>
                )}
              </BreadcrumbItem>
              {!last && <BreadcrumbSeparator />}
            </Fragment>
          )
        })}
      </BreadcrumbList>
    </Breadcrumb>
  )
}
