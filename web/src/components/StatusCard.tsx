import type { ReactNode } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import IcebergLogo from './IcebergLogo'

interface Props {
  title: string
  description?: ReactNode
  children?: ReactNode
}

/** The app's name above one centered card: sign-in, loading and "nothing to show" pages. */
export default function StatusCard({ title, description, children }: Props) {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-6 sky-wash p-6">
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
    <div className="flex items-center gap-2.5 text-lg font-semibold tracking-tight">
      <IcebergLogo className="size-8 rounded-[22%] ring-1 ring-black/5" />
      twillingate
    </div>
  )
}
