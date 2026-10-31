import { MoreHorizontalIcon, PlusIcon, XIcon } from 'lucide-react'
import ConfirmDialog from '@/components/ConfirmDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { useClosingTarget } from '@/hooks/use-closing-target'
import AliasDialog from './AliasDialog'
import MergeDialog from './MergeDialog'
import RenameDialog from './RenameDialog'
import { ruleCopy, type RuleNoun, type RuleTarget } from './rule-copy'

/** The rule dialogs of a list of events or attributes, each on the name it was opened for. */
export function useRuleDialogs<T extends RuleTarget>() {
  return {
    renaming: useClosingTarget<T>(),
    merging: useClosingTarget<T>(),
    aliasing: useClosingTarget<T>(),
    blocking: useClosingTarget<T>(),
    /** An alias (a rename onto `to`) to remove. */
    unaliasing: useClosingTarget<{ alias: T; to: string }>(),
  }
}

type Dialogs<T extends RuleTarget> = ReturnType<typeof useRuleDialogs<T>>

interface MenuProps<T extends RuleTarget> {
  /** The trigger's accessible name. */
  label: string
  target: T
  blocked: boolean
  pending?: boolean
  dialogs: Dialogs<T>
  onUnblock: () => void
}

/** A row's actions: rename, merge and alias open their dialogs; block asks first, unblock does not. */
export function RuleMenu<T extends RuleTarget>({ label, target, blocked, pending, dialogs, onUnblock }: MenuProps<T>) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="size-7" aria-label={label}>
          <MoreHorizontalIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem disabled={pending} onClick={() => dialogs.renaming.show(target)}>Rename…</DropdownMenuItem>
        <DropdownMenuItem disabled={pending} onClick={() => dialogs.merging.show(target)}>Merge into…</DropdownMenuItem>
        <DropdownMenuItem disabled={pending} onClick={() => dialogs.aliasing.show(target)}>Add alias…</DropdownMenuItem>
        {blocked ? (
          <DropdownMenuItem disabled={pending} onClick={onUnblock}>Unblock</DropdownMenuItem>
        ) : (
          <DropdownMenuItem disabled={pending} onClick={() => dialogs.blocking.show(target)}>Block…</DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

interface BadgesProps<T extends RuleTarget> {
  target: T
  /** The names renamed onto the target. */
  aliases: string[]
  pending?: boolean
  dialogs: Dialogs<T>
}

/** Under a name, the old names renamed to it, each removable (confirmed), and a button adding one. */
export function AliasBadges<T extends RuleTarget>({ target, aliases, pending, dialogs }: BadgesProps<T>) {
  return (
    <div className="mt-1 flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
      <span>also from</span>
      {aliases.map((a) => (
        <Badge key={a} variant="outline" className="gap-0.5 border-sky-500/30 bg-background pr-0.5 font-mono whitespace-normal">
          <span className="break-all">{a}</span>
          <Button variant="ghost" size="icon" className="size-4" aria-label={`Remove alias ${a}`} disabled={pending}
            onClick={() => dialogs.unaliasing.show({ alias: { ...target, name: a }, to: target.name })}>
            <XIcon className="size-3" />
          </Button>
        </Badge>
      ))}
      <Button variant="outline" size="icon" className="size-5 rounded-full" aria-label={`Add alias to ${target.name}`} disabled={pending}
        onClick={() => dialogs.aliasing.show(target)}>
        <PlusIcon className="size-3" />
      </Button>
    </div>
  )
}

interface DialogsProps<T extends RuleTarget> {
  noun: RuleNoun
  dialogs: Dialogs<T>
  pending?: boolean
  /** What a rename or alias onto the target must know: the names with data, and the blocked ones. */
  taken: (t: T | undefined) => string[]
  blocked: (t: T | undefined) => string[]
  /** What the target can merge into. */
  into: (t: T | undefined) => { name: string; count: number }[]
  /** The names already renamed onto the target. */
  aliases: (t: T | undefined) => string[]
  /** Each resolves true when done; a refusal is already toasted (except a rename's 'conflict', which points to merging). */
  onRename: (t: T, to: string) => Promise<boolean | 'conflict'>
  onMerge: (t: T, to: string) => Promise<boolean>
  onAddAlias: (t: T, alias: string) => Promise<boolean>
  onBlock: (t: T) => Promise<boolean>
  /** Removes the rule on an alias: its rename. */
  onRemove: (t: T) => Promise<boolean>
}

/** The rename, merge and alias dialogs, and the confirms of a block and of an alias's removal. */
export function RuleDialogs<T extends RuleTarget>({ noun, dialogs, pending, taken, blocked, into, aliases, onRename, onMerge, onAddAlias, onBlock, onRemove }: DialogsProps<T>) {
  const copy = ruleCopy(noun)
  const { renaming, merging, aliasing, blocking, unaliasing } = dialogs
  return (
    <>
      <RenameDialog
        open={renaming.open}
        onOpenChange={renaming.onOpenChange}
        noun={noun}
        target={renaming.target ?? null}
        taken={taken(renaming.target)}
        blocked={blocked(renaming.target)}
        pending={pending}
        onRename={(to) => onRename(renaming.target!, to)}
      />
      <MergeDialog
        open={merging.open}
        onOpenChange={merging.onOpenChange}
        noun={noun}
        target={merging.target ?? null}
        into={into(merging.target)}
        pending={pending}
        onMerge={(to) => onMerge(merging.target!, to)}
      />
      <AliasDialog
        open={aliasing.open}
        onOpenChange={aliasing.onOpenChange}
        noun={noun}
        target={aliasing.target ?? null}
        taken={taken(aliasing.target)}
        aliases={aliases(aliasing.target)}
        pending={pending}
        onAdd={(alias) => onAddAlias(aliasing.target!, alias)}
      />
      <ConfirmDialog
        open={unaliasing.open}
        onOpenChange={unaliasing.onOpenChange}
        titleClassName="wrap-anywhere"
        descriptionClassName="wrap-anywhere"
        title={unaliasing.target && copy.unalias.title(unaliasing.target.alias.name)}
        description={unaliasing.target && copy.unalias.description(unaliasing.target.alias.name, unaliasing.target.to)}
        action="Remove alias"
        onAction={() => void onRemove(unaliasing.target!.alias)}
      />
      <ConfirmDialog
        open={blocking.open}
        onOpenChange={blocking.onOpenChange}
        titleClassName="wrap-anywhere"
        descriptionClassName="wrap-anywhere"
        title={blocking.target && copy.block.title(blocking.target)}
        description={blocking.target && copy.block.description(blocking.target)}
        action={copy.block.action}
        onAction={() => void onBlock(blocking.target!)}
      />
    </>
  )
}
