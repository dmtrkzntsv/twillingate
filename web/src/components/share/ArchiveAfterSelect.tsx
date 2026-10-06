import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import type { ArchiveAfter } from '@/lib/api'
import { ARCHIVE_AFTER } from '@/lib/share'

interface Props {
  value: ArchiveAfter
  onChange: (value: ArchiveAfter) => void
  /**
   * A date the share already has ("Nov 4"). No period matches it exactly, so
   * it is an extra first option that stands for the current value and is
   * selected; `value` is then ignored.
   */
  current?: string
  id?: string
  'aria-label'?: string
}

const CURRENT = 'current'

/** When a share archives itself: a native select over the archive-after choices. */
export function ArchiveAfterSelect({ value, onChange, current, id, 'aria-label': label }: Props) {
  return (
    <NativeSelect
      id={id}
      aria-label={label}
      value={current ? CURRENT : value}
      onChange={(e) => {
        if (e.target.value !== CURRENT) onChange(e.target.value as ArchiveAfter)
      }}
    >
      {current && <NativeSelectOption value={CURRENT}>{current}</NativeSelectOption>}
      {ARCHIVE_AFTER.map((o) => (
        <NativeSelectOption key={o.value} value={o.value}>
          {o.label}
        </NativeSelectOption>
      ))}
    </NativeSelect>
  )
}
