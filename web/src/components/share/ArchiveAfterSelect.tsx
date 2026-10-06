import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import type { ArchiveAfter } from '@/lib/api'
import { ARCHIVE_AFTER } from '@/lib/share'

interface Props {
  value: ArchiveAfter
  onChange: (value: ArchiveAfter) => void
  id?: string
  'aria-label'?: string
}

/** When a share archives itself: a native select over the archive-after choices. */
export function ArchiveAfterSelect({ value, onChange, id, 'aria-label': label }: Props) {
  return (
    <NativeSelect id={id} aria-label={label} value={value} onChange={(e) => onChange(e.target.value as ArchiveAfter)}>
      {ARCHIVE_AFTER.map((o) => (
        <NativeSelectOption key={o.value} value={o.value}>
          {o.label}
        </NativeSelectOption>
      ))}
    </NativeSelect>
  )
}
