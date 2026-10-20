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
          ? `Events arriving as ${withContext(t)} are stored under the new name, and the ones already stored are renamed. Widgets whose SQL names the old event are not changed.`
          : `Events carrying ${t.name} store it under the new name, and the ones already stored are renamed. Widgets whose SQL names the old attribute are not changed.`,
      label: 'New name',
      blocked: (to: string, t: RuleTarget) => `${to} is blocked: nothing is renamed onto a blocked ${noun}. To stop receiving ${t.name}, block it.`,
      taken: (to: string, t: RuleTarget) => `${to} already has events: use Merge into… to fold ${t.name} into it.`,
    },
    merge: {
      title: (t: RuleTarget) => `Merge ${t.name} into…`,
      description: (t: RuleTarget) => {
        const moves = event
          ? `${t.name}'s stored events${t.context ? ` (${t.context})` : ''} move to the event you pick, and events sent later as ${t.name} are stored under it.`
          : `${t.name}'s stored values move to the attribute you pick, and events sent later carrying ${t.name} store it under that one.`
        return `${moves} A merge cannot be undone, and on merged days unique users and groups become an upper bound. Widgets whose SQL names ${t.name} are not changed.`
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
      merging: (alias: string, t: RuleTarget) => `${alias} already has events: they merge into ${t.name}, and this cannot be undone.`,
    },
    unalias: {
      title: (alias: string) => `Remove alias ${alias}?`,
      description: (alias: string, to: string) =>
        event
          ? `Events sent as ${alias} arrive under their own name again; the ones already renamed stay under ${to}.`
          : `Events carrying ${alias} keep it under its own name again; the ones already renamed stay under ${to}.`,
    },
    block: {
      title: (t: RuleTarget) => `Block ${t.name}?`,
      description: (t: RuleTarget) =>
        event
          ? `Events named ${withContext(t)} are hidden from every report at once, past events included; new ones are not stored. Remove the rule to receive it again.`
          : `${t.name} is hidden from every report at once, past events included; new ones are not stored, and it stops being a breakdown. Remove the rule to receive it again.`,
      action: event ? 'Block event' : 'Block attribute',
    },
  }
}
