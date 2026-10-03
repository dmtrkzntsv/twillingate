import CopyButton from './CopyButton'

interface Props {
  /** Absent when the server returned only a snippet (a project created without a key). */
  keyValue?: string
  snippet?: string
  note?: string
}

/** A freshly issued ingest key with its install snippet and note, each copyable. */
export default function IssuedKeyView({ keyValue, snippet, note }: Props) {
  return (
    <div className="flex flex-col gap-2">
      {keyValue && (
        <div className="flex items-center gap-2 rounded-md border p-2 font-mono text-sm">
          <span className="flex-1 truncate">{keyValue}</span>
          <CopyButton value={keyValue} label="Copy key" />
        </div>
      )}
      {snippet && (
        <div className="flex items-start gap-2 rounded-md border p-2">
          <pre className="flex-1 overflow-x-auto font-mono text-xs whitespace-pre-wrap">{snippet}</pre>
          <CopyButton value={snippet} label="Copy snippet" />
        </div>
      )}
      {note && <p className="text-xs text-muted-foreground">{note}</p>}
    </div>
  )
}
