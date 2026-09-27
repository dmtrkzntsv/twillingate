import type { ReactNode } from 'react'
import { ChartColumnIcon } from 'lucide-react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

interface Props {
  title: string
  description?: ReactNode
  children?: ReactNode
}

/** The app's name above one centered card: sign-in, loading and "nothing to show" pages. */
export default function StatusCard({ title, description, children }: Props) {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-6 bg-muted/40 p-6">
      <Brand />
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>{title}</CardTitle>
          {description && <CardDescription>{description}</CardDescription>}
        </CardHeader>
        {children && <CardContent>{children}</CardContent>}
      </Card>
    </div>
  )
}

export function Brand() {
  return (
    <div className="flex items-center gap-2 font-medium">
      <span className="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
        <ChartColumnIcon className="size-4" />
      </span>
      twillingate
    </div>
  )
}
