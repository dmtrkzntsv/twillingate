/** What a rule acts on: an event name (of a family) or an attribute key. */
export type RuleNoun = 'event' | 'attribute'

/** The name or key a rule dialog acts on. */
export interface RuleTarget {
  name: string
  /** Shown after the name, e.g. "product"; omitted for attributes. */
  context?: string
}

/** A name with its context in parentheses, when it has one: "signup (product)". */
const withContext = (t: RuleTarget) => (t.context ? `${t.name} (${t.context})` : t.name)

/**
 * The wording of the rename, merge, alias and block dialogs, in one place: an
 * event's texts arrive "as" a name; an attribute's are "carried" by events.
 */
export function ruleCopy(noun: RuleNoun) {
  const event = noun === 'event'
  return {
    rename: {
      title: (t: RuleTarget) => `Rename ${t.name}`,
      description: (t: RuleTarget) =>
        event
          ? `Events arriving as ${withContext(t)} are stored under the new name, and every report shows the ones already stored under it at once. Widgets whose SQL names the old event are not changed.`
          : `Events carrying ${t.name} store it under the new name, and every report shows the values already stored under it at once. Widgets whose SQL names the old attribute are not changed.`,
      label: 'New name',
      blocked: (to: string, t: RuleTarget) => `${to} is blocked: nothing is renamed onto a blocked ${noun}. To stop receiving ${t.name}, block it.`,
      taken: (to: string, t: RuleTarget) => `${to} already has events: use Merge into… to fold ${t.name} into it.`,
    },
    merge: {
      title: (t: RuleTarget) => `Merge ${t.name} into…`,
      description: (t: RuleTarget) => {
        const moves = event
          ? `Every report counts ${t.name}'s events${t.context ? ` (${t.context})` : ''} under the event you pick, past ones included, and events sent later as ${t.name} are stored under it.`
          : `Every report shows ${t.name}'s values under the attribute you pick, past ones included, and events sent later carrying ${t.name} store it under that one.`
        return `${moves} Removing the rule later shows past events as sent again, but not the ones received while it held. Widgets whose SQL names ${t.name} are not changed.`
      },
      none: (t: RuleTarget) => (event ? `No other ${t.context} events.` : 'No other attributes.'),
      search: event ? 'Search events' : 'Search attributes',
    },
    alias: {
      title: (t: RuleTarget) => `Add an alias to ${t.name}`,
      /** With the alias typed (and valid) or not yet. */
      description: (t: RuleTarget, alias?: string) =>
        event
          ? alias ? `Events sent as ${alias} are stored as ${t.name}.` : `Events sent under the alias are stored as ${withContext(t)}.`
          : alias ? `Events carrying ${alias} store it as ${t.name}.` : `Events carrying the alias store it as ${t.name}.`,
      label: 'Old name',
      merging: (alias: string, t: RuleTarget) => `${alias} already has events: every report counts them under ${t.name} from now on.`,
    },
    unalias: {
      title: (alias: string) => `Remove alias ${alias}?`,
      description: (alias: string, to: string) =>
        event
          ? `Events sent as ${alias} arrive under their own name again, and so do the ones stored before the alias; those received while it held stay under ${to}.`
          : `Events carrying ${alias} keep it under its own name again, and so do the ones stored before the alias; those received while it held stay under ${to}.`,
    },
    block: {
      title: (t: RuleTarget) => `Block ${t.name}?`,
      description: (t: RuleTarget) =>
        event
          ? `Events named ${withContext(t)} are hidden from every report at once, past events included; new ones are not stored. Remove the rule to receive it again.`
          : `${t.name} is hidden from every report at once, past events included; new ones are not stored. Remove the rule to receive it again.`,
      action: event ? 'Block event' : 'Block attribute',
    },
  }
}
