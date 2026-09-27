import type { ComponentProps, ReactNode } from 'react'
import { ChevronDownIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'

/** The select-like trigger both header switchers share: icon, value, chevron. */
export default function SwitcherButton({
  icon,
  className,
  children,
  ...props
}: ComponentProps<typeof Button> & { icon: ReactNode }) {
  return (
    <Button
      variant="outline"
      size="sm"
      className={cn('min-w-0 justify-start gap-2 font-normal sm:max-w-60 [&>svg]:text-muted-foreground', className)}
      {...props}
    >
      {icon}
      <span className="truncate">{children}</span>
      <ChevronDownIcon className="ml-auto opacity-60" />
    </Button>
  )
}
