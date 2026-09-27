import { ArchiveIcon, FolderIcon } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { Project } from '@/lib/api'
import SwitcherButton from './SwitcherButton'

interface Props {
  projects: Project[]
  value?: number
  onChange: (projectId: number) => void
  className?: string
}

/** Picks the project: the active ones, and archived ones folded into a group of their own. */
export default function ProjectSwitcher({ projects, value, onChange, className }: Props) {
  const active = projects.filter((p) => !p.archived)
  const archived = projects.filter((p) => p.archived)
  const current = projects.find((p) => p.project_id === value)
  const select = (v: string) => onChange(Number(v))

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <SwitcherButton icon={<FolderIcon />} aria-label={`Project: ${current?.name ?? 'none'}`} className={className}>
          {current?.name ?? 'Choose a project'}
        </SwitcherButton>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-48">
        <DropdownMenuLabel className="text-xs text-muted-foreground">Projects</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={String(value)} onValueChange={select}>
          {active.map((p) => (
            <DropdownMenuRadioItem key={p.project_id} value={String(p.project_id)}>
              {p.name}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {archived.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <ArchiveIcon />
                Archived
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent>
                <DropdownMenuRadioGroup value={String(value)} onValueChange={select}>
                  {archived.map((p) => (
                    <DropdownMenuRadioItem key={p.project_id} value={String(p.project_id)}>
                      {p.name}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
