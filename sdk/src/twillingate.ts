/* twillingate SDK core — one instance of the tracker.
 *
 * The bundle registers one object at window.twillingate (factory.ts): the
 * default instance plus a registry of named ones. This file is the
 * instance. It posts to the collector's POST /ingest/events
 * (docs/twillingate.md is the normative wire format).
 *
 * Three rules shape it:
 *  - every call is legal before init(): configuration takes effect at
 *    once, event-producing calls are held and run after init(), entry
 *    pageview first;
 *  - the identity mode decides what is SENT: an anonymous instance never
 *    sends $user_id, $user_name or $install_id;
 *  - attributes layer as derived < attrs() defaults < the call's own <
 *    listener returns, and null drops a key at the end.
 *
 * Nothing is kept on the device unless the instance declares consent;
 * with it, the storage driver holds the instance's keys.
 */

import { resolveMask, type MaskSpec } from "./mask";
import { resolveConsent, type ConsentSpec } from "./consent";
import { collectorOrigin } from "./origin";
import { DEBUG_FLAG, IGNORE_FLAG, readFlag, resolveStorage, writeFlag, type StorageDriver, type StorageSpec } from "./storage";
import { runtime, type NavigationSource, type Subscriber } from "./runtime";
import { maskIds, withQuery } from "./util";
import {
  detectAll, detectBrowser as detectBrowserFrom, detectDevice as detectDeviceFrom,
  detectOS as detectOSFrom, primePlatformVersion, type BrowserInfo, type ClientSignals, type DeviceInfo, type OSInfo,
} from "./detect";

export { detectOS, detectBrowser, detectDevice } from "./detect";
export type { ClientSignals, OSInfo, BrowserInfo, DeviceInfo } from "./detect";
export type { StorageDriver, StorageSpec } from "./storage";

// Substituted by the collector at serve time with its build version.
export const VERSION = "__TWILLINGATE_VERSION__";

export interface InitOptions {
  /** Ingest key (ak_…). Required. The collector's origin is in the served file. */
  key: string;
  /**
   * anonymous (default) or identified. Decides what the instance SENDS:
   * anonymous never sends $user_id, $user_name or $install_id and
   * identify()/installId() are inert; identified sends them and, with
   * consent, persists the visitor id, user and group.
   */
  identity?: "anonymous" | "identified";
  /**
   * May this instance keep anything on the device. Default false. true,
   * or a function or global name a consent manager maintains, unlocks the
   * storage driver; consulted at every storage decision, never cached.
   */
  consent?: ConsentSpec;
  /** Where keys live with consent: localStorage (default), sessionStorage, memory, cookie, or a driver. */
  storage?: StorageSpec;
  /**
   * What this client is: "web" (default), "app", "cli", or any short
   * lower-case token. Anything but "web" makes automatic tracking emit
   * $screen_view with the route path as the screen.
   */
  kind?: string;
  /** The surface the product is used through ($platform). Defaults to "web" while kind is "web". */
  platform?: string;
  /** Version of this client application ($app_version). */
  appVersion?: string;
  /** Automatic pageviews incl. pushState/popstate. Default true. */
  autoPageviews?: boolean;
  /** Track elements carrying data-twillingate-event. Default true. */
  taggedEvents?: boolean;
  /** Rewrite the URL before it is split into $host and $path. Fails closed. */
  maskUrl?: MaskSpec;
  /** "history" (default) or "hash". */
  routing?: "history" | "hash";
  /** Milliseconds events wait in the queue before a flush. Default 1000. */
  flushInterval?: number;
  /** true, or a function consulted at every event; OR-ed with the twillingate_ignore flag. */
  optOut?: boolean | (() => unknown);
  /** Log every event and send to the console; OR-ed with the twillingate_debug flag. */
  debug?: boolean;
}

/**
 * What a page listener sees for each pageview, automatic or manual.
 * host and path are THREADED: each listener receives the previous
 * listener's output. url is the post-mask URL.
 */
export interface PageviewInfo {
  url: string;
  host: string;
  path: string;
  referrer: string;
  attributes: Record<string, unknown>;
}

/** Return an object to merge attributes, false to cancel, anything else to observe. */
export type PageListener = (page: PageviewInfo) => Record<string, unknown> | false | void;

/** What an event listener sees for every event, after onPage for a pageview. */
export interface EventInfo {
  name: string;
  attributes: Record<string, unknown>;
}

export type EventListener = (event: EventInfo) => Record<string, unknown> | false | void;

interface Event {
  id: string;
  ts: string;
  name: string;
  attributes: Record<string, unknown>;
}

interface Batch {
  key: string;
  attributes: Record<string, unknown>;
  events: Event[];
}

function isBatch(x: unknown): x is Batch {
  if (!x || typeof x !== "object") return false;
  const events = (x as { events?: unknown }).events;
  return Array.isArray(events) && events.length > 0 && typeof (events[0] as { id?: unknown })?.id === "string";
}

function mergeBatches(stored: Batch[], pending: Batch[]): Batch[] {
  const known = new Set(stored.map((b) => b.events[0].id));
  const merged = [...stored, ...pending.filter((b) => !known.has(b.events[0].id))];
  return merged.length > MAX_STORED_BATCHES ? merged.slice(merged.length - MAX_STORED_BATCHES) : merged;
}

// Storage keys are prefixed with the instance name so two instances on
// one page do not share a visitor id or a queue.
export const DEFAULT_INSTANCE = "twillingate";
const SUFFIXES = ["visitor", "user", "user_name", "group", "group_name", "queue"] as const;
type Keys = Record<(typeof SUFFIXES)[number], string>;

function keysFor(instance: string): Keys {
  const k = {} as Keys;
  for (const s of SUFFIXES) k[s] = `${instance}_${s}`;
  return k;
}

const INSTANCE_RE = /^[a-z][a-z0-9_]{0,15}$/;

/**
 * Validate an instance name: a registry key and a storage-key prefix, so
 * it has to be an identifier. An invalid name is refused with a warning
 * and the default returned.
 */
export function instanceName(name: string | null | undefined): string {
  if (name === null || name === undefined || name === "") return DEFAULT_INSTANCE;
  if (INSTANCE_RE.test(name)) return name;
  console.warn(`twillingate: instance name ${JSON.stringify(name)} is not an identifier; using "${DEFAULT_INSTANCE}"`);
  return DEFAULT_INSTANCE;
}

const MAX_BATCH = 500; // server cap per docs/twillingate.md
const FLUSH_AT = 20; // flush early once this many events queue up
const MAX_STORED_BATCHES = 50; // offline queue bound: oldest dropped first
const MAX_HELD = MAX_BATCH; // calls held before init(): oldest dropped first

function uuid(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16);
  });
}

// The optOut option, resolved once: true is always out, a function is
// asked at every event and a throw counts as opted out (fail closed).
function resolveOptOut(spec: boolean | (() => unknown) | undefined): () => boolean {
  if (spec === true) return () => true;
  if (typeof spec !== "function") return () => false;
  let warned = false;
  return () => {
    try {
      return Boolean(spec());
    } catch (e) {
      if (!warned) {
        warned = true;
        console.warn("twillingate: the optOut function threw, treating as opted out", e);
      }
      return true;
    }
  };
}

export class Twillingate implements Subscriber {
  readonly name: string;
  private key = "";
  private url = "";
  private identified = false;
  private userId: string | null = null;
  private userName: string | null = null;
  private groupId: string | null = null;
  private groupName: string | null = null;
  private declaredInstallId: string | null = null;
  private defaultAttrs: Record<string, unknown> = {};
  private pageListeners: PageListener[] = [];
  private eventListeners: EventListener[] = [];
  private kind = "web";
  private platform: string | null = null;
  private appVersion: string | null = null;
  private flushInterval = 1000;
  private k: Keys;
  private driver: StorageDriver = resolveStorage(undefined);
  private consentSpec: () => boolean = () => false;
  private consentPin: boolean | null = null;
  // null until the first decision point: the first false read wipes keys
  // an earlier session may have left behind.
  private lastConsent: boolean | null = null;
  private optOutSpec: () => boolean = () => false;
  private debugOpt = false;
  // Failed batches waiting for a retry. Lives in memory; mirrored to the
  // driver only while consent reads true.
  private pending: Batch[] = [];
  private queue: Event[] = [];
  private flushTimer: ReturnType<typeof setTimeout> | null = null;
  private lastPage: string | null = null;
  // undefined = no mask configured; null = configured and unresolvable,
  // which drops pageviews (fail closed).
  private mask: ((href: string) => string) | null | undefined;
  private routing: "history" | "hash" = "history";
  private firstPageviewSent = false;
  private ready = false;
  private autoPageviews = true;
  private taggedEvents = true;
  // Event-producing calls made before init(), run in order after it.
  private held: Array<() => void> = [];
  private heldWarned = false;
  private warnedAnonymous = false;
  private retired = false;

  /** Path-shaping helpers, for use inside an onPage listener. */
  readonly util = { maskIds, withQuery };

  constructor(name: string = DEFAULT_INSTANCE) {
    this.name = name;
    this.k = keysFor(name);
  }

  init(opts: InitOptions): this {
    if (this.ready) {
      console.warn(`twillingate: ${this.label()} is already initialised; ignoring init()`);
      return this;
    }
    if (!opts || !opts.key) {
      console.warn("twillingate: init requires a key");
      return this;
    }
    const origin = collectorOrigin();
    if (!origin) {
      console.warn("twillingate: this build carries no collector origin; load twillingate.js from your collector");
      return this;
    }
    this.key = opts.key;
    this.url = origin;
    this.identified = opts.identity === "identified";
    this.consentSpec = resolveConsent(opts.consent);
    this.driver = resolveStorage(opts.storage);
    this.optOutSpec = resolveOptOut(opts.optOut);
    this.debugOpt = opts.debug === true;
    if (!this.identified && (this.userId !== null || this.userName !== null || this.declaredInstallId !== null)) {
      // Identity was set before init() decided the mode: an anonymous
      // instance sends none of it.
      this.userId = this.userName = this.declaredInstallId = null;
      this.warnAnonymous("identify() / installId()");
    }
    // Identity set before init() stands; storage fills only what is unset.
    if (this.identified && this.mayStore()) {
      if (this.userId === null) {
        this.userId = this.driver.get(this.k.user);
        this.userName = this.driver.get(this.k.user_name);
      }
      if (this.groupId === null) {
        this.groupId = this.driver.get(this.k.group);
        this.groupName = this.driver.get(this.k.group_name);
      }
    }
    this.kind = opts.kind && INSTANCE_RE.test(opts.kind) ? opts.kind : "web";
    this.platform = opts.platform || (this.kind === "web" ? "web" : null);
    // The one async detection input; read at flush time, not awaited.
    primePlatformVersion();
    this.appVersion = opts.appVersion || null;
    if (opts.flushInterval !== undefined) this.flushInterval = opts.flushInterval;
    // Resolved before any pageview can fire, so data-mask-url covers the
    // entry page -- the one most likely to carry an identifier.
    this.mask = resolveMask(opts.maskUrl);
    this.routing = opts.routing === "hash" ? "hash" : "history";
    this.autoPageviews = opts.autoPageviews !== false;
    this.taggedEvents = opts.taggedEvents !== false;
    this.ready = true;
    this.log("init", { key: this.key, identity: this.identified ? "identified" : "anonymous", kind: this.kind });
    // Persist what identify()/group() set before init(), now that the
    // mode and the driver are known.
    this.saveIdentity();
    this.replay();
    runtime.subscribe(this);
    if (this.autoPageviews && typeof location !== "undefined" && typeof history !== "undefined") {
      // Synchronous, deliberately: an app that navigates during hydration
      // must see the entry pageview fire BEFORE its pushState.
      this.page();
    }
    const held = this.held;
    this.held = [];
    for (const call of held) call();
    return this;
  }

  /**
   * $page_view (or $screen_view for a non-web kind), deduped against the
   * previous path. page() records the current page; page("/settings") an
   * explicit path; page({section: "docs"}) the current page with extra
   * attributes. page(fn) is deprecated: use onPage(fn).
   */
  page(arg?: string | Record<string, unknown> | PageListener | null, attrs?: Record<string, unknown>): void {
    if (typeof arg === "function") {
      console.warn("twillingate: page(fn) is deprecated and will be removed; use onPage(fn)");
      this.onPage(arg);
      return;
    }
    if (!this.ready) return this.hold(() => this.page(arg, attrs));
    if (!this.live()) return;

    let href: string;
    // The dedup key is always the RAW location. Keying on the masked path
    // would collapse /account/1 -> /account/2 into a single pageview.
    let rawKey: string;
    if (typeof arg === "string" && arg !== "") {
      try {
        href = new URL(arg, location.href).href;
      } catch {
        href = arg;
      }
      rawKey = arg;
    } else {
      if (arg && typeof arg === "object") attrs = { ...arg, ...attrs };
      href = location.href;
      rawKey = location.pathname + location.search + (this.routing === "hash" ? location.hash : "");
    }
    if (rawKey === this.lastPage) return;
    this.lastPage = rawKey;

    // Campaign parameters come off the ORIGINAL href: a mask that strips
    // the query must not cost attribution.
    const utm = utmFrom(href);

    if (this.mask === null) return; // configured but unresolvable: fail closed
    let masked = href;
    if (this.mask) {
      try {
        masked = this.mask(href);
      } catch (e) {
        console.warn("twillingate: mask threw, dropping pageview", e);
        return;
      }
      if (typeof masked !== "string") {
        console.warn("twillingate: mask returned a non-string, dropping pageview");
        return;
      }
    }

    const split = splitLocation(masked, this.routing);
    if (!split) {
      console.warn(`twillingate: mask produced an unusable url, dropping pageview: ${masked}`);
      return;
    }
    let { host, path } = split;
    const referrer = typeof document !== "undefined" ? document.referrer : "";

    // derived < attrs() defaults < the call's own attributes
    let attributes: Record<string, unknown> = {
      $host: host, $path: path, $referrer: referrer, ...utm, ...displaySize(),
      ...this.defaultAttrs, ...attrs,
    };
    if (typeof attributes.$host === "string") host = attributes.$host;
    if (typeof attributes.$path === "string") path = attributes.$path;
    for (const listener of this.pageListeners) {
      let r: ReturnType<PageListener>;
      try {
        r = listener({ url: masked, host, path, referrer, attributes });
      } catch (e) {
        console.warn("twillingate: an onPage listener threw, dropping pageview", e);
        return;
      }
      if (r === false) return;
      if (r && typeof r === "object") {
        attributes = { ...attributes, ...r };
        if (typeof r.$host === "string") host = r.$host;
        if (typeof r.$path === "string") path = r.$path;
      }
    }
    if (typeof attributes.$path !== "string" || attributes.$path === "") {
      console.warn("twillingate: a listener produced no $path, dropping pageview");
      return;
    }
    this.firstPageviewSent = true;
    if (this.kind === "web") {
      this.emit("$page_view", attributes);
      return;
    }
    // A non-web kind is an app: the route is the screen, and the page
    // context (host, referrer, campaign) does not apply.
    const { $host: _h, $referrer: _r, $utm_source: _s, $utm_medium: _m, $utm_campaign: _c, $path, ...rest } = attributes;
    this.emit("$screen_view", { $screen: $path, ...rest });
  }

  /** Register a pageview listener; runs for every pageview, automatic ones included. */
  onPage(fn: PageListener): void {
    if (this.firstPageviewSent) {
      console.warn(
        "twillingate: onPage listener registered after the first pageview; it will not apply to that one. " +
          "Use data-mask-url for the entry page, or register from code before init().",
      );
    }
    this.pageListeners.push(fn);
  }

  /** Register a listener for every event; runs after onPage for a pageview. */
  onEvent(fn: EventListener): void {
    this.eventListeners.push(fn);
  }

  /** App analytics $screen_view. */
  screen(name: string, attrs?: Record<string, unknown>): void {
    if (!this.ready) return this.hold(() => this.screen(name, attrs));
    if (!this.live() || !name) return;
    this.emit("$screen_view", { ...displaySize(), ...this.defaultAttrs, $screen: String(name), ...attrs });
  }

  /** Opt-in product event. */
  track(name: string, attrs?: Record<string, unknown>): void {
    if (!this.ready) return this.hold(() => this.track(name, attrs));
    if (!this.live() || !name) return;
    this.emit(String(name), { ...this.defaultAttrs, ...attrs });
  }

  /**
   * Default attributes under every event: they override values the SDK
   * derives and are overridden by the call's own. Successive calls merge;
   * attrs(null) clears them all.
   */
  attrs(attrs: Record<string, unknown> | null): void {
    if (attrs === null) {
      this.defaultAttrs = {};
      return;
    }
    this.defaultAttrs = { ...this.defaultAttrs, ...attrs };
  }

  /**
   * Set the user ($user_id) and optional display name ($user_name).
   * Persisted for an identified instance with consent. Inert on an
   * anonymous instance. Events already sent stay unattributed.
   */
  identify(user: string, name?: string): void {
    if (this.ready && !this.identified) return this.warnAnonymous("identify()");
    this.userId = user ? String(user) : null;
    this.userName = name ? String(name) : null;
    this.saveIdentity();
  }

  /** Set the group ($group_id) and optional display name ($group_name). Every mode. */
  group(id: string, name?: string): void {
    this.groupId = id ? String(id) : null;
    this.groupName = name ? String(name) : null;
    this.saveIdentity();
  }

  /**
   * With an argument, set the stable per-install id an app supplies as
   * $install_id; without one, read what would be sent: the declared id,
   * else the persisted visitor id, else null. Inert on an anonymous
   * instance.
   */
  installId(id?: string | null): string | null {
    if (id !== undefined) {
      if (this.ready && !this.identified) {
        this.warnAnonymous("installId()");
        return null;
      }
      this.declaredInstallId = id ? String(id) : null;
    }
    if (this.declaredInstallId) return this.declaredInstallId;
    return this.identified && this.mayStore() ? this.driver.get(this.k.visitor) : null;
  }

  /**
   * Required on logout: without it the next person on a shared browser
   * inherits the previous user's identity. Also drops the retry queue in
   * every mode — a queued batch carries the $user_id it was built with.
   */
  reset(): void {
    this.userId = null;
    this.userName = null;
    this.groupId = null;
    this.groupName = null;
    this.pending = [];
    if (this.mayStore()) this.wipe();
  }

  /** Force-send everything queued. */
  flush(): void {
    if (!this.ready) return this.hold(() => this.flush());
    this.drain(false);
  }

  /**
   * Pin storage consent (true/false) over whatever the tag declared, hand
   * control back (null), or read the effective value (no argument).
   */
  consent(granted?: boolean | null): boolean {
    if (granted !== undefined) this.consentPin = granted === null ? null : Boolean(granted);
    return this.mayStore();
  }

  /**
   * With a boolean, write or clear the twillingate_ignore flag; returns
   * the effective state, the optOut callback included.
   */
  optOut(flag?: boolean): boolean {
    if (flag !== undefined) writeFlag(IGNORE_FLAG, Boolean(flag));
    return readFlag(IGNORE_FLAG) || this.optOutSpec();
  }

  /** With a boolean, write or clear the twillingate_debug flag; returns the effective state. */
  debug(flag?: boolean): boolean {
    if (flag !== undefined) writeFlag(DEBUG_FLAG, Boolean(flag));
    return this.debugging();
  }

  /** Pure detection, exposed so a page can see what the SDK would send. */
  detectOS(signals?: ClientSignals): OSInfo {
    return detectOSFrom(signals);
  }

  detectBrowser(signals?: ClientSignals): BrowserInfo {
    return detectBrowserFrom(signals);
  }

  detectDevice(signals?: ClientSignals): DeviceInfo {
    return detectDeviceFrom(signals);
  }

  // ---- runtime subscriber: the instance decides, the runtime subscribes ----

  onNavigate(source: NavigationSource): void {
    if (!this.ready || !this.autoPageviews) return;
    // In history mode a hash change is an in-page anchor jump, not a route.
    if (source === "hash" && this.routing !== "hash") return;
    this.page();
  }

  onOnline(): void {
    if (this.ready) this.replay();
  }

  onUnload(): void {
    if (this.ready) this.drain(true);
  }

  onTagged(name: string, path: string): void {
    if (this.ready && this.taggedEvents) this.track(name, { path });
  }

  /**
   * Stop this instance for good: it leaves its runtime and produces no
   * more events. Called by bootstrap on a default instance a later tag
   * with a different key has taken over. Internal; not part of the
   * documented API.
   */
  retire(): void {
    // A dormant copy was never subscribed and an app may still hold a
    // reference to it (e.g. a keyed tag took over before this one's own
    // init() ran); it must stay usable rather than being retired sight
    // unseen.
    if (!this.ready) return;
    runtime.unsubscribe(this);
    this.retired = true;
  }

  // ---- internals ----

  private label(): string {
    return this.name === DEFAULT_INSTANCE ? "twillingate" : `twillingate:${this.name}`;
  }

  private debugging(): boolean {
    return this.debugOpt || readFlag(DEBUG_FLAG);
  }

  private log(msg: string, data?: unknown): void {
    if (!this.debugging()) return;
    if (data === undefined) console.log(`[${this.label()}] ${msg}`);
    else console.log(`[${this.label()}] ${msg}`, data);
  }

  private warnAnonymous(what: string): void {
    if (this.warnedAnonymous) return;
    this.warnedAnonymous = true;
    console.warn(`twillingate: ${what} does nothing on an anonymous instance; set identity: "identified" to send ids`);
  }

  private hold(call: () => void): void {
    if (this.held.length >= MAX_HELD) {
      this.held.shift();
      if (!this.heldWarned) {
        this.heldWarned = true;
        console.warn(`twillingate: ${this.label()} has ${MAX_HELD} calls waiting for init(); dropping the oldest`);
      }
    }
    this.held.push(call);
  }

  // Whether an event may be produced now: initialised, and neither the
  // person's opt-out flag nor the site's optOut callback says no.
  private live(): boolean {
    return this.ready && !this.retired && !readFlag(IGNORE_FLAG) && !this.optOutSpec();
  }

  // The last layer: onEvent listeners, then null drops a key.
  private emit(name: string, merged: Record<string, unknown>): void {
    for (const listener of this.eventListeners) {
      let r: ReturnType<EventListener>;
      try {
        r = listener({ name, attributes: merged });
      } catch (e) {
        console.warn(`twillingate: an onEvent listener threw, dropping ${name}`, e);
        return;
      }
      if (r === false) return;
      if (r && typeof r === "object") merged = { ...merged, ...r };
    }
    const attributes: Record<string, unknown> = {};
    for (const key of Object.keys(merged)) {
      if (merged[key] !== null && merged[key] !== undefined) attributes[key] = merged[key];
    }
    this.log(name, attributes);
    this.queue.push({ id: uuid(), ts: new Date().toISOString(), name, attributes });
    if (this.queue.length >= FLUSH_AT) {
      this.drain(false);
      return;
    }
    if (this.flushTimer === null) {
      this.flushTimer = setTimeout(() => {
        this.flushTimer = null;
        this.drain(false);
      }, this.flushInterval);
    }
  }

  private drain(unloading: boolean): void {
    if (this.flushTimer !== null) {
      clearTimeout(this.flushTimer);
      this.flushTimer = null;
    }
    while (this.queue.length > 0) {
      const events = this.queue.splice(0, MAX_BATCH);
      this.send({ key: this.key, attributes: this.batchAttributes(), events }, unloading);
    }
    // Unloading is the last chance for batches whose delivery failed: try
    // each once more through sendBeacon. A batch the beacon accepts is
    // retired from pending; the stored copy stays until a replay lands it.
    if (unloading) {
      this.pending = this.pending.filter((batch) => !this.send(batch, true));
    }
  }

  // Identity precedence: the declared install id, else the stored visitor
  // id (identified, with consent), else nothing — the server then falls
  // back to its rotating connection hash.
  private visitorId(): string | null {
    if (this.declaredInstallId) return this.declaredInstallId;
    if (!this.identified || !this.mayStore()) return null;
    let v = this.driver.get(this.k.visitor);
    if (!v) {
      v = uuid();
      this.driver.set(this.k.visitor, v);
    }
    return v;
  }

  private batchAttributes(): Record<string, unknown> {
    const a: Record<string, unknown> = {};
    if (this.identified) {
      if (this.userId) a.$user_id = this.userId;
      if (this.userName) a.$user_name = this.userName;
      const v = this.visitorId();
      if (v) a.$install_id = v;
    }
    if (this.groupId) a.$group_id = this.groupId;
    if (this.groupName) a.$group_name = this.groupName;
    a.$kind = this.kind;
    // The answer the storage code acts on, read at flush time, so the
    // server can tell consented rows from the rest. Sent in both identity
    // modes: an anonymous instance's retry queue is still gated on it.
    a.$consent = this.mayStore() ? 1 : 0;
    if (this.platform) a.$platform = this.platform;
    // Detection runs per flush and is the only source of the environment.
    const d = detectAll();
    a.$os = d.os;
    if (d.osVersion) a.$os_version = d.osVersion;
    if (d.osName) a.$os_name = d.osName;
    a.$browser = d.browser;
    if (d.browserVersion) a.$browser_version = d.browserVersion;
    a.$device = d.device;
    if (this.appVersion) a.$app_version = this.appVersion;
    if (typeof navigator !== "undefined" && navigator.language) a.$locale = navigator.language;
    return a;
  }

  // Returns whether sendBeacon accepted the batch.
  private send(batch: Batch, unloading: boolean): boolean {
    const endpoint = this.url + "/ingest/events";
    const body = JSON.stringify(batch);
    if (unloading && typeof navigator !== "undefined" && typeof navigator.sendBeacon === "function" && navigator.sendBeacon(endpoint, body)) {
      this.log(`beaconed ${batch.events.length} event(s)`);
      return true;
    }
    fetch(endpoint, { method: "POST", body, keepalive: true })
      .then((res) => {
        this.log(`sent ${batch.events.length} event(s) → ${res.status}`);
        // 5xx is transient — keep the batch for replay. 4xx is permanent.
        if (res.status >= 500) this.store(batch);
      })
      .catch((e) => {
        this.log(`send failed, keeping ${batch.events.length} event(s) for retry`, e);
        this.store(batch);
      });
    return false;
  }

  // The decision point every read and write goes through. Consent is
  // consulted here, never cached. A change of answer is a transition:
  // granted moves the pending queue onto the driver; withdrawn deletes
  // everything this instance wrote. Before init() there is no configured
  // driver yet (this.driver is still the default), so a bare read (e.g.
  // consent() called ahead of init()) reports the effective value without
  // running the transition against it.
  private mayStore(): boolean {
    const now = this.consentPin !== null ? this.consentPin : this.consentSpec();
    if (this.ready && now !== this.lastConsent) {
      this.lastConsent = now;
      if (now) {
        if (this.pending.length) this.driver.set(this.k.queue, JSON.stringify(mergeBatches(this.storedBatches(), this.pending)));
        this.saveIdentity();
      } else {
        this.wipe();
      }
    }
    return now;
  }

  private wipe(): void {
    for (const s of SUFFIXES) this.driver.remove(this.k[s]);
  }

  private saveIdentity(): void {
    if (!this.identified || !this.mayStore()) return;
    const put = (key: string, value: string | null) => (value === null ? this.driver.remove(key) : this.driver.set(key, value));
    put(this.k.user, this.userId);
    put(this.k.user_name, this.userName);
    put(this.k.group, this.groupId);
    put(this.k.group_name, this.groupName);
  }

  private store(batch: Batch): void {
    if (this.pending.includes(batch)) return; // an unload retry that failed again
    this.pending.push(batch);
    while (this.pending.length > MAX_STORED_BATCHES) this.pending.shift();
    this.saveQueue();
  }

  private saveQueue(): void {
    if (!this.mayStore()) return;
    if (this.pending.length) this.driver.set(this.k.queue, JSON.stringify(this.pending));
    else this.driver.remove(this.k.queue);
  }

  private storedBatches(): Batch[] {
    const raw = this.driver.get(this.k.queue);
    if (!raw) return [];
    try {
      const parsed: unknown = JSON.parse(raw);
      return Array.isArray(parsed) ? parsed.filter(isBatch) : [];
    } catch {
      return [];
    }
  }

  private replay(): void {
    const batches = this.mayStore() ? mergeBatches(this.storedBatches(), this.pending) : this.pending;
    this.pending = [];
    this.saveQueue();
    for (const batch of batches) this.send(batch, false); // a failure re-stores itself
  }
}

/** Campaign parameters, read from the original href before any masking. */
function utmFrom(href: string): Record<string, string> {
  const out: Record<string, string> = {};
  let params: URLSearchParams;
  try {
    params = new URL(href, "http://localhost").searchParams;
  } catch {
    return out;
  }
  for (const [param, key] of [
    ["utm_source", "$utm_source"],
    ["utm_medium", "$utm_medium"],
    ["utm_campaign", "$utm_campaign"],
  ] as const) {
    const v = params.get(param);
    if (v) out[key] = v;
  }
  return out;
}

/** Physical display size in pixels, when the runtime exposes one. */
function displaySize(): Record<string, number> {
  if (typeof screen === "undefined" || !screen.width || !screen.height) return {};
  return { $display_width: screen.width, $display_height: screen.height };
}

/**
 * Split a URL into the host and path that get stored. The query is always
 * dropped; the hash is kept only in hash-routing mode, where it IS the
 * route.
 */
function splitLocation(href: string, routing: "history" | "hash"): { host: string; path: string } | null {
  let u: URL;
  try {
    u = new URL(href, location.href);
  } catch {
    return null;
  }
  let path = u.pathname || "/";
  if (routing === "hash" && u.hash) {
    path += "#" + u.hash.slice(1).split("?")[0];
  }
  return { host: u.hostname, path };
}
