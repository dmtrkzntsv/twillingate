import { useId, type ReactNode } from 'react'

interface Props {
  title: string
  /** Under the title: what the setting does, or its current state. */
  aside?: ReactNode
  children: ReactNode
}

/**
 * One row of a form's settings: the title and its explanation on the left,
 * the controls on the right, kept to a readable width; stacked on a phone.
 * The row is a group named by its title.
 */
export default function SettingRow({ title, aside, children }: Props) {
  const id = useId()
  return (
    <div
      role="group"
      aria-labelledby={`${id}-title`}
      className="grid grid-cols-1 gap-3 py-5 first:pt-0 last:pb-0 md:grid-cols-[minmax(0,16rem)_minmax(0,1fr)] md:gap-8"
    >
      <div className="flex min-w-0 flex-col gap-1">
        <h4 id={`${id}-title`} className="text-sm font-medium">
          {title}
        </h4>
        {aside && <div className="text-xs text-muted-foreground">{aside}</div>}
      </div>
      <div className="flex w-full max-w-xl min-w-0 flex-col gap-3">{children}</div>
    </div>
  )
}
