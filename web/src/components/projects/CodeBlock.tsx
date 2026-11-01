import CopyButton from './CopyButton'

/** Copyable code that wraps rather than widening the page. */
export default function CodeBlock({ value, label }: { value: string; label: string }) {
  return (
    <div className="flex items-start gap-2 rounded-md border p-2">
      <pre className="min-w-0 flex-1 font-mono text-xs wrap-anywhere whitespace-pre-wrap">{value}</pre>
      <CopyButton value={value} label={label} />
    </div>
  )
}
