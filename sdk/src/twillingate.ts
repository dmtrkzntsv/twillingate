/* twillingate SDK core — views and product analytics against the
 * collector's POST /ingest/events (docs/twillingate.md is the normative wire
 * format). Bundled as an IIFE by build.mjs and served at /js/twillingate.js.
 *
 * Two usage modes:
 *  - snippet: <script defer src=".../js/twillingate.js" data-key="ak_…">
 *    auto-inits from data attributes with automatic page views;
 *  - SDK-only: load the file without data-key (or bundle this module) and
 *    call twillingate.init({...}) yourself — a superset that emits views
 *    and product events entirely from code.
 *
 * Nothing is kept on the device unless the tag declares consent
 * (data-consent); with it, data-identity decides whether identity persists.
 * The server salts anonymous projects no matter what the client claims, so
 * a misconfigured client fails safe.
 */

// Substituted by the collector at serve time with its build version.
import { resolveMask, type MaskSpec } from "./mask";
import { resolveConsent, type ConsentSpec } from "./consent";
import { maskIds, withQuery } from "./util";
import {
  detectAll, detectBrowser as detectBrowserFrom, detectDevice as detectDeviceFrom,
  detectOS as detectOSFrom, primePlatformVersion, type BrowserInfo, type ClientSignals, type DeviceInfo, type OSInfo,
} from "./detect";

export { detectOS, detectBrowser, detectDevice } from "./detect";
export type { ClientSignals, OSInfo, BrowserInfo, DeviceInfo } from "./detect";

export const VERSION = "__TWILLINGATE_VERSION__";

export interface InitOptions {
  /** Ingest key (ak_…). Required. */
  key: string;
  /** Collector base URL. Defaults to the origin the script was loaded from. */
  url?: string;
  /**
   * Mirrors the project's identity mode. With consent it decides whether
   * the visitor id, user and group persist; without consent nothing does.
   * The server enforces the real mode.
   */
  identity?: "anonymous" | "identified";
  user?: string;
  group?: string;
  /**
   * May this instance keep anything on the device. Default false: records
   * live in memory and nothing is read from or written to localStorage.
   * true, or a function or global name a consent manager maintains,
   * unlocks it; consulted at every storage decision, never cached.
   */
  consent?: ConsentSpec;
  /**
   * Storage-key prefix for a bundled consumer that shares a page with
   * another instance. Registers no global. A tag that declared
   * data-instance ignores a disagreeing value here.
   */
  instance?: string;
  /**
   * What this client is: "web" (default), "app", "cli", or any short
   * lower-case token. Anything but "web" makes automatic tracking emit
   * $screen_view with the route path as the screen, and exempts the
   * client from the server's crawler filter, which applies to web only.
   */
  kind?: string;
  /**
   * The surface the product is used through ($platform): "web", "ios",
   * "android", "electron", … Defaults to "web" while kind is "web". Any
   * other kind is a wrapper the SDK cannot identify, so it sends no
   * $platform and the server records unknown — set it beside kind.
   */
  platform?: string;
  /**
   * Overrides for detection. Every detected value has one, and an
   * explicit option always beats detection. os, browser and device are
   * closed lower-case vocabularies (docs/twillingate.md); osName is the
   * full self-reported name with version.
   */
  os?: string;
  osVersion?: string;
  osName?: string;
  browser?: string;
  browserVersion?: string;
  device?: string;
  /** Version of this client application ($app_version). */
  appVersion?: string;
  installId?: string;
  /**
   * Automatic pageviews incl. pushState/popstate. Snippet mode defaults to
   * true; explicit init() defaults to false — turning it on is a deliberate
   * choice when instrumenting a SPA through the API.
   */
  autoPageviews?: boolean;
  /**
   * Rewrite the URL before it is split into $host and $path. Accepts the
   * same strings as data-mask-url ("uuid", "uuid,numeric", "/re/flags", or
   * a global function name) plus a RegExp or a function directly.
   *
   * A mask that cannot be resolved, throws, or returns a non-string DROPS
   * pageviews. Shipping the raw path would defeat the point of masking.
   */
  maskUrl?: MaskSpec;
  /**
   * "history" (default) or "hash". In hash mode $path is pathname + hash
   * and hashchange emits a pageview; in history mode a hash change is an
   * in-page anchor and is ignored.
   */
  routing?: "history" | "hash";
  /** Milliseconds events wait in the queue before a flush. */
  flushInterval?: number;
}

/**
 * What a page listener sees for each pageview, automatic or manual.
 *
 * host and path are THREADED: each listener receives the previous
 * listener's output, so rules split across several page() calls compose
 * instead of clobbering each other. url is the post-mask URL -- handing
 * over the raw href would let a mask scrub a parameter and then leak it
 * straight back through a listener.
 */
export interface PageviewInfo {
  url: string;
  host: string;
  path: string;
  referrer: string;
  attributes: Record<string, unknown>;
}

/**
 * Registered via page(fn). Return an object to merge extra attributes into
 * the pageview, false to cancel it, anything else to just observe.
 */
export type PageListener = (page: PageviewInfo) => Record<string, unknown> | false | void;

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

// Union of a stored queue and the in-memory one, deduped by each batch's
// first event id (every batch has at least one event). Neither side is
// assumed complete: a write can fail silently (lsSet swallows quota and
// partitioned-storage errors) and another tab can append its own batches,
// so the merge keeps whichever copy already has each batch and adds what
// the other one has that it does not.
function mergeBatches(stored: Batch[], pending: Batch[]): Batch[] {
  const known = new Set(stored.map((b) => b.events[0].id));
  return [...stored, ...pending.filter((b) => !known.has(b.events[0].id))];
}

// Storage keys are prefixed with the instance name (default "twillingate")
// so two instances on one page do not share a visitor id or a queue. The
// opt-out is the one unprefixed key: it is about the person, not one tag.
const DEFAULT_INSTANCE = "twillingate";
const IGNORE = "twillingate_ignore";
const SUFFIXES = ["visitor", "user", "user_name", "group", "group_name", "queue"] as const;
type Keys = Record<(typeof SUFFIXES)[number], string>;

function keysFor(instance: string): Keys {
  const k = {} as Keys;
  for (const s of SUFFIXES) k[s] = `${instance}_${s}`;
  return k;
}

const INSTANCE_RE = /^[a-z][a-z0-9_]{0,15}$/;

/**
 * Validate an instance name. It becomes a property on window and a
 * storage-key prefix, so it has to be an identifier (the same shape as
 * $kind). An invalid name is refused with a warning and the default kept.
 */
export function instanceName(name: string | null | undefined): string {
  if (name === null || name === undefined || name === "") return DEFAULT_INSTANCE;
  if (INSTANCE_RE.test(name)) return name;
  console.warn(`twillingate: instance name ${JSON.stringify(name)} is not an identifier; using "${DEFAULT_INSTANCE}"`);
  return DEFAULT_INSTANCE;
}

// Two ways to recognize "this is a twillingate instance": same-bundle
// identity (instanceof, what a test constructing Twillingate directly
// produces) or the cross-release marker every shipped bundle stamps on the
// global (VERSION, a string) alongside init(). init() alone is not enough —
// plenty of unrelated globals (Segment's analytics.js among them) expose an
// init() method, and duck-typing on that would let bootstrap either
// overwrite a foreign global or, worse, treat it as a loaded copy of this
// SDK and refuse to run at all.
function isInstance(x: unknown): x is Twillingate {
  return (
    x instanceof Twillingate ||
    (!!x && typeof (x as Twillingate).init === "function" && typeof (x as { VERSION?: unknown }).VERSION === "string")
  );
}

const MAX_BATCH = 500; // server cap per docs/twillingate.md
const FLUSH_AT = 20; // flush early once this many events queue up
const MAX_STORED_BATCHES = 50; // offline queue bound: oldest dropped first

function ls(name: string): string | null {
  try {
    return localStorage.getItem(name);
  } catch {
    return null;
  }
}

function lsSet(name: string, value: string | null): void {
  try {
    if (value === null) localStorage.removeItem(name);
    else localStorage.setItem(name, value);
  } catch {
    /* storage unavailable: identity simply does not persist */
  }
}

function uuid(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16);
  });
}

// The only thing the SDK decides on its own is the person's opt-out.
// Whether analytics should run at all (a developer's localhost, a test
// browser) belongs to the product, which can skip init() on a condition
// it knows.
function ignored(): boolean {
  return ls(IGNORE) === "true";
}

export class Twillingate {
  private key = "";
  private url = "";
  private identified = false;
  private userId: string | null = null;
  private userName: string | null = null;
  private groupId: string | null = null;
  private groupName: string | null = null;
  private defaultAttrs: Record<string, unknown> = {};
  private pageListeners: PageListener[] = [];
  private kind = "web";
  private platform: string | null = null;
  private env: Partial<OSInfo & BrowserInfo & DeviceInfo> = {};
  private appVersion: string | null = null;
  private installId: string | null = null;
  private flushInterval = 1000;
  private k: Keys = keysFor(DEFAULT_INSTANCE);
  private consentSpec: () => boolean = () => false;
  private consentPin: boolean | null = null;
  // null until the first decision point: the first false read wipes keys
  // an earlier session may have left behind.
  private lastConsent: boolean | null = null;
  // Failed batches waiting for a retry. Lives in memory; mirrored to
  // storage only while consent reads true.
  private pending: Batch[] = [];

  private queue: Event[] = [];
  private flushTimer: ReturnType<typeof setTimeout> | null = null;
  private lastPage: string | null = null;
  private hooked = false;
  // undefined = no mask configured; null = one was configured and could
  // not be resolved, which drops pageviews (fail closed).
  private mask: ((href: string) => string) | null | undefined;
  private routing: "history" | "hash" = "history";
  private firstPageviewSent = false;

  /** Path-shaping helpers, for use inside a page() listener. */
  readonly util = { maskIds, withQuery };
  private ready = false;

  private instance = DEFAULT_INSTANCE;
  // True when the name came from data-instance: the tag is registered
  // under it and already reading prefixed keys, so init() cannot rename.
  private declared = false;

  constructor(instance?: string) {
    if (instance !== undefined) {
      this.declared = true;
      this.useInstance(instanceName(instance));
    }
  }

  private useInstance(name: string): void {
    this.instance = name;
    this.k = keysFor(name);
  }

  init(opts: InitOptions): void {
    if (!opts || !opts.key) {
      console.warn("twillingate: init requires a key");
      return;
    }
    this.key = opts.key;
    this.url = (opts.url || scriptOrigin() || "").replace(/\/$/, "");
    if (!this.url) {
      console.warn("twillingate: init requires a url when not loaded via <script>");
      return;
    }
    if (opts.instance !== undefined) {
      if (this.declared) {
        if (opts.instance !== this.instance) {
          console.warn(`twillingate: data-instance="${this.instance}" is already set; ignoring instance "${opts.instance}"`);
        }
      } else {
        this.useInstance(instanceName(opts.instance));
      }
    }
    this.identified = opts.identity === "identified";
    this.consentSpec = resolveConsent(opts.consent);
    const stored = this.identified && this.mayStore();
    this.userId = opts.user ? String(opts.user) : stored ? ls(this.k.user) : null;
    this.userName = stored ? ls(this.k.user_name) : null;
    this.groupId = opts.group ? String(opts.group) : stored ? ls(this.k.group) : null;
    this.groupName = stored ? ls(this.k.group_name) : null;
    this.kind = opts.kind && /^[a-z][a-z0-9_]{0,15}$/.test(opts.kind) ? opts.kind : "web";
    this.platform = opts.platform || (this.kind === "web" ? "web" : null);
    this.env = {
      os: opts.os || undefined, osVersion: opts.osVersion || undefined, osName: opts.osName || undefined,
      browser: opts.browser || undefined, browserVersion: opts.browserVersion || undefined,
      device: opts.device || undefined,
    };
    // The one async detection input; read at flush time, not awaited.
    primePlatformVersion();
    this.appVersion = opts.appVersion || null;
    this.installId = opts.installId || null;
    if (opts.flushInterval !== undefined) this.flushInterval = opts.flushInterval;
    // Resolved before any pageview can fire, so data-mask-url covers the
    // entry page -- the one most likely to carry an identifier.
    this.mask = resolveMask(opts.maskUrl);
    this.routing = opts.routing === "hash" ? "hash" : "history";
    this.ready = true;

    this.replay();
    if (typeof addEventListener === "function") {
      addEventListener("online", () => this.replay());
      // pagehide covers navigations and tab closes; visibilitychange the
      // mobile cases where pagehide never fires. Both drain via sendBeacon.
      addEventListener("pagehide", () => this.flush(true));
      document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "hidden") this.flush(true);
      });
    }
    if (opts.autoPageviews) {
      this.hookHistory();
      // Synchronous, deliberately. Deferring this by a tick would let a
      // listener registered from an inline module still affect it, but an
      // app that navigates during hydration would then have the entry
      // pageview fire AFTER its pushState -- reporting the wrong location
      // and deduping against it. Masking the entry page is data-mask-url's
      // job (resolved above, before this line); a listener registered too
      // late gets a warning from page() instead.
      this.page();
    }
  }

  /**
   * $page_view (or $screen_view for a non-web kind), deduped against the
   * previous path. Overloads:
   *
   *   page()                  — record the current page
   *   page("/settings")       — record an explicit path
   *   page({section: "docs"}) — current page with extra attributes
   *   page(fn)                — register a PageListener called for every
   *                             pageview (automatic ones included); it can
   *                             enrich attributes or cancel the event
   */
  page(arg?: string | Record<string, unknown> | PageListener | null, attrs?: Record<string, unknown>): void {
    if (typeof arg === "function") {
      if (this.firstPageviewSent) {
        console.warn(
          "twillingate: page() listener registered after the first pageview; " +
            "it will not apply to that one. Register it from an inline " +
            '<script type="module"> placed after the tag.',
        );
      }
      this.pageListeners.push(arg);
      return;
    }
    if (!this.ok()) return;

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
      rawKey =
        location.pathname + location.search +
        (this.routing === "hash" ? location.hash : "");
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

    let attributes: Record<string, unknown> = {
      $host: host, $path: path, $referrer: document.referrer, ...utm, ...attrs,
    };
    for (const listener of this.pageListeners) {
      const r = listener({ url: masked, host, path, referrer: document.referrer, attributes });
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
      this.emit("$page_view", { ...attributes, ...displaySize() });
      return;
    }
    // A non-web kind is an app: the route is the screen, and the page
    // context (host, referrer, campaign) does not apply.
    const { $host: _h, $referrer: _r, $utm_source: _s, $utm_medium: _m, $utm_campaign: _c, $path, ...rest } = attributes;
    this.emit("$screen_view", { $screen: $path, ...rest, ...displaySize() });
  }

  /** App analytics $screen_view. */
  screen(name: string, attrs?: Record<string, unknown>): void {
    if (!this.ok() || !name) return;
    this.emit("$screen_view", { $screen: String(name), ...displaySize(), ...attrs });
  }

  /** Opt-in product event. */
  track(name: string, attrs?: Record<string, unknown>): void {
    if (!this.ok() || !name) return;
    this.emit(String(name), attrs || {});
  }

  /**
   * Default attributes merged under every event's own (event attributes
   * win). Successive calls merge; attrs(null) clears them all.
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
   * Persisted for identified projects with consent, so every later event —
   * this page and future loads — carries the identity. Events already sent
   * stay unattributed: no retroactive stitching. The name is only stored
   * server-side for identified projects; anonymous ones ignore it.
   */
  identify(user: string, name?: string): void {
    this.userId = user ? String(user) : null;
    this.userName = name ? String(name) : null;
    this.saveIdentity();
  }

  /** Set the group ($group_id) and optional display name ($group_name). */
  group(id: string, name?: string): void {
    this.groupId = id ? String(id) : null;
    this.groupName = name ? String(name) : null;
    this.saveIdentity();
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
  flush(unloading = false): void {
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
    // retired from pending (and, with consent, from storage); one it does
    // not accept stays for the next retry — visibilitychange fires this on
    // every tab switch, not only on the final pagehide, so an unbounded
    // re-beacon of the same accepted batches would otherwise follow.
    if (unloading) {
      this.pending = this.pending.filter((batch) => !this.send(batch, true));
      this.saveQueue();
    }
  }

  /**
   * Pin storage consent (true/false) over whatever the tag declared, hand
   * control back to the declared value (null), or read the effective value
   * (no argument). Granting writes the pending retry queue to storage and,
   * for an identified instance, starts persisting a visitor id; withdrawing
   * deletes every key this instance owns. Every call, including a bare
   * read, is itself a decision point and may run that grant/withdraw
   * transition.
   */
  consent(granted?: boolean | null): boolean {
    if (granted !== undefined) this.consentPin = granted === null ? null : Boolean(granted);
    return this.mayStore();
  }

  /**
   * Pure detection, exposed so a page can see what the SDK would send:
   * twillingate.detectOS() in the console. Omit the argument to read the
   * ambient navigator; pass a ClientSignals to answer for exactly those
   * signals and nothing else. Overrides given to init() are deliberately
   * ignored here — this answers for the signals, the batch carries the
   * option.
   */
  detectOS(signals?: ClientSignals): OSInfo {
    return detectOSFrom(signals);
  }

  detectBrowser(signals?: ClientSignals): BrowserInfo {
    return detectBrowserFrom(signals);
  }

  detectDevice(signals?: ClientSignals): DeviceInfo {
    return detectDeviceFrom(signals);
  }

  private ok(): boolean {
    if (!this.ready) {
      console.warn("twillingate: not initialised; call twillingate.init first");
      return false;
    }
    return !ignored();
  }

  private emit(name: string, attributes: Record<string, unknown>): void {
    // A null or undefined value drops the key: the way to suppress a value
    // the SDK derives on its own ($referrer) for one call. Applies to the
    // event's attributes and attrs() defaults; batch attributes are
    // untouched and the server layers the event over them key by key.
    const merged: Record<string, unknown> = { ...this.defaultAttrs, ...attributes };
    attributes = {};
    for (const key of Object.keys(merged)) {
      if (merged[key] !== null && merged[key] !== undefined) attributes[key] = merged[key];
    }
    this.queue.push({ id: uuid(), ts: new Date().toISOString(), name, attributes });
    if (this.queue.length >= FLUSH_AT) {
      this.flush();
      return;
    }
    if (this.flushTimer === null) {
      this.flushTimer = setTimeout(() => {
        this.flushTimer = null;
        this.flush();
      }, this.flushInterval);
    }
  }

  // Identity precedence: a caller-supplied id, else the stored visitor id,
  // else nothing — the server then falls back to its rotating hash. The
  // persistent visitor id is terminal-equipment storage under ePrivacy, so
  // it is only ever written for identified projects, and only with consent.
  private visitorId(): string | null {
    if (this.installId) return this.installId;
    if (!this.identified || !this.mayStore()) return null;
    let v = ls(this.k.visitor);
    if (!v) {
      v = uuid();
      lsSet(this.k.visitor, v);
    }
    return v;
  }

  private batchAttributes(): Record<string, unknown> {
    const a: Record<string, unknown> = {};
    if (this.userId) a.$user_id = this.userId;
    if (this.userName) a.$user_name = this.userName;
    if (this.groupId) a.$group_id = this.groupId;
    if (this.groupName) a.$group_name = this.groupName;
    const v = this.visitorId();
    if (v) a.$install_id = v;
    a.$kind = this.kind;
    // Detection runs per flush: an explicit option beats it, and the
    // three always-resolved values are sent on every batch so the server
    // can tell "declared unknown" from "sent nothing".
    const d = detectAll();
    const e = this.env;
    if (this.platform) a.$platform = this.platform;
    a.$os = e.os || d.os;
    const osVersion = e.osVersion || d.osVersion;
    if (osVersion) a.$os_version = osVersion;
    const osName = e.osName || d.osName;
    if (osName) a.$os_name = osName;
    a.$browser = e.browser || d.browser;
    const browserVersion = e.browserVersion || d.browserVersion;
    if (browserVersion) a.$browser_version = browserVersion;
    a.$device = e.device || d.device;
    if (this.appVersion) a.$app_version = this.appVersion;
    if (typeof navigator !== "undefined" && navigator.language) a.$locale = navigator.language;
    return a;
  }

  // Returns whether sendBeacon accepted the batch, so a caller retiring a
  // beaconed batch from pending (flush(true)) knows which ones landed.
  private send(batch: Batch, unloading: boolean): boolean {
    const endpoint = this.url + "/ingest/events";
    const body = JSON.stringify(batch);
    // sendBeacon with a string posts text/plain: a CORS-simple request with
    // no preflight that survives page unload. It cannot set headers, which
    // is why the key travels in the body.
    if (unloading && typeof navigator.sendBeacon === "function" && navigator.sendBeacon(endpoint, body)) {
      return true;
    }
    fetch(endpoint, { method: "POST", body, keepalive: true })
      .then((res) => {
        // 5xx is transient — keep the batch for replay. 4xx is permanent
        // (bad key, bad payload): dropping beats resending it forever.
        if (res.status >= 500) this.store(batch);
      })
      .catch(() => this.store(batch));
    return false;
  }

  // The decision point every read and write goes through. Consent is
  // consulted here, never cached, so a consent manager that answers after
  // page load is picked up at the next decision. A change of answer is a
  // transition: granted moves the pending queue onto the device; withdrawn
  // deletes everything this instance wrote.
  private mayStore(): boolean {
    const now = this.consentPin !== null ? this.consentPin : this.consentSpec();
    if (now !== this.lastConsent) {
      this.lastConsent = now;
      if (now) {
        // Merge rather than overwrite: another tab may already have
        // queued its own batches under this key.
        if (this.pending.length) lsSet(this.k.queue, JSON.stringify(mergeBatches(this.storedBatches(), this.pending)));
      } else {
        this.wipe();
      }
    }
    return now;
  }

  private wipe(): void {
    for (const s of SUFFIXES) lsSet(this.k[s], null);
  }

  private saveIdentity(): void {
    if (!this.identified || !this.mayStore()) return;
    lsSet(this.k.user, this.userId);
    lsSet(this.k.user_name, this.userName);
    lsSet(this.k.group, this.groupId);
    lsSet(this.k.group_name, this.groupName);
  }

  // Retry queue: failed batches keep the attributes they were built with
  // and replay on `online`, on unload, and (with consent) on the next load.
  // Events carry ids and client timestamps, so the server dedupes replays
  // and keeps the original times.
  private store(batch: Batch): void {
    if (this.pending.includes(batch)) return; // an unload retry that failed again
    this.pending.push(batch);
    while (this.pending.length > MAX_STORED_BATCHES) this.pending.shift();
    this.saveQueue();
  }

  private saveQueue(): void {
    if (!this.mayStore()) return;
    lsSet(this.k.queue, this.pending.length ? JSON.stringify(this.pending) : null);
  }

  private storedBatches(): Batch[] {
    const raw = ls(this.k.queue);
    if (!raw) return [];
    try {
      const parsed = JSON.parse(raw);
      return Array.isArray(parsed) ? parsed : [];
    } catch {
      return [];
    }
  }

  // With consent, replay the union of the stored copy and pending, deduped
  // by first event id: pending can hold a batch the store write silently
  // dropped (quota, a partitioned context), and the stored copy can hold a
  // batch only another tab knows about. Without consent nothing was ever
  // written, so pending alone is the whole queue.
  private replay(): void {
    const batches = this.mayStore() ? mergeBatches(this.storedBatches(), this.pending) : this.pending;
    this.pending = [];
    this.saveQueue();
    for (const batch of batches) this.send(batch, false); // a failure re-stores itself
  }

  private hookHistory(): void {
    if (this.hooked || typeof history === "undefined") return;
    this.hooked = true;
    const pushState = history.pushState;
    // eslint-style rebind: arrow keeps `this` on the SDK instance.
    history.pushState = (...args: Parameters<History["pushState"]>) => {
      pushState.apply(history, args);
      this.page();
    };
    addEventListener("popstate", () => this.page());
    // Hash mode only. In history mode a hash change is an in-page anchor
    // jump (#pricing), and treating those as pageviews would flood the
    // pages breakdown with duplicates of one route.
    if (this.routing === "hash") {
      addEventListener("hashchange", () => this.page());
    }
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
 * route. The "#" is retained so the client route /app/#/settings stays
 * distinguishable from the server route /app/settings, and the pathname
 * prefix is kept so two hash apps mounted at different paths stay apart.
 */
function splitLocation(
  href: string,
  routing: "history" | "hash",
): { host: string; path: string } | null {
  let u: URL;
  try {
    u = new URL(href, location.href);
  } catch {
    return null;
  }
  let path = u.pathname || "/";
  if (routing === "hash" && u.hash) {
    // Strip a hash-internal query: $path carries none by default, in
    // either mode.
    path += "#" + u.hash.slice(1).split("?")[0];
  }
  return { host: u.hostname, path };
}

function scriptOrigin(): string | null {
  if (typeof document === "undefined") return null;
  const script = document.currentScript as HTMLScriptElement | null;
  if (!script || !script.src) return null;
  try {
    return new URL(script.src).origin;
  } catch {
    return null;
  }
}

/**
 * Whether this copy of the bundle should stand down and leave the instance
 * already at window[name] in place. A page that gets the tag twice (a
 * theme and a tag manager both adding it) would otherwise run two instances
 * sending every pageview under one key, with the second replacing the
 * global the page's own calls go to.
 *
 * A tag naming a different key still takes over: two projects on one page
 * need separate storage, which a guard cannot give them.
 */
export function supersededBy(existing: unknown, script: HTMLScriptElement | null, name = DEFAULT_INSTANCE): boolean {
  if (!isInstance(existing)) return false;
  // Read structurally: a copy from another release is a different class.
  const loadedKey = (existing as unknown as { key?: unknown }).key;
  const key = script?.getAttribute("data-key");
  if (!key || key === loadedKey) {
    console.warn("twillingate: twillingate.js loaded twice; keeping the first copy, remove the duplicate <script> tag");
    return true;
  }
  if (loadedKey) {
    console.warn(`twillingate: a second twillingate.js replaced window.${name} (${loadedKey} -> ${key})`);
  }
  return false;
}

/**
 * Snippet-mode entry: init from the loading <script>'s data attributes.
 * Without data-key the SDK stays dormant until twillingate.init is called.
 */
export function autoInit(tg: Twillingate, script: HTMLScriptElement | null): void {
  if (!script) return;
  const key = script.getAttribute("data-key");
  if (!key) return;
  let url: string | null = null;
  try {
    url = script.src ? new URL(script.src).origin : null;
  } catch {
    url = null;
  }
  tg.init({
    key,
    url: url || undefined,
    identity: script.getAttribute("data-identity") === "identified" ? "identified" : "anonymous",
    user: script.getAttribute("data-user") || undefined,
    group: script.getAttribute("data-group") || undefined,
    consent: script.getAttribute("data-consent") || undefined,
    autoPageviews: script.getAttribute("data-auto") !== "off",
    maskUrl: script.getAttribute("data-mask-url") || undefined,
    routing: script.getAttribute("data-routing") === "hash" ? "hash" : "history",
    kind: script.getAttribute("data-kind") || undefined,
  });
}

/**
 * Snippet-mode bootstrap, called once by the bundle entry. Picks the
 * instance name from data-instance, stands down for a duplicate of the
 * same tag, registers the global and auto-inits. Returns the instance, or
 * null when this copy stood down. The global is registered only when the
 * name is free or holds an earlier Twillingate (the take-over path);
 * anything else stays untouched — data-instance="location" should cost a
 * warning, not the page.
 */
export function bootstrap(script: HTMLScriptElement | null): Twillingate | null {
  const attr = script ? script.getAttribute("data-instance") : null;
  const name = attr === null ? DEFAULT_INSTANCE : instanceName(attr);
  const g = window as unknown as Record<string, unknown>;
  const existing = g[name];
  if (supersededBy(existing, script, name)) return null;
  // An unusable attribute already fell back to the default inside
  // instanceName above; pass undefined rather than the resolved name so
  // the constructor does not mark this instance "declared" against a name
  // it never actually got from the tag, and a later init({ instance })
  // can still take effect.
  const tg = new Twillingate(name === DEFAULT_INSTANCE ? undefined : name);
  (tg as Twillingate & { VERSION: string }).VERSION = VERSION;
  if (existing === undefined || existing === null || isInstance(existing)) {
    g[name] = tg;
  } else {
    console.warn(`twillingate: window.${name} is already taken by something else; the instance is not registered there`);
  }
  autoInit(tg, script);
  return tg;
}
