import { Button } from '@/components/ui/button'

/** A failed read, said once: what could not load, why, and a Retry. */
export default function LoadError({ what, error, onRetry }: { what: string; error: Error; onRetry: () => void }) {
  return (
    <div role="alert" className="flex flex-col gap-2 text-sm text-muted-foreground">
      <p>
        Couldn't load {what}. {error.message}
      </p>
      <Button variant="outline" size="sm" className="self-start" onClick={onRetry}>Retry</Button>
    </div>
  )
}
