/* The global: the default instance plus a registry of named ones, and
 * the bundle entry's decision about what it finds at window.twillingate.
 * No window.<name> is ever created; a second tag with data-instance
 * lands in the registry of whichever copy got there first.
 */

import { DEFAULT_INSTANCE, Twillingate, VERSION, instanceName, type InitOptions } from "./twillingate";

export class TwillingateGlobal extends Twillingate {
  readonly VERSION = VERSION;
  private registry = new Map<string, Twillingate>();

  constructor() {
    super(DEFAULT_INSTANCE);
  }

  /**
   * Build, register and (with options) initialise a named instance. An
   * existing name returns the existing instance and ignores the options,
   * with a warning; the default name and an invalid one are refused and
   * the default instance returned.
   */
  create(name: string, opts?: InitOptions): Twillingate {
    if (name === DEFAULT_INSTANCE || name === "") {
      console.warn(`twillingate: create() needs a name other than "${DEFAULT_INSTANCE}"; that instance is the global itself`);
      return this;
    }
    if (instanceName(name) !== name) return this; // instanceName warned
    const existing = this.registry.get(name);
    if (existing) {
      if (opts) console.warn(`twillingate: instance "${name}" already exists; ignoring the new options`);
      return existing;
    }
    const instance = new Twillingate(name);
    this.registry.set(name, instance);
    if (opts) instance.init(opts);
    return instance;
  }

  /** The named instance, or the default with no argument. Nothing is created on lookup. */
  get(name?: string): Twillingate | undefined {
    if (name === undefined || name === DEFAULT_INSTANCE) return this;
    return this.registry.get(name);
  }

  /** Registered names, for diagnostics. */
  instances(): string[] {
    return Array.from(this.registry.keys());
  }

  /**
   * Store an already-built instance under name, if the name is free.
   * Internal: bootstrap uses this to carry a superseded copy's named
   * instances over to the copy that replaces it, without re-running
   * init() or validation beyond skipping the default name.
   */
  adopt(name: string, instance: Twillingate): void {
    if (name === DEFAULT_INSTANCE || this.registry.has(name)) return;
    this.registry.set(name, instance);
  }
}

/**
 * A live SDK global from any release: create() plus the VERSION marker.
 * init() alone is not enough — plenty of unrelated globals expose one.
 */
export function isSDK(x: unknown): x is TwillingateGlobal {
  return !!x && typeof (x as TwillingateGlobal).create === "function" && typeof (x as { VERSION?: unknown }).VERSION === "string";
}

/** The tag's attributes as options, or null without data-key. */
export function tagOptions(script: HTMLScriptElement | null): InitOptions | null {
  if (!script) return null;
  const key = script.getAttribute("data-key");
  if (!key) return null;
  return {
    key,
    identity: script.getAttribute("data-identity") === "identified" ? "identified" : "anonymous",
    consent: script.getAttribute("data-consent") || undefined,
    autoPageviews: script.getAttribute("data-auto") !== "off",
    maskUrl: script.getAttribute("data-mask-url") || undefined,
    routing: script.getAttribute("data-routing") === "hash" ? "hash" : "history",
    kind: script.getAttribute("data-kind") || undefined,
  };
}

/** Snippet-mode init from the loading <script>'s attributes; dormant without data-key. */
export function autoInit(tg: Twillingate, script: HTMLScriptElement | null): void {
  const opts = tagOptions(script);
  if (opts) tg.init(opts);
}

/**
 * Whether this copy should stand down and leave the default instance in
 * place: a second tag with the same key or no key. A different key takes
 * over, with a warning when the first copy had a key of its own.
 */
export function supersededBy(existing: unknown, script: HTMLScriptElement | null): boolean {
  if (!isSDK(existing)) return false;
  // Read structurally: a copy from another release is a different class.
  const loadedKey = (existing as unknown as { key?: unknown }).key;
  const key = script?.getAttribute("data-key");
  if (!key || key === loadedKey) {
    console.warn("twillingate: twillingate.js loaded twice; keeping the first copy, remove the duplicate <script> tag");
    return true;
  }
  if (loadedKey) {
    console.warn(`twillingate: a second twillingate.js replaced window.twillingate (${loadedKey} -> ${key})`);
  }
  return false;
}

/**
 * Bundle entry. Three cases at window.twillingate: nothing (register and
 * auto-init), a live SDK (join its registry with data-instance, else the
 * duplicate rule), something foreign (warn, leave it, track unregistered).
 */
export function bootstrap(script: HTMLScriptElement | null): Twillingate | null {
  const attr = script ? script.getAttribute("data-instance") : null;
  const name = attr === null || attr === "" ? DEFAULT_INSTANCE : instanceName(attr);
  const g = window as unknown as Record<string, unknown>;
  const existing = g.twillingate;

  if (isSDK(existing)) {
    if (name !== DEFAULT_INSTANCE) return existing.create(name, tagOptions(script) ?? undefined);
    if (supersededBy(existing, script)) return null;
    // A different key takes over the default instance: carry the old
    // global's named instances across structurally (a copy from another
    // release still hands over what it can), then retire the old default
    // so it stops sending under the superseded key.
    const tg = new TwillingateGlobal();
    if (typeof existing.instances === "function" && typeof existing.get === "function") {
      for (const n of existing.instances()) {
        const instance = existing.get(n);
        if (instance) tg.adopt(n, instance);
      }
    }
    if (typeof existing.retire === "function") existing.retire();
    g.twillingate = tg;
    autoInit(tg, script);
    return tg;
  } else if (existing !== undefined && existing !== null) {
    console.warn("twillingate: window.twillingate is already taken by something else; this tag's instance is not registered there");
    const tg = new TwillingateGlobal();
    if (name !== DEFAULT_INSTANCE) return tg.create(name, tagOptions(script) ?? undefined);
    autoInit(tg, script);
    return tg;
  }

  const tg = new TwillingateGlobal();
  g.twillingate = tg;
  if (name !== DEFAULT_INSTANCE) return tg.create(name, tagOptions(script) ?? undefined);
  autoInit(tg, script);
  return tg;
}
