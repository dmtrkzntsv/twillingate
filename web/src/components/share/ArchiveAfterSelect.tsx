import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import type { ArchiveAfter } from '@/lib/api'
import { archiveAfterChoices } from '@/lib/share'

interface Props {
  value: ArchiveAfter
  onChange: (value: ArchiveAfter) => void
  /** Whether the share has a project: its no-date choice is then the project's lifetime, else "Never". */
  hasProject: boolean
  disabled?: boolean
  id?: string
  'aria-label'?: string
}

/** When a share archives itself: a native select over the archive-after choices, for a form. */
export function ArchiveAfterSelect({ value, onChange, hasProject, disabled, id, 'aria-label': label }: Props) {
  return (
    <NativeSelect id={id} aria-label={label} disabled={disabled} value={value} onChange={(e) => onChange(e.target.value as ArchiveAfter)}>
      {archiveAfterChoices(hasProject).map((o) => (
        <NativeSelectOption key={o.value} value={o.value}>
          {o.label}
        </NativeSelectOption>
      ))}
    </NativeSelect>
  )
}
