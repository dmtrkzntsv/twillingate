# SDK global factory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One `window.twillingate` that is the default instance and a factory for named ones, browser hooks installed once and fanned out, calls legal before `init()`, the tag's identity mode enforced by the SDK, hooks and precedence for every event, storage drivers, opt-out and debug as methods and localStorage flags, tagged elements, the collector origin baked into the served file, and a trimmed option list.

**Architecture:** The SDK core (`Twillingate`) becomes one instance with a name; a `TwillingateGlobal` subclass adds the registry and is what the bundle registers at `window.twillingate`. A module-level `runtime` owns every browser subscription and dispatches to the instances that registered with it. Small pure modules hold the origin placeholder (`origin.ts`) and the storage drivers (`storage.ts`). The Go server substitutes `__TWILLINGATE_URL__` per request beside the version placeholder.

**Tech Stack:** TypeScript (ES2018 target, esbuild IIFE bundle, vitest + jsdom), Go 1.26 (`/usr/local/go/bin/go`), the committed bundle `internal/server/twillingate.js` rebuilt with `npm run build` in `sdk/`.

**Spec:** `docs/superpowers/specs/2026-09-22-sdk-global-factory-design.md` (already on this branch). The spec is the authority; this plan is its argument.

## Global Constraints

- **The tag carries exactly these attributes:** `data-key`, `data-identity`, `data-auto`, `data-mask-url`, `data-routing`, `data-kind`, `data-consent`, `data-instance`. No `data-user`, `data-group`, `data-debug`, `data-storage`, `data-tagged-events`, `data-attr-*`, and no environment attributes. Every attribute except `data-instance` maps to an `InitOptions` field.
- **`InitOptions` is exactly:** `key`, `identity`, `consent`, `storage`, `kind`, `platform`, `appVersion`, `autoPageviews`, `taggedEvents`, `maskUrl`, `routing`, `flushInterval`, `optOut`, `debug`. No `url`, `instance`, `user`, `group`, `installId`, `os`, `osVersion`, `osName`, `browser`, `browserVersion`, `device`.
- **No inline pre-load stub.** Nothing is added to `docs/` beyond edits to `docs/twillingate.md`.
- **No `window.<name>` global is ever created.** The only global is `window.twillingate`.
- **`document.currentScript` is read for the tag's attributes only, never for the origin.** The origin is `__TWILLINGATE_URL__`, substituted by the collector.
- **Person-level flags are localStorage, unprefixed, read live:** `twillingate_ignore`, `twillingate_debug`.
- **Storage keys are unchanged:** `<instance>_visitor`, `_user`, `_user_name`, `_group`, `_group_name`, `_queue`; default instance prefix `twillingate`.
- **Wire format, reserved keys, transport, batching, retry bounds, masking, routing, detection: unchanged.**
- **Every SDK test file** mocks `./origin` and calls `runtime.reset()` in `beforeEach` (see Task 3 for the exact lines).
- **Implementers never commit.** The controller commits per task. Conventional Commits; the squashed PR title is `feat(sdk)!: one global with a factory, hooks on every event, storage drivers and tagged elements` (or a shorter equivalent the controller settles on).
- Commands: `cd sdk && npm test`, `npm run typecheck`, `npm run build`; Go: `/usr/local/go/bin/go test ./internal/server/ ./internal/api/`; final `make check` (with `PATH=/usr/local/go/bin:$PATH`).
- Docs (`docs/twillingate.md`) change in the same PR as the SDK (Task 6). `docs_sync_test.go` binds symbols; keep every symbol it lists present in both source and docs.

---

## File structure

| File | Responsibility |
| --- | --- |
| `sdk/src/origin.ts` (new) | The `__TWILLINGATE_URL__` placeholder and `collectorOrigin()` |
| `sdk/src/storage.ts` (new) | `StorageDriver`, built-in drivers, `resolveStorage`, the two person-level flags |
| `sdk/src/runtime.ts` (new) | One owner for every browser hook; `Subscriber` interface; `runtime` singleton; `reset()` test hook |
| `sdk/src/twillingate.ts` (rewritten) | `Twillingate`: one instance. Options, hold-before-init, identity enforcement, precedence, `onPage`/`onEvent`, drivers, `optOut`/`debug`/`installId`, subscriber methods |
| `sdk/src/factory.ts` (new) | `TwillingateGlobal` (registry, `create`, `get`, `VERSION`), `tagOptions`, `autoInit`, `supersededBy`, `bootstrap` |
| `sdk/src/entry.ts` (edited) | Calls `bootstrap` from `factory.ts` |
| `sdk/src/*.test.ts` | New: `origin`, `storage`, `runtime`, `hold`, `hooks`, `optout-debug`, `tagged`, `factory`. Edited: `twillingate`, `api`, `identity`, `consent`. Deleted: `instances.test.ts` (folded into `factory.test.ts`) |
| `internal/server/script.go` | Per-request origin substitution |
| `internal/server/twillingate_script_test.go` | Origin substitution test, new markers |
| `internal/api/docs_sync_test.go` | Symbol list; reads `twillingate.ts` and `factory.ts` |
| `internal/server/twillingate.js` | Rebuilt bundle (committed) |
| `docs/twillingate.md`, `sdk/README.md`, spec status | Task 6 |

---

### Task 1: origin.ts and storage.ts

**Files:**
- Create: `sdk/src/origin.ts`, `sdk/src/storage.ts`
- Test: `sdk/src/origin.test.ts`, `sdk/src/storage.test.ts`

**Interfaces:**
- Produces: `ORIGIN: string`, `collectorOrigin(): string | null`; `StorageDriver`, `StorageSpec`, `resolveStorage(spec?: StorageSpec): StorageDriver`, `memoryDriver()`, `cookieDriver()`, `readFlag(name): boolean`, `writeFlag(name, on): void`, constants `IGNORE_FLAG = "twillingate_ignore"`, `DEBUG_FLAG = "twillingate_debug"`.

- [ ] **Step 1: Write the failing tests**

`sdk/src/origin.test.ts`:

```ts
// The collector origin is baked into the served file. A build whose
// placeholder was never substituted (this test file: no vi.mock) has none.
import { describe, expect, it } from "vitest";
import { ORIGIN, collectorOrigin } from "./origin";

describe("collectorOrigin", () => {
  it("is null while the placeholder is unsubstituted", () => {
    expect(ORIGIN).toBe("__TWILLINGATE_URL__");
    expect(collectorOrigin()).toBeNull();
  });
});
```

`sdk/src/storage.test.ts`:

```ts
// Storage drivers: the built-ins, a custom object, a throwing one, and the
// two person-level flags that always live in localStorage.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DEBUG_FLAG, IGNORE_FLAG, cookieDriver, memoryDriver, readFlag, resolveStorage, writeFlag } from "./storage";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  for (const c of document.cookie.split(";")) {
    const k = c.split("=")[0].trim();
    if (k) document.cookie = `${k}=; path=/; max-age=0`;
  }
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("resolveStorage", () => {
  it("defaults to localStorage", () => {
    const d = resolveStorage(undefined);
    d.set("k", "v");
    expect(localStorage.getItem("k")).toBe("v");
    expect(d.get("k")).toBe("v");
    d.remove("k");
    expect(localStorage.getItem("k")).toBeNull();
  });

  it("sessionStorage lives for the tab", () => {
    const d = resolveStorage("sessionStorage");
    d.set("k", "v");
    expect(sessionStorage.getItem("k")).toBe("v");
    expect(localStorage.getItem("k")).toBeNull();
  });

  it("memory keeps nothing on the device", () => {
    const d = resolveStorage("memory");
    d.set("k", "v");
    expect(d.get("k")).toBe("v");
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
    d.remove("k");
    expect(d.get("k")).toBeNull();
    // two memory drivers do not share
    expect(memoryDriver().get("k")).toBeNull();
  });

  it("cookie stores identity keys as host-only cookies and skips the queue", () => {
    const d = resolveStorage("cookie");
    d.set("twillingate_visitor", "v 1");
    expect(document.cookie).toContain("twillingate_visitor=v%201");
    expect(d.get("twillingate_visitor")).toBe("v 1");
    d.set("twillingate_queue", "[{}]");
    expect(document.cookie).not.toContain("twillingate_queue");
    expect(d.get("twillingate_queue")).toBeNull();
    d.remove("twillingate_visitor");
    expect(d.get("twillingate_visitor")).toBeNull();
  });

  it("accepts a custom driver object", () => {
    const store = new Map<string, string>();
    const d = resolveStorage({
      get: (k) => store.get(k) ?? null,
      set: (k, v) => void store.set(k, v),
      remove: (k) => void store.delete(k),
    });
    d.set("k", "v");
    expect(store.get("k")).toBe("v");
    expect(d.get("k")).toBe("v");
  });

  it("treats a throwing driver as unavailable", () => {
    const d = resolveStorage({
      get: () => { throw new Error("no"); },
      set: () => { throw new Error("no"); },
      remove: () => { throw new Error("no"); },
    });
    expect(() => d.set("k", "v")).not.toThrow();
    expect(d.get("k")).toBeNull();
    expect(() => d.remove("k")).not.toThrow();
  });

  it("falls back to localStorage on an unusable spec, with a warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const d = resolveStorage("indexedDB" as never);
    d.set("k", "v");
    expect(localStorage.getItem("k")).toBe("v");
    expect(warn).toHaveBeenCalled();
    expect(resolveStorage({ get: () => null } as never)).toBeDefined();
  });
});

describe("flags", () => {
  it("read and write the two localStorage flags", () => {
    expect(readFlag(IGNORE_FLAG)).toBe(false);
    writeFlag(IGNORE_FLAG, true);
    expect(localStorage.getItem("twillingate_ignore")).toBe("true");
    expect(readFlag(IGNORE_FLAG)).toBe(true);
    writeFlag(IGNORE_FLAG, false);
    expect(localStorage.getItem("twillingate_ignore")).toBeNull();
    localStorage.setItem(DEBUG_FLAG, "true");
    expect(readFlag(DEBUG_FLAG)).toBe(true);
  });

  it("cookieDriver is host-only, one year, SameSite=Lax", () => {
    const set = vi.spyOn(document, "cookie", "set");
    cookieDriver().set("a_user", "u");
    const written = set.mock.calls[0][0] as string;
    expect(written).toContain("a_user=u");
    expect(written).toContain("path=/");
    expect(written).toContain("max-age=31536000");
    expect(written).toContain("SameSite=Lax");
    expect(written).not.toContain("domain=");
    expect(written).toContain("Secure"); // jsdom url is https
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd sdk && npx vitest run src/origin.test.ts src/storage.test.ts`
Expected: FAIL, modules not found.

- [ ] **Step 3: Write the modules**

`sdk/src/origin.ts`:

```ts
/* The collector's origin, baked into the served file.
 *
 * The collector replaces this placeholder, per request, with the origin
 * the file was requested from (internal/server/script.go), the same way
 * it replaces __TWILLINGATE_VERSION__. A build that still carries the
 * placeholder was not served by a collector -- someone bundled the
 * module -- and has no idea where to post; init() then warns and stays
 * dormant. Tests mock this module to supply an origin.
 */
export const ORIGIN = "__TWILLINGATE_URL__";

/** The origin batches are posted to, or null when this build has none. */
export function collectorOrigin(): string | null {
  if (ORIGIN.slice(0, 2) === "__") return null;
  return ORIGIN.replace(/\/$/, "");
}
```

`sdk/src/storage.ts`:

```ts
/* Where an instance keeps its keys once consent allows: the built-in
 * drivers, a custom object, and the two person-level flags that always
 * live in localStorage regardless of driver, because opting out and
 * debugging are about the page, not one instance.
 */

export interface StorageDriver {
  get(key: string): string | null;
  set(key: string, value: string): void;
  remove(key: string): void;
}

export type StorageSpec = "localStorage" | "sessionStorage" | "memory" | "cookie" | StorageDriver;

export const IGNORE_FLAG = "twillingate_ignore";
export const DEBUG_FLAG = "twillingate_debug";

// One year, the same lifetime every analytics cookie uses.
const COOKIE_MAX_AGE = 31536000;

/** Wrap any driver so a throw counts as "storage unavailable". */
function guarded(d: StorageDriver): StorageDriver {
  return {
    get(key) {
      try {
        return d.get(key);
      } catch {
        return null;
      }
    },
    set(key, value) {
      try {
        d.set(key, value);
      } catch {
        /* unavailable: nothing persists */
      }
    },
    remove(key) {
      try {
        d.remove(key);
      } catch {
        /* unavailable */
      }
    },
  };
}

function webStorage(which: "localStorage" | "sessionStorage"): StorageDriver {
  return guarded({
    get: (key) => window[which].getItem(key),
    set: (key, value) => window[which].setItem(key, value),
    remove: (key) => window[which].removeItem(key),
  });
}

/** An in-instance map: equivalent to no consent, useful for tests and apps that hold identity themselves. */
export function memoryDriver(): StorageDriver {
  const m = new Map<string, string>();
  return {
    get: (key) => (m.has(key) ? (m.get(key) as string) : null),
    set: (key, value) => void m.set(key, value),
    remove: (key) => void m.delete(key),
  };
}

/**
 * Host-only cookies, one per identity key. The retry queue is skipped: a
 * queue in a cookie would ride on every request to the site. A cookie on
 * a shared parent domain is a custom driver (docs/twillingate.md shows
 * one), since the SDK cannot know the registrable domain.
 */
export function cookieDriver(): StorageDriver {
  const isQueue = (key: string) => key.slice(-6) === "_queue";
  const secure = typeof location !== "undefined" && location.protocol === "https:" ? "; Secure" : "";
  return guarded({
    get(key) {
      if (isQueue(key)) return null;
      const m = new RegExp("(?:^|; )" + key.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "=([^;]*)").exec(document.cookie);
      return m ? decodeURIComponent(m[1]) : null;
    },
    set(key, value) {
      if (isQueue(key)) return;
      document.cookie = `${key}=${encodeURIComponent(value)}; path=/; max-age=${COOKIE_MAX_AGE}; SameSite=Lax${secure}`;
    },
    remove(key) {
      if (isQueue(key)) return;
      document.cookie = `${key}=; path=/; max-age=0; SameSite=Lax${secure}`;
    },
  });
}

/** Resolve the storage option to a driver. Unusable input warns and keeps localStorage. */
export function resolveStorage(spec: StorageSpec | undefined): StorageDriver {
  if (spec === undefined || spec === "localStorage") return webStorage("localStorage");
  if (spec === "sessionStorage") return webStorage("sessionStorage");
  if (spec === "memory") return memoryDriver();
  if (spec === "cookie") return cookieDriver();
  if (
    spec && typeof spec === "object" &&
    typeof spec.get === "function" && typeof spec.set === "function" && typeof spec.remove === "function"
  ) {
    return guarded(spec);
  }
  console.warn(`twillingate: storage ${JSON.stringify(spec)} is not a driver; using localStorage`);
  return webStorage("localStorage");
}

/** The person-level flags: always localStorage, never prefixed, read live. */
export function readFlag(name: string): boolean {
  try {
    return localStorage.getItem(name) === "true";
  } catch {
    return false;
  }
}

export function writeFlag(name: string, on: boolean): void {
  try {
    if (on) localStorage.setItem(name, "true");
    else localStorage.removeItem(name);
  } catch {
    /* storage unavailable */
  }
}
```

- [ ] **Step 4: Run the tests**

Run: `cd sdk && npx vitest run src/origin.test.ts src/storage.test.ts && npm run typecheck`
Expected: PASS. (If the `cookieDriver is host-only` test cannot spy on the `document.cookie` setter in this jsdom, replace the spy with reading `document.cookie` back and asserting `a_user=u` is present, and drop the attribute assertions — note the substitution in the report.)

- [ ] **Step 5: Report** (no commit; the controller commits)

---

### Task 2: runtime.ts

**Files:**
- Create: `sdk/src/runtime.ts`
- Test: `sdk/src/runtime.test.ts`

**Interfaces:**
- Produces: `NavigationSource = "push" | "pop" | "hash"`, `interface Subscriber { onNavigate(source): void; onOnline(): void; onUnload(): void; onTagged(name: string, path: string): void }`, `runtime: Runtime` with `subscribe(s)`, `unsubscribe(s)`, `reset()`, `subscribers(): number`, `installed(): boolean`.
- Consumed by Task 3 (`Twillingate implements Subscriber`).

- [ ] **Step 1: Write the failing test**

`sdk/src/runtime.test.ts`:

```ts
// The runtime installs every browser hook once and fans out to every
// subscriber; each subscriber decides for itself.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { runtime, type NavigationSource, type Subscriber } from "./runtime";

function sub(): Subscriber & { nav: NavigationSource[]; online: number; unload: number; tagged: Array<[string, string]> } {
  const s = {
    nav: [] as NavigationSource[], online: 0, unload: 0, tagged: [] as Array<[string, string]>,
    onNavigate(source: NavigationSource) { s.nav.push(source); },
    onOnline() { s.online++; },
    onUnload() { s.unload++; },
    onTagged(name: string, path: string) { s.tagged.push([name, path]); },
  };
  return s;
}

const nativePushState = history.pushState;

beforeEach(() => {
  runtime.reset();
  history.replaceState(null, "", "/start");
  document.body.innerHTML = "";
});

afterEach(() => {
  runtime.reset();
  vi.restoreAllMocks();
  expect(history.pushState).toBe(nativePushState);
});

describe("runtime", () => {
  it("installs nothing until the first subscriber, then once", () => {
    expect(runtime.installed()).toBe(false);
    const a = sub();
    const b = sub();
    runtime.subscribe(a);
    expect(runtime.installed()).toBe(true);
    const patched = history.pushState;
    runtime.subscribe(b);
    runtime.subscribe(a); // idempotent
    expect(history.pushState).toBe(patched);
    expect(runtime.subscribers()).toBe(2);
  });

  it("one pushState reaches every subscriber as one navigation each", () => {
    const a = sub();
    const b = sub();
    runtime.subscribe(a);
    runtime.subscribe(b);
    history.pushState(null, "", "/second");
    expect(location.pathname).toBe("/second"); // the native call still ran
    expect(a.nav).toEqual(["push"]);
    expect(b.nav).toEqual(["push"]);
    window.dispatchEvent(new Event("popstate"));
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    expect(a.nav).toEqual(["push", "pop", "hash"]);
  });

  it("dispatches online and unload", () => {
    const a = sub();
    runtime.subscribe(a);
    window.dispatchEvent(new Event("online"));
    window.dispatchEvent(new Event("pagehide"));
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(a.online).toBe(1);
    expect(a.unload).toBe(2);
  });

  it("a subscriber that throws does not stop the others", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const bad = { ...sub(), onNavigate() { throw new Error("boom"); } };
    const good = sub();
    runtime.subscribe(bad);
    runtime.subscribe(good);
    history.pushState(null, "", "/x");
    expect(good.nav).toEqual(["push"]);
  });

  it("tagged elements: click on the element or a descendant, main and middle button, nearest ancestor wins", () => {
    document.body.innerHTML =
      '<div data-twillingate-event="outer"><button id="b" data-twillingate-event="signup"><span id="s">Go</span></button></div>' +
      '<a id="plain">no tag</a>';
    const a = sub();
    runtime.subscribe(a);
    document.getElementById("s")!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
    document.getElementById("b")!.dispatchEvent(new MouseEvent("auxclick", { bubbles: true, button: 1 }));
    document.getElementById("b")!.dispatchEvent(new MouseEvent("auxclick", { bubbles: true, button: 2 }));
    document.getElementById("plain")!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
    expect(a.tagged).toEqual([["signup", "/start"], ["signup", "/start"]]);
  });

  it("tagged forms fire on submit only, other elements on click only", () => {
    document.body.innerHTML =
      '<form id="f" data-twillingate-event="subscribe"><button id="fb" type="submit">Go</button></form>';
    const a = sub();
    runtime.subscribe(a);
    document.getElementById("fb")!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
    expect(a.tagged).toEqual([]);
    document.getElementById("f")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(a.tagged).toEqual([["subscribe", "/start"]]);
  });

  it("a handler that stops propagation cannot eat a tagged click", () => {
    document.body.innerHTML = '<button id="b" data-twillingate-event="cta">Go</button>';
    document.getElementById("b")!.addEventListener("click", (e) => e.stopPropagation());
    const a = sub();
    runtime.subscribe(a);
    document.getElementById("b")!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
    expect(a.tagged).toEqual([["cta", "/start"]]);
  });

  it("reset() removes every hook and restores pushState", () => {
    const a = sub();
    runtime.subscribe(a);
    runtime.reset();
    expect(runtime.installed()).toBe(false);
    history.pushState(null, "", "/after");
    window.dispatchEvent(new Event("online"));
    expect(a.nav).toEqual([]);
    expect(a.online).toBe(0);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd sdk && npx vitest run src/runtime.test.ts`
Expected: FAIL, module not found.

- [ ] **Step 3: Write the module**

`sdk/src/runtime.ts`:

```ts
/* One owner for every browser hook.
 *
 * Every instance used to patch history.pushState itself and add its own
 * popstate, hashchange, online, pagehide and visibilitychange listeners;
 * two instances meant two patches chained on one another. The runtime
 * installs each hook once, lazily, when the first instance subscribes,
 * and dispatches to every subscriber. The instance keeps the decision
 * (autoPageviews off, routing mode, taggedEvents off), the runtime keeps
 * the subscription. There is one runtime per bundle copy.
 */

export type NavigationSource = "push" | "pop" | "hash";

export interface Subscriber {
  onNavigate(source: NavigationSource): void;
  onOnline(): void;
  onUnload(): void;
  onTagged(name: string, path: string): void;
}

const TAG_ATTR = "data-twillingate-event";

type Listener = [EventTarget, string, EventListener, boolean];

class Runtime {
  private subs: Subscriber[] = [];
  private hooked = false;
  private nativePushState: History["pushState"] | null = null;
  private listeners: Listener[] = [];

  subscribe(s: Subscriber): void {
    if (this.subs.indexOf(s) < 0) this.subs.push(s);
    this.install();
  }

  unsubscribe(s: Subscriber): void {
    this.subs = this.subs.filter((x) => x !== s);
  }

  /** Test hooks. */
  reset(): void {
    for (const [target, type, fn, capture] of this.listeners) target.removeEventListener(type, fn, capture);
    this.listeners = [];
    if (this.nativePushState && typeof history !== "undefined") history.pushState = this.nativePushState;
    this.nativePushState = null;
    this.subs = [];
    this.hooked = false;
  }

  installed(): boolean {
    return this.hooked;
  }

  subscribers(): number {
    return this.subs.length;
  }

  private each(fn: (s: Subscriber) => void): void {
    for (const s of this.subs.slice()) {
      try {
        fn(s);
      } catch (e) {
        console.warn("twillingate: an instance threw while handling a browser event", e);
      }
    }
  }

  private on(target: EventTarget, type: string, fn: EventListener, capture = false): void {
    target.addEventListener(type, fn, capture);
    this.listeners.push([target, type, fn, capture]);
  }

  private install(): void {
    if (this.hooked || typeof window === "undefined" || typeof document === "undefined") return;
    this.hooked = true;
    if (typeof history !== "undefined") {
      const native = history.pushState;
      this.nativePushState = native;
      history.pushState = (...args: Parameters<History["pushState"]>) => {
        native.apply(history, args);
        this.each((s) => s.onNavigate("push"));
      };
      this.on(window, "popstate", () => this.each((s) => s.onNavigate("pop")));
      this.on(window, "hashchange", () => this.each((s) => s.onNavigate("hash")));
    }
    this.on(window, "online", () => this.each((s) => s.onOnline()));
    // pagehide covers navigations and tab closes; visibilitychange the
    // mobile cases where pagehide never fires.
    this.on(window, "pagehide", () => this.each((s) => s.onUnload()));
    this.on(document, "visibilitychange", () => {
      if (document.visibilityState === "hidden") this.each((s) => s.onUnload());
    });
    // Capture phase: a handler that stops propagation must not eat the
    // event. auxclick because middle-click opens links too.
    const handle = (e: Event) => this.handleTagged(e);
    this.on(document, "click", handle, true);
    this.on(document, "auxclick", handle, true);
    this.on(document, "submit", handle, true);
  }

  private handleTagged(e: Event): void {
    if (e.type !== "submit" && (e as MouseEvent).button > 1) return; // main and middle click only
    const target = e.target as Node | null;
    let el: Element | null = target && target.nodeType === 1 ? (target as Element) : target ? target.parentElement : null;
    let name: string | null = null;
    while (el) {
      name = el.getAttribute(TAG_ATTR);
      if (name) break;
      el = el.parentElement;
    }
    if (!el || !name) return;
    // A tagged <form> fires on submit, anything else on click. Without
    // this a tagged form wrapping its own submit button counts twice.
    if ((el.tagName === "FORM") !== (e.type === "submit")) return;
    const path = typeof location !== "undefined" ? location.pathname : "";
    const tagged = name;
    this.each((s) => s.onTagged(tagged, path));
  }
}

export const runtime = new Runtime();
```

- [ ] **Step 4: Run the test**

Run: `cd sdk && npx vitest run src/runtime.test.ts && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: Report**

---

### Task 3: The core instance, and the existing suites adapted

**Files:**
- Rewrite: `sdk/src/twillingate.ts`
- Modify: `sdk/src/twillingate.test.ts`, `sdk/src/api.test.ts`, `sdk/src/identity.test.ts`, `sdk/src/consent.test.ts`
- Delete: `sdk/src/instances.test.ts` (Task 4 replaces it with `factory.test.ts`)
- Temporarily: `sdk/src/entry.ts` will not compile until Task 4 adds `factory.ts`; `npm run typecheck` will report `entry.ts` only. That is expected in this task; `vitest` does not load `entry.ts`.

**Interfaces:**
- Consumes: Task 1 (`collectorOrigin`, `resolveStorage`, `readFlag`, `writeFlag`, `IGNORE_FLAG`, `DEBUG_FLAG`, `StorageDriver`, `StorageSpec`), Task 2 (`runtime`, `Subscriber`, `NavigationSource`).
- Produces: `class Twillingate implements Subscriber` with `constructor(name = "twillingate")`, `readonly name`, the public API in the table of spec §11 (`init` returns `this`; `page`, `screen`, `track`, `attrs`, `identify`, `group`, `installId`, `reset`, `flush`, `consent`, `optOut`, `debug`, `onPage`, `onEvent`, `detectOS`, `detectBrowser`, `detectDevice`, `util`); exports `VERSION`, `InitOptions`, `PageviewInfo`, `PageListener`, `EventInfo`, `EventListener`, `instanceName`, `DEFAULT_INSTANCE`, and re-exports from `detect` and `storage`. `bootstrap`, `supersededBy`, `autoInit` are **removed** from this file (Task 4 owns them in `factory.ts`).

- [ ] **Step 1: Write the new `sdk/src/twillingate.ts`** (full file; replace the existing one)

```ts
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
    return this.ready && !readFlag(IGNORE_FLAG) && !this.optOutSpec();
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
  // everything this instance wrote.
  private mayStore(): boolean {
    const now = this.consentPin !== null ? this.consentPin : this.consentSpec();
    if (now !== this.lastConsent) {
      this.lastConsent = now;
      if (now) {
        if (this.pending.length) this.driver.set(this.k.queue, JSON.stringify(mergeBatches(this.storedBatches(), this.pending)));
        // Guarded by `ready`: init()'s own first decision point runs
        // before stored values are loaded, so saving here would overwrite
        // storage with nulls before init() reads it back.
        if (this.ready) this.saveIdentity();
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
```

- [ ] **Step 2: Adapt the four existing suites.** Every suite gets the same header; apply these edits exactly.

**Common header** (every SDK test file that constructs a `Twillingate`, i.e. `twillingate.test.ts`, `api.test.ts`, `identity.test.ts`, `consent.test.ts`, and the new files of Tasks 3–4): add after the imports, before any helper:

```ts
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));
```

and as the first line of the file's top-level `beforeEach`: `runtime.reset();`. Remove `history.pushState = nativePushState;` and the `nativePushState` constant where present (the runtime restores it). The `tg()` helper in every file becomes:

```ts
function tg(opts: Partial<InitOptions> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
  return t;
}
```

with `import { Twillingate, type InitOptions } from "./twillingate";`. `autoPageviews: false` is explicit because the code default is now `true` and most tests want to control their pageviews; tests that pass `autoPageviews: true` keep doing so.

**`twillingate.test.ts`:**
- Imports: `import { Twillingate, type InitOptions } from "./twillingate"; import { autoInit, supersededBy } from "./factory";` — `factory.ts` arrives in Task 4; until then this file will not run. **Ruling for the implementer:** in this task, move the `describe("snippet auto-init")` block and the `describe("script tag and init parity")` block out of `twillingate.test.ts` into a new file `sdk/src/snippet.test.ts` verbatim (same header, same helpers `scriptTag`, `drain`, `sent`), leave that file failing to import `./factory`, and note it in the report; Task 4 fixes it. `twillingate.test.ts` then imports only `Twillingate` and `InitOptions`.
- `describe("init")`: replace the second and third tests with:

```ts
  it("holds calls made before init and runs them after", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    t.track("early");
    expect(warn).not.toHaveBeenCalled();
    expect(sent).toHaveLength(0);
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false });
    t.flush();
    await drain();
    expect(sent[0].body.events[0].name).toBe("early");
  });

  it("init() returns the instance and a second init() warns and is ignored", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    expect(t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false })).toBe(t);
    t.init({ key: "ak_other", flushInterval: 0 });
    expect(warn.mock.calls.flat().join(" ")).toContain("already initialised");
  });
```

- `"carries app context as batch attributes"`: options become `tg({ identity: "identified", kind: "app", platform: "ios", appVersion: "2.4.1" })` followed by `t.installId("018f-install");`. Assert `$kind`, `$platform`, `$app_version`, `$install_id` as before, and `expect(typeof attributes.$os).toBe("string")` instead of `$os: "ios"` / `$os_version`.
- `describe("environment")`: delete `"lets an explicit option beat detection, while detect* still answers for the signals"`. Keep the rest.
- `describe("batching")`: unchanged (the runtime dispatches `pagehide` and `visibilitychange` to the instance).
- `describe("failure handling and the offline queue")`: unchanged.

**`snippet.test.ts`** (moved blocks): in the parity test, scan `factorySource` for `getAttribute("data-…")` and `twillingateSource` for `InitOptions` fields:

```ts
import twillingateSource from "./twillingate.ts?raw";
import factorySource from "./factory.ts?raw";
// …
    const attrs: string[] = [];
    const re = /getAttribute\("data-([a-z-]+)"\)/g;
    for (let m = re.exec(factorySource); m !== null; m = re.exec(factorySource)) attrs.push(m[1]);
    const optionFor: Record<string, string> = {
      key: "key", identity: "identity", auto: "autoPageviews", "mask-url": "maskUrl",
      routing: "routing", kind: "kind", consent: "consent",
    };
    expect(attrs.length).toBeGreaterThan(0);
    for (const a of attrs) {
      if (a === "instance") continue; // maps to create()'s name, not an option
      const opt = optionFor[a];
      expect(opt, `data-${a} has no InitOptions field`).toBeDefined();
      const declared = new RegExp(`^\\s+${opt}\\??:`, "m");
      expect(declared.test(twillingateSource), `InitOptions declares no ${opt}`).toBe(true);
    }
    expect(attrs).toContain("instance");
```

Also in that file: delete `"carries data-user and data-group into batch attributes"` and replace with:

```ts
  it("ignores data-user and data-group: identity is set from code", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_s", "data-identity": "identified", "data-user": "u_1", "data-group": "org_9", "data-auto": "off" }));
    t.track("probe");
    t.flush();
    await drain();
    expect(sent[0].body.attributes).not.toHaveProperty("$user_id");
    expect(sent[0].body.attributes).not.toHaveProperty("$group_id");
  });
```

In `"ignores environment data attributes; overrides are init() options"` rename to `"ignores environment data attributes"` and keep its assertions. `"warns when a listener is registered after the first pageview"` uses `t.onPage(...)`.

**`api.test.ts`:**
- `"page(listener) registers a pageview listener that can enrich attributes"`, `"a listener returning false cancels the pageview"`, `"listeners fire for automatic SPA pageviews too"`: replace `t.page((p) => …)` with `t.onPage((p) => …)`.
- Add to `describe("page() overloads")`:

```ts
  it("page(fn) still registers a listener, with a deprecation warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = tg();
    t.page(() => ({ legacy: true }));
    expect(warn.mock.calls.flat().join(" ")).toContain("deprecated");
    t.page("/a");
    t.flush();
    await drain();
    expect((lastEvent().attributes as Record<string, unknown>).legacy).toBe(true);
  });
```

- Add a new `describe("precedence")`:

```ts
describe("precedence: derived < attrs() defaults < call < listeners", () => {
  it("an attrs() default overrides a derived pageview value, and null drops it", async () => {
    Object.defineProperty(document, "referrer", { value: "https://news.example.org/", configurable: true });
    const t = tg();
    t.attrs({ $host: "selfhosted_ab12", $referrer: null });
    t.page("/budget");
    t.flush();
    await drain();
    const attrs = lastEvent().attributes as Record<string, unknown>;
    expect(attrs.$host).toBe("selfhosted_ab12");
    expect(attrs).not.toHaveProperty("$referrer");
    expect(attrs.$path).toBe("/budget");
  });

  it("the call's attributes beat the defaults, and a listener beats the call", async () => {
    const t = tg();
    t.attrs({ tier: "beta", region: "eu" });
    t.onEvent(() => ({ region: "us" }));
    t.track("e", { tier: "pro" });
    t.flush();
    await drain();
    expect(lastEvent().attributes).toEqual({ tier: "pro", region: "us" });
  });
});
```

**`identity.test.ts`:**
- `describe("anonymous mode")`: replace `"identify() carries the user for the session but does not persist it"` with:

```ts
  it("identify() and installId() are inert, with one warning; group() still sends", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = tg({ consent: true });
    t.identify("u_42", "Ada");
    t.installId("device-7");
    t.group("org_9", "Acme");
    const attrs = await lastAttributes(t);
    expect(attrs).not.toHaveProperty("$user_id");
    expect(attrs).not.toHaveProperty("$user_name");
    expect(attrs).not.toHaveProperty("$install_id");
    expect(attrs).toMatchObject({ $group_id: "org_9", $group_name: "Acme" });
    expect(warn).toHaveBeenCalledTimes(1);
    expect(t.installId()).toBeNull();
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
  });

  it("identity set before init() is discarded when init() turns out anonymous", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    t.identify("u_early");
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false });
    const attrs = await lastAttributes(t);
    expect(attrs).not.toHaveProperty("$user_id");
    expect(warn).toHaveBeenCalledTimes(1);
  });
```

- `describe("identified mode")`: `"an explicit installId wins over the stored visitor id"` becomes:

```ts
  it("installId(id) wins over the stored visitor id, and installId() reads what is sent", async () => {
    localStorage.setItem("twillingate_visitor", "stored-visitor");
    const t = tg({ identity: "identified", consent: true });
    expect(t.installId()).toBe("stored-visitor");
    t.installId("device-7");
    expect(t.installId()).toBe("device-7");
    const attrs = await lastAttributes(t);
    expect(attrs.$install_id).toBe("device-7");
  });
```

  `"an init-supplied user wins over the stored one"` becomes:

```ts
  it("identify() before init() wins over the stored user and lands on the first event", async () => {
    localStorage.setItem("twillingate_user", "u_old");
    const t = new Twillingate();
    t.identify("u_new", "New");
    t.init({ key: "ak_test", flushInterval: 0, identity: "identified", consent: true, autoPageviews: true });
    t.flush();
    await drain();
    expect(sent[0].body.attributes.$user_id).toBe("u_new");
    expect(sent[0].body.events[0].name).toBe("$page_view");
    expect(localStorage.getItem("twillingate_user")).toBe("u_new");
    expect(localStorage.getItem("twillingate_user_name")).toBe("New");
  });
```

- `describe("pageviews")`: add first:

```ts
  it("automatic pageviews are on by default in code", async () => {
    const t = new Twillingate();
    t.init({ key: "ak_test", flushInterval: 0 });
    t.flush();
    await drain();
    expect(sent[0].body.events[0].name).toBe("$page_view");
  });
```

  `"autoPageviews hooks pushState and popstate"` and `"popstate back to a different path…"`: unchanged in body (they pass `autoPageviews: true`).
- `describe("location attributes")`: `"threads host and path through the listener chain"` and `"gives listeners the post-mask url"` use `t.onPage(...)`.
- `describe("hash routing")`: unchanged.

**`consent.test.ts`:**
- Header as above; `autoInit` now imports from `./factory` (Task 4). **Ruling:** the two `data-consent` tests at the end (`"data-consent resolves a literal…"`, `"data-consent naming nothing…"`) move to `snippet.test.ts` verbatim (with the `scriptTag` helper and the `g` global record they use). `consent.test.ts` then does not import `autoInit`.
- `"without consent, identity still comes from init and identify() for the session"`: rewrite as:

```ts
  it("without consent, identity still comes from identify() for the session", async () => {
    const t = new Twillingate();
    t.identify("u_1");
    t.group("org_1");
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, identity: "identified" });
    const attrs = await lastAttributes(t);
    expect(attrs).toMatchObject({ $user_id: "u_1", $group_id: "org_1" });
    expect(localStorage.length).toBe(0);
  });
```

- Every other test in the file: replace any `user:`/`group:` init option with an `identify()`/`group()` call before `init()` (grep the file; `"consent(true) persists a user and group the instance already holds"` already calls them after init and is unchanged).
- `OWNED` constant, `tg` default and the rest unchanged.

- [ ] **Step 3: Delete `sdk/src/instances.test.ts`** (`git rm`; Task 4 replaces it).

- [ ] **Step 4: Run the suites**

Run: `cd sdk && npx vitest run src/twillingate.test.ts src/api.test.ts src/identity.test.ts src/consent.test.ts src/storage.test.ts src/origin.test.ts src/runtime.test.ts src/detect.test.ts src/mask.test.ts src/util.test.ts`
Expected: PASS. `npx vitest run` (all) fails only in `snippet.test.ts` on the missing `./factory` import. `npm run typecheck` reports errors only in `entry.ts` and `snippet.test.ts` (both fixed by Task 4).

- [ ] **Step 5: Report**, naming exactly which tests were rewritten, moved or deleted.

---

### Task 4: factory.ts, entry.ts, and the factory suite

**Files:**
- Create: `sdk/src/factory.ts`, `sdk/src/factory.test.ts`
- Modify: `sdk/src/entry.ts`, `sdk/src/snippet.test.ts` (imports now resolve)

**Interfaces:**
- Consumes: Task 3 (`Twillingate`, `VERSION`, `instanceName`, `DEFAULT_INSTANCE`, `InitOptions`).
- Produces: `class TwillingateGlobal extends Twillingate` with `readonly VERSION`, `create(name, opts?)`, `get(name?)`, `instances(): string[]`; `tagOptions(script): InitOptions | null`; `autoInit(tg, script)`; `supersededBy(existing, script)`; `isSDK(x)`; `bootstrap(script): Twillingate | null`.

- [ ] **Step 1: Write the failing tests**

`sdk/src/factory.test.ts`:

```ts
// The global: default instance plus registry, and the bundle entry's
// decisions about what it finds at window.twillingate.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate } from "./twillingate";
import { TwillingateGlobal, bootstrap, isSDK, supersededBy } from "./factory";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

const URL_BASE = "https://collector.example.com";
const g = globalThis as Record<string, unknown>;

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
}

async function lastAttributes(t: Twillingate): Promise<Record<string, unknown>> {
  t.track("probe");
  t.flush();
  await drain();
  return sent[sent.length - 1].body.attributes;
}

function scriptTag(attrs: Record<string, string>): HTMLScriptElement {
  const s = document.createElement("script");
  s.src = URL_BASE + "/js/twillingate.js";
  for (const [k, v] of Object.entries(attrs)) s.setAttribute(k, v);
  return s;
}

beforeEach(() => {
  runtime.reset();
  vi.useFakeTimers();
  sent = [];
  vi.stubGlobal("fetch", (_url: string, init: { body: string }) => {
    sent.push({ body: JSON.parse(init.body) });
    return Promise.resolve({ status: 202 });
  });
  localStorage.clear();
  history.replaceState(null, "", "/start");
  delete g.twillingate;
  delete g.et;
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  delete g.twillingate;
  delete g.et;
});

describe("registry", () => {
  it("create(name, opts) builds, inits and registers; get(name) finds it; get() is the default", async () => {
    const tg = new TwillingateGlobal();
    const et = tg.create("et", { key: "ak_et", flushInterval: 0, autoPageviews: false, identity: "identified", consent: true });
    expect(et).toBeInstanceOf(Twillingate);
    expect(et).not.toBe(tg);
    expect(tg.get("et")).toBe(et);
    expect(tg.get()).toBe(tg);
    expect(tg.get("twillingate")).toBe(tg);
    expect(tg.get("nope")).toBeUndefined();
    expect(tg.instances()).toEqual(["et"]);
    const attrs = await lastAttributes(et);
    expect(sent[0].body.key).toBe("ak_et");
    expect(localStorage.getItem("et_visitor")).toBe(attrs.$install_id);
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    expect(typeof tg.VERSION).toBe("string");
  });

  it("create(name) without options is dormant until init()", async () => {
    const tg = new TwillingateGlobal();
    const et = tg.create("et");
    et.track("early");
    await drain();
    expect(sent).toHaveLength(0);
    et.init({ key: "ak_code", flushInterval: 0, autoPageviews: false });
    et.flush();
    await drain();
    expect(sent[0].body.key).toBe("ak_code");
    expect(sent[0].body.events[0].name).toBe("early");
  });

  it("a duplicate name returns the existing instance and ignores new options, with a warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const tg = new TwillingateGlobal();
    const a = tg.create("et", { key: "ak_a", flushInterval: 0, autoPageviews: false });
    const b = tg.create("et", { key: "ak_b", flushInterval: 0 });
    expect(b).toBe(a);
    expect(warn.mock.calls.flat().join(" ")).toContain("et");
    expect(tg.create("et")).toBe(a);
  });

  it("refuses the default name and an invalid name, returning the default instance", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const tg = new TwillingateGlobal();
    expect(tg.create("twillingate", { key: "ak_x" })).toBe(tg);
    expect(tg.create("Not-Valid", { key: "ak_x" })).toBe(tg);
    expect(tg.instances()).toEqual([]);
    expect(warn).toHaveBeenCalledTimes(2);
    expect(g.twillingate).toBeUndefined();
  });

  it("two instances share neither a visitor id nor a queue", async () => {
    const tg = new TwillingateGlobal();
    const a = tg.create("alpha", { key: "ak_a", flushInterval: 0, autoPageviews: false, identity: "identified", consent: true });
    const b = tg.create("beta", { key: "ak_b", flushInterval: 0, autoPageviews: false, identity: "identified", consent: true });
    expect((await lastAttributes(a)).$install_id).not.toBe((await lastAttributes(b)).$install_id);
    vi.stubGlobal("fetch", () => Promise.reject(new TypeError("down")));
    a.track("a-fails");
    a.flush();
    await drain();
    expect(JSON.parse(localStorage.getItem("alpha_queue")!)).toHaveLength(1);
    expect(localStorage.getItem("beta_queue")).toBeNull();
  });
});

describe("bootstrap", () => {
  it("registers the global and auto-inits from the tag", async () => {
    const tg = bootstrap(scriptTag({ "data-key": "ak_web" }));
    expect(g.twillingate).toBe(tg);
    expect(isSDK(g.twillingate)).toBe(true);
    (tg as Twillingate).flush();
    await drain();
    expect(sent[0].body.key).toBe("ak_web");
    expect(sent[0].body.events[0].name).toBe("$page_view");
  });

  it("registers a dormant global without data-key", async () => {
    const tg = bootstrap(scriptTag({}))!;
    expect(g.twillingate).toBe(tg);
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("data-instance lands in the registry; no window.<name>", async () => {
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-identity": "identified", "data-consent": "true", "data-auto": "off" }))!;
    const tg = g.twillingate as TwillingateGlobal;
    expect(isSDK(tg)).toBe(true);
    expect(tg.get("et")).toBe(et);
    expect(g.et).toBeUndefined();
    const attrs = await lastAttributes(et);
    expect(sent[0].body.key).toBe("ak_et");
    expect(localStorage.getItem("et_visitor")).toBe(attrs.$install_id);
  });

  it("a data-instance tag without data-key registers a dormant named instance", async () => {
    const et = bootstrap(scriptTag({ "data-instance": "et" }))!;
    expect((g.twillingate as TwillingateGlobal).get("et")).toBe(et);
    await drain();
    expect(sent).toHaveLength(0);
    et.init({ key: "ak_code", flushInterval: 0, autoPageviews: false });
    expect((await lastAttributes(et)).$kind).toBe("web");
  });

  it("a second bundle copy with data-instance joins the first copy's registry", async () => {
    const web = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }))!;
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }))!;
    expect(g.twillingate).toBe(web);
    expect((web as TwillingateGlobal).get("et")).toBe(et);
    expect(runtime.subscribers()).toBe(2);
    history.pushState(null, "", "/second");
    // both instances have autoPageviews off: one navigation reached both, neither emitted
    web.flush();
    et.flush();
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("one pushState patch serves two tags, each into its own project", async () => {
    bootstrap(scriptTag({ "data-key": "ak_web" }));
    bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et" }));
    history.pushState(null, "", "/second");
    const tg = g.twillingate as TwillingateGlobal;
    tg.flush();
    tg.get("et")!.flush();
    await drain();
    const byKey = (key: string) =>
      sent.filter((s) => s.body.key === key).flatMap((s) => s.body.events.map((e) => (e.attributes as Record<string, string>).$path));
    expect(byKey("ak_web")).toEqual(["/start", "/second"]);
    expect(byKey("ak_et")).toEqual(["/start", "/second"]);
  });

  it("stands down for a duplicate default tag with the same key or no key", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const web = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }));
    expect(bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }))).toBeNull();
    expect(bootstrap(scriptTag({}))).toBeNull();
    expect(g.twillingate).toBe(web);
  });

  it("a duplicate named tag stands down inside the registry", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }));
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }));
    expect(bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }))).toBe(et);
    expect((g.twillingate as TwillingateGlobal).instances()).toEqual(["et"]);
  });

  it("a different key takes over the default instance, with a warning", () => {
    const first = bootstrap(scriptTag({ "data-key": "ak_a", "data-auto": "off" }));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const second = bootstrap(scriptTag({ "data-key": "ak_b", "data-auto": "off" }));
    expect(second).not.toBe(first);
    expect(g.twillingate).toBe(second);
    expect(warn.mock.calls.flat().join(" ")).toContain("ak_a -> ak_b");
  });

  it("never overwrites a foreign global, and still tracks unregistered", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const foreign = { init() {} };
    g.twillingate = foreign;
    const tg = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }))!;
    expect(g.twillingate).toBe(foreign);
    expect(warn.mock.calls.flat().join(" ")).toContain("taken");
    expect((await lastAttributes(tg)).$kind).toBe("web");
  });

  it("an invalid data-instance falls back to the default instance with a warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = bootstrap(scriptTag({ "data-key": "ak_web", "data-instance": "Not-Valid", "data-auto": "off" }));
    expect(g.twillingate).toBe(t);
    expect(warn).toHaveBeenCalled();
  });
});

describe("supersededBy", () => {
  it("reads the loaded key structurally", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const tg = new TwillingateGlobal();
    tg.init({ key: "ak_same", flushInterval: 0, autoPageviews: false });
    expect(supersededBy(tg, scriptTag({ "data-key": "ak_same" }))).toBe(true);
    expect(supersededBy(tg, scriptTag({}))).toBe(true);
    expect(supersededBy(tg, scriptTag({ "data-key": "ak_other" }))).toBe(false);
    expect(supersededBy(new TwillingateGlobal(), scriptTag({ "data-key": "ak_b" }))).toBe(false);
    expect(supersededBy(undefined, scriptTag({ "data-key": "ak_b" }))).toBe(false);
    expect(supersededBy({ init() {} }, scriptTag({}))).toBe(false);
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd sdk && npx vitest run src/factory.test.ts`
Expected: FAIL, `./factory` not found.

- [ ] **Step 3: Write `sdk/src/factory.ts`**

```ts
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
    if (name === DEFAULT_INSTANCE) {
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
    // A different key takes over the default instance below.
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
```

`sdk/src/entry.ts` (replace):

```ts
// Bundle entry: register the SDK at window.twillingate and auto-init in
// snippet mode (a data-key on the loading <script> tag). The tag's
// attributes are the only thing read off document.currentScript; the
// collector's origin is baked into the file (origin.ts).
import { bootstrap, type TwillingateGlobal } from "./factory";

declare global {
  interface Window {
    twillingate: TwillingateGlobal;
  }
}

bootstrap(document.currentScript as HTMLScriptElement | null);
```

- [ ] **Step 4: Run everything**

Run: `cd sdk && npx vitest run && npm run typecheck`
Expected: PASS, no type errors (`snippet.test.ts` now resolves `./factory`).

- [ ] **Step 5: Report**

---

### Task 5: Hold, hooks, opt-out, debug and tagged-element suites

**Files:**
- Create: `sdk/src/hold.test.ts`, `sdk/src/hooks.test.ts`, `sdk/src/optout-debug.test.ts`, `sdk/src/tagged.test.ts`

**Interfaces:** consumes Tasks 3 and 4 only. No source changes expected; if a test exposes a defect, fix the source and say so in the report.

- [ ] **Step 1: Write the suites** (each with the common header from Task 3 and the `tg`/`drain`/`sent` helpers)

`sdk/src/hold.test.ts`:

```ts
// Every call is legal before init(): configuration takes effect at once,
// event-producing calls are held and run after init(), entry pageview
// first, capped at 500 with the oldest dropped.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate } from "./twillingate";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}
let sent: Sent[];

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
}

beforeEach(() => {
  runtime.reset();
  vi.useFakeTimers();
  sent = [];
  vi.stubGlobal("fetch", (_url: string, init: { body: string }) => {
    sent.push({ body: JSON.parse(init.body) });
    return Promise.resolve({ status: 202 });
  });
  localStorage.clear();
  history.replaceState(null, "", "/start");
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("before init()", () => {
  it("runs held calls in order after init(), entry pageview first", async () => {
    const t = new Twillingate();
    t.track("one");
    t.screen("/s");
    t.page("/two");
    t.track("three");
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: true });
    t.flush();
    await drain();
    const events = sent.flatMap((s) => s.body.events);
    expect(events.map((e) => e.name)).toEqual(["$page_view", "one", "$screen_view", "$page_view", "three"]);
    expect((events[0].attributes as Record<string, string>).$path).toBe("/start");
    expect((events[3].attributes as Record<string, string>).$path).toBe("/two");
  });

  it("configuration made before init() applies to the entry pageview", async () => {
    const t = new Twillingate();
    t.onPage(({ path }) => ({ $path: path.replace("/start", "/[entry]") }));
    t.onEvent(() => ({ via: "early" }));
    t.attrs({ tier: "beta" });
    t.group("org_1", "Acme");
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: true });
    t.flush();
    await drain();
    const ev = sent[0].body.events[0];
    expect(ev.name).toBe("$page_view");
    expect(ev.attributes).toMatchObject({ $path: "/[entry]", via: "early", tier: "beta" });
    expect(sent[0].body.attributes).toMatchObject({ $group_id: "org_1", $group_name: "Acme" });
  });

  it("caps held calls at 500, dropping the oldest, with one warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    for (let i = 0; i < 505; i++) t.track(`e${i}`);
    expect(warn).toHaveBeenCalledTimes(1);
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false });
    t.flush();
    await drain();
    const names = sent.flatMap((s) => s.body.events.map((e) => e.name));
    expect(names).toHaveLength(500);
    expect(names[0]).toBe("e5");
    expect(names[499]).toBe("e504");
  });

  it("consent(), optOut() and debug() work before init()", () => {
    const t = new Twillingate();
    expect(t.consent(true)).toBe(true);
    expect(t.optOut()).toBe(false);
    expect(t.debug(true)).toBe(true);
    expect(localStorage.getItem("twillingate_debug")).toBe("true");
    t.debug(false);
  });

  it("a never-initialised instance never sends and never warns on track()", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate("dormant");
    t.track("x");
    t.flush();
    await drain();
    expect(sent).toHaveLength(0);
    expect(warn).not.toHaveBeenCalled();
  });
});
```

`sdk/src/hooks.test.ts`:

```ts
// onEvent runs for every event, after onPage for a pageview; a throwing
// listener drops the event with a warning; listeners see the merged
// attributes and may drop or amend.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, type InitOptions } from "./twillingate";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}
let sent: Sent[];

function tg(opts: Partial<InitOptions> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
  return t;
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
}

function events(): Array<Record<string, unknown>> {
  return sent.flatMap((s) => s.body.events);
}

beforeEach(() => {
  runtime.reset();
  vi.useFakeTimers();
  sent = [];
  vi.stubGlobal("fetch", (_url: string, init: { body: string }) => {
    sent.push({ body: JSON.parse(init.body) });
    return Promise.resolve({ status: 202 });
  });
  localStorage.clear();
  history.replaceState(null, "", "/start");
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("onEvent", () => {
  it("amends or drops product events and pageviews alike, in registration order", async () => {
    const t = tg();
    t.onEvent(({ name }) => (name.startsWith("debug_") ? false : { app: "econumo" }));
    t.onEvent(({ attributes }) => ({ app: `${attributes.app}!` }));
    t.track("debug_noise");
    t.track("budget_created", { currency: "EUR" });
    t.page("/budget");
    t.flush();
    await drain();
    expect(events().map((e) => e.name)).toEqual(["budget_created", "$page_view"]);
    expect(events()[0].attributes).toEqual({ currency: "EUR", app: "econumo!" });
    expect((events()[1].attributes as Record<string, unknown>).app).toBe("econumo!");
  });

  it("runs after onPage on a pageview and sees its output", async () => {
    const t = tg();
    const seen: string[] = [];
    t.onPage(({ path }) => ({ $path: path + "/masked" }));
    t.onEvent(({ name, attributes }) => {
      seen.push(`${name}:${attributes.$path}`);
    });
    t.page("/a");
    t.flush();
    await drain();
    expect(seen).toEqual(["$page_view:/a/masked"]);
  });

  it("a null returned by a listener drops the key", async () => {
    const t = tg();
    t.onEvent(() => ({ $referrer: null }));
    t.page("/a");
    t.flush();
    await drain();
    expect(events()[0].attributes).not.toHaveProperty("$referrer");
  });

  it("a throwing onEvent listener drops the event with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = tg();
    t.onEvent(() => {
      throw new Error("boom");
    });
    t.track("x");
    t.flush();
    await drain();
    expect(sent).toHaveLength(0);
    expect(warn.mock.calls.flat().join(" ")).toContain("onEvent");
  });

  it("a throwing onPage listener drops the pageview with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = tg();
    t.onPage(() => {
      throw new Error("boom");
    });
    t.page("/a");
    t.track("still-sent");
    t.flush();
    await drain();
    expect(events().map((e) => e.name)).toEqual(["still-sent"]);
    expect(warn.mock.calls.flat().join(" ")).toContain("onPage");
  });
});
```

`sdk/src/optout-debug.test.ts`:

```ts
// optOut: the person's flag OR the site's callback; debug: the option,
// the method, or the localStorage flag, all logging with the instance name.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, type InitOptions } from "./twillingate";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}
let sent: Sent[];

function tg(opts: Partial<InitOptions> = {}, name?: string): Twillingate {
  const t = new Twillingate(name);
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
  return t;
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
}

beforeEach(() => {
  runtime.reset();
  vi.useFakeTimers();
  sent = [];
  vi.stubGlobal("fetch", (_url: string, init: { body: string }) => {
    sent.push({ body: JSON.parse(init.body) });
    return Promise.resolve({ status: 202 });
  });
  localStorage.clear();
  history.replaceState(null, "", "/start");
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("optOut", () => {
  it("optOut(true) writes the flag and silences every instance; optOut(false) clears it", async () => {
    const a = tg();
    const b = tg({}, "et");
    expect(a.optOut(true)).toBe(true);
    expect(localStorage.getItem("twillingate_ignore")).toBe("true");
    a.track("a");
    b.track("b");
    a.flush();
    b.flush();
    await drain();
    expect(sent).toHaveLength(0);
    expect(b.optOut(false)).toBe(false);
    expect(localStorage.getItem("twillingate_ignore")).toBeNull();
    a.track("a2");
    a.flush();
    await drain();
    expect(sent).toHaveLength(1);
  });

  it("the callback is consulted at every event and OR-ed with the flag", async () => {
    let dev = true;
    const t = tg({ optOut: () => dev });
    expect(t.optOut()).toBe(true);
    t.track("dropped");
    dev = false;
    expect(t.optOut()).toBe(false);
    t.track("sent");
    t.flush();
    await drain();
    expect(sent.flatMap((s) => s.body.events.map((e) => e.name))).toEqual(["sent"]);
  });

  it("optOut: true is always out; a throwing callback counts as out, with one warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const a = tg({ optOut: true });
    a.track("x");
    const b = tg({ optOut: () => { throw new Error("boom"); } }, "et");
    b.track("y");
    b.track("z");
    a.flush();
    b.flush();
    await drain();
    expect(sent).toHaveLength(0);
    expect(warn).toHaveBeenCalledTimes(1);
  });
});

describe("debug", () => {
  it("logs each event and each send with the instance name, via the option", async () => {
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const t = tg({ debug: true }, "econumo");
    t.track("budget_created", { currency: "EUR" });
    t.flush();
    await drain();
    const lines = log.mock.calls.map((c) => String(c[0]));
    expect(lines.some((l) => l.startsWith("[twillingate:econumo] budget_created"))).toBe(true);
    expect(lines.some((l) => l.startsWith("[twillingate:econumo] sent 1 event(s) → 202"))).toBe(true);
    expect(sent).toHaveLength(1); // logging never changes what is sent
  });

  it("the localStorage flag turns logging on for every instance, and debug(flag) writes it", async () => {
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const t = tg();
    t.track("quiet");
    t.flush();
    await drain();
    expect(log).not.toHaveBeenCalled();
    localStorage.setItem("twillingate_debug", "true");
    expect(t.debug()).toBe(true);
    t.track("loud");
    t.flush();
    await drain();
    expect(log.mock.calls.some((c) => String(c[0]).startsWith("[twillingate] loud"))).toBe(true);
    expect(t.debug(false)).toBe(false);
    expect(localStorage.getItem("twillingate_debug")).toBeNull();
  });
});
```

`sdk/src/tagged.test.ts`:

```ts
// Tagged elements reach every instance with taggedEvents on, through the
// one listener set the runtime owns; the event carries its name and path.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, type InitOptions } from "./twillingate";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}
let sent: Sent[];

function tg(opts: Partial<InitOptions> = {}, name?: string): Twillingate {
  const t = new Twillingate(name);
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
  return t;
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
}

function click(id: string): void {
  document.getElementById(id)!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
}

beforeEach(() => {
  runtime.reset();
  vi.useFakeTimers();
  sent = [];
  vi.stubGlobal("fetch", (_url: string, init: { body: string }) => {
    sent.push({ body: JSON.parse(init.body) });
    return Promise.resolve({ status: 202 });
  });
  localStorage.clear();
  history.replaceState(null, "", "/pricing");
  document.body.innerHTML = '<button id="b" data-twillingate-event="signup" data-twillingate-plan="pro">Go</button>';
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("tagged elements", () => {
  it("track the event name with the path, nothing else, on every instance with them on", async () => {
    const web = tg({ key: "ak_web" });
    const et = tg({ key: "ak_et" }, "et");
    click("b");
    web.flush();
    et.flush();
    await drain();
    const byKey = (key: string) => sent.filter((s) => s.body.key === key).flatMap((s) => s.body.events);
    expect(byKey("ak_web")).toHaveLength(1);
    expect(byKey("ak_web")[0].name).toBe("signup");
    expect(byKey("ak_web")[0].attributes).toEqual({ path: "/pricing" });
    expect(byKey("ak_et")).toHaveLength(1);
  });

  it("taggedEvents: false opts one instance out", async () => {
    const web = tg({ key: "ak_web" });
    const et = tg({ key: "ak_et", taggedEvents: false }, "et");
    click("b");
    web.flush();
    et.flush();
    await drain();
    expect(sent.map((s) => s.body.key)).toEqual(["ak_web"]);
  });

  it("go through attrs() defaults and onEvent like any event", async () => {
    const t = tg();
    t.attrs({ site: "docs" });
    t.onEvent(() => ({ via: "markup" }));
    click("b");
    t.flush();
    await drain();
    expect(sent[0].body.events[0].attributes).toEqual({ path: "/pricing", site: "docs", via: "markup" });
  });
});
```

- [ ] **Step 2: Run the suites**

Run: `cd sdk && npx vitest run && npm run typecheck`
Expected: PASS. A failure here is a defect in Task 3's core or Task 4's factory: fix the source minimally and report which assertion drove the fix.

- [ ] **Step 3: Report**

---

### Task 6: Server origin substitution, symbol lists, the rebuilt bundle

**Files:**
- Modify: `internal/server/script.go`, `internal/server/twillingate_script_test.go`, `internal/api/docs_sync_test.go`
- Rebuild: `internal/server/twillingate.js` (`cd sdk && npm run build`)
- Modify: `sdk/README.md` (one paragraph)

**Interfaces:** consumes the placeholder name `__TWILLINGATE_URL__` from Task 1.

- [ ] **Step 1: Write the failing Go tests**

In `internal/server/twillingate_script_test.go`, add to the marker list in `TestTwillingateSDKServed`:

```go
		"twillingate_debug",      // debug flag
		"data-twillingate-event", // tagged elements
```

and after the placeholder check add:

```go
	if strings.Contains(body, "__TWILLINGATE_URL__") {
		t.Error("served bundle still contains the origin placeholder")
	}
```

Add a new test:

```go
// The collector bakes the origin the file was requested from into the
// served copy, so a collector answering on several hostnames serves each
// site a copy that posts back to the hostname that site used.
func TestTwillingateSDKCarriesRequestOrigin(t *testing.T) {
	_, h := testServer(t)
	serve := func(host, proto string) string {
		r := httptest.NewRequest("GET", "/js/twillingate.js", nil)
		r.Host = host
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Body.String()
	}
	for _, tc := range []struct{ host, proto, want string }{
		{"t.example.com", "https", `"https://t.example.com"`},
		{"t.example.org", "https", `"https://t.example.org"`},
		{"localhost:8080", "", `"http://localhost:8080"`},
	} {
		body := serve(tc.host, tc.proto)
		if !strings.Contains(body, tc.want) {
			t.Errorf("host %q proto %q: served bundle does not carry %s", tc.host, tc.proto, tc.want)
		}
		if strings.Contains(body, "__TWILLINGATE_URL__") {
			t.Errorf("host %q: placeholder left in the served bundle", tc.host)
		}
	}
	// A host that cannot be an origin is not written into the script.
	body := serve(`evil"host`, "https")
	if strings.Contains(body, `evil"host`) {
		t.Error("an unusable Host header reached the served script")
	}
	if !strings.Contains(body, "__TWILLINGATE_URL__") {
		t.Error("an unusable Host header should leave the placeholder, so the SDK stays dormant")
	}
}
```

In `internal/api/docs_sync_test.go`, `TestDocumentMatchesSDK`: read both sources and update the list:

```go
	src := readSource(t, "../../sdk/src/twillingate.ts") + readSource(t, "../../sdk/src/factory.ts")
	for _, symbol := range []string{
		"data-key", "data-identity", "data-auto",
		"data-mask-url", "data-routing", "data-consent", "data-instance",
		"data-kind",
		"init", "page", "onPage", "onEvent", "screen", "track", "attrs", "identify", "group", "installId",
		"reset", "flush", "consent", "optOut", "debug", "create", "get", "storage", "taggedEvents",
		"detectOS", "detectBrowser", "detectDevice", "ClientSignals",
		"twillingate_ignore", "twillingate_debug",
		"pushState", "popstate", "hashchange",
		"$page_view", "$screen_view", "$install_id", "$kind", "$platform", "$os", "$os_name",
		"$browser", "$browser_version", "$device",
		"$os_version", "$display_width", "$display_height",
	} {
```

(`data-user` and `data-group` removed.) Add, after that loop, a check that the removed attributes are gone from both:

```go
	for _, gone := range []string{"data-user", "data-group", "data-debug", "data-storage"} {
		if strings.Contains(src, `getAttribute("`+gone+`")`) {
			t.Errorf("the SDK still reads %s, which the spec removed", gone)
		}
	}
```

- [ ] **Step 2: Run the Go tests to see them fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/server/ -run 'TestTwillingateSDK' ; PATH=/usr/local/go/bin:$PATH go test ./internal/api/ -run TestDocumentMatchesSDK`
Expected: FAIL (markers missing from the stale bundle; symbols missing; the docs half of the symbol test also fails until Task 7 — note it).

- [ ] **Step 3: Rewrite `internal/server/script.go`**

```go
package server

import (
	"bytes"
	_ "embed"
	"net/http"
	"regexp"

	"github.com/dmtrkzntsv/twillingate/docs"
	"github.com/dmtrkzntsv/twillingate/internal/version"
)

// twillingate.js is the only served client, compiled from sdk/ (`npm run
// build` there rewrites the committed bundle).
//
//go:embed twillingate.js
var sdkScript []byte

// The committed bundle carries two placeholders so the artifact stays
// deterministic for CI's drift check. The version is substituted once at
// startup; the origin per request, with the origin the file was
// requested from, so a collector answering on several hostnames serves
// each site a copy that posts back to the hostname that site used. A
// bundle that keeps the origin placeholder (someone bundled the module)
// warns and stays dormant in the browser.
const (
	sdkVersionPlaceholder = "__TWILLINGATE_VERSION__"
	sdkOriginPlaceholder  = "__TWILLINGATE_URL__"
)

// The substituted origin lands inside a JS string literal. Only a plain
// host (with an optional port) is written; anything else leaves the
// placeholder in place, which the SDK treats as "no collector origin".
var originHost = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)

// requestOrigin is the scheme and host the client used to fetch the
// script: the proxy's forwarded scheme when there is one, else the
// connection's, and the Host header.
func requestOrigin(r *http.Request) string {
	if !originHost.MatchString(r.Host) {
		return ""
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme != "https" && scheme != "http" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	return scheme + "://" + r.Host
}

func (s *Server) registerScript(mux *http.ServeMux) {
	headers := func(w http.ResponseWriter) {
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=86400")
	}
	versioned := bytes.ReplaceAll(sdkScript,
		[]byte(sdkVersionPlaceholder), []byte(version.Version))
	mux.HandleFunc("GET /js/twillingate.js", func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		body := versioned
		if origin := requestOrigin(r); origin != "" {
			body = bytes.ReplaceAll(versioned, []byte(sdkOriginPlaceholder), []byte(origin))
		}
		w.Write(body)
	})
	// Helpers are served from the same table so a site loads them rather
	// than copying them into its own static assets. plausible-shim.js is
	// embedded from docs/, where the README documenting it lives.
	mux.HandleFunc("GET /js/plausible-shim.js", func(w http.ResponseWriter, _ *http.Request) {
		headers(w)
		w.Write(docs.PlausibleShim)
	})
}
```

- [ ] **Step 4: Rebuild the bundle and run Go tests**

Run: `cd sdk && npm run build && cd .. && git diff --stat internal/server/twillingate.js && PATH=/usr/local/go/bin:$PATH go test ./internal/server/`
Expected: the bundle changed; server tests PASS. Verify the served bundle logic by hand once: `grep -c '__TWILLINGATE_URL__' internal/server/twillingate.js` prints `1`.

- [ ] **Step 5: `sdk/README.md`** — after the CI paragraph add:

```md
The committed bundle carries two placeholders: `__TWILLINGATE_VERSION__`,
substituted once at startup with the build version, and
`__TWILLINGATE_URL__`, substituted per request with the origin the file
was requested from. The served file is the only way to load the SDK;
bundling `src/twillingate.ts` leaves the origin placeholder in place and
the SDK stays dormant. Tests mock `src/origin.ts` to supply an origin.
```

- [ ] **Step 6: Report** (the `docs_sync` docs-side failures are expected until Task 7)

---

### Task 7: docs/twillingate.md, and the spec's status

**Files:**
- Modify: `docs/twillingate.md` ("Instrument a website" section rewritten; one sentence in Identity; Privacy bullets)
- Modify: `docs/superpowers/specs/2026-09-22-sdk-global-factory-design.md` (`Status: implemented (2026-09-22)`)

**Interfaces:** the Go test `TestDocumentMatchesSDK` (Task 6) binds every symbol below to this page.

- [ ] **Step 1: Replace the whole "Instrument a website" section** (from the `## Instrument a website` heading up to, not including, the `---` before `## The event model`) with the text below. Keep every other section as it is.

````md
## Instrument a website

The collector serves its own SDK at `/js/twillingate.js`, compiled from the
TypeScript in `sdk/` and embedded in the binary. The served file carries
the collector's origin, so nothing but the key is configured. Load it as a
tag, or inject the same tag from code; bundling the module is not
supported.

### Snippet mode

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_9f3c…"
        data-identity="anonymous"></script>
```

`twillingate key issue -project-id <id> -label <label>` mints the key and
prints this snippet ready to paste. Its `src` uses `PUBLIC_URL`; a site on
another collector hostname changes the origin, and the served copy posts
back to whichever hostname loaded it.

| Attribute | `init()` option | Meaning |
| --- | --- | --- |
| `data-key` | `key` | The project's ingest key. Required to auto-init; without it the tag loads dormant for `init()` from code. Public by design. |
| `data-identity` | `identity` | `anonymous` (default) or `identified`. Decides what the instance **sends**: anonymous never sends `$user_id`, `$user_name` or `$install_id`, and `identify()` is inert; identified sends them and, with [consent](#consent-and-storage), persists the visitor id, user and group. |
| `data-auto="off"` | `autoPageviews` | Disable automatic pageviews; drive them with `twillingate.page()`. Both default on. |
| `data-mask-url` | `maskUrl` | Rewrite the URL before it is sent. See [Masking](#masking-urls). |
| `data-routing` | `routing` | `history` (default) or `hash`. See [Hash routing](#hash-routing). |
| `data-kind` | `kind` | What this client is: `web` (default), `app`, `cli`, or any short lower-case token. Anything but `web` switches automatic tracking from `$page_view` to `$screen_view` (the route path becomes the screen) and exempts the client from the server's crawler filter. |
| `data-consent` | `consent` | May this instance keep anything on the device. `false` (default): nothing is read from or written to storage. `true`, or the name of a global variable or function a consent manager maintains, unlocks it. See [Consent and storage](#consent-and-storage). |
| `data-instance` | `create(name)` | Register this tag's instance as `twillingate.get(name)` instead of as the default instance. See [Two projects on one page](#two-projects-on-one-page). |

**Every `data-*` attribute has an `init()` equivalent**, enforced by a
test, with one exception: `data-instance` maps to `create()`'s name. The
reverse does not hold: `flushInterval`, `platform`, `appVersion`,
`storage`, `taggedEvents`, `optOut` and `debug` are code-only. Identity is
set from code too, through `identify()`, `group()` and `installId()`,
never pasted into markup.

Views are automatic, including on `history.pushState` and `popstate`, so
single-page apps need no extra code. Elements carrying
`data-twillingate-event` are tracked on click or submit; see [Tagged
elements](#tagged-elements).

Include each tag once. A second copy with the same `data-key`, or with
none, leaves the first in place and logs a warning; a second copy with a
different key replaces the default instance, with a warning. A second
project on the page gives its tag a `data-instance` instead.

**Migrating from Plausible?** The collector also serves
`/js/plausible-shim.js`, an optional second tag that fires events from
Plausible's `plausible-event-*` CSS classes, so a site whose CTAs are
tagged that way keeps working without touching the markup. See
[docs/plausible/](plausible/).

### From code

Load the file without `data-key` and it stays dormant. Or inject the same
tag when the application decides analytics should run:

```js
function loadTwillingate(src) {
  return new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src = src;                       // no data-key: loads dormant
    s.onload = () => resolve(window.twillingate);
    s.onerror = () => reject(new Error("twillingate.js failed to load"));
    document.head.appendChild(s);
  });
}
const tg = await loadTwillingate("https://twillingate.example.com/js/twillingate.js");
tg.init({ key: "ak_9f3c…", consent: () => cmp.hasConsent("analytics") });
```

Setting `s.dataset.key` before appending makes the injected script
auto-init on load, as a pasted tag would. Then initialise:

```js
twillingate.init({
  key: "ak_9f3c…",             // required; the collector's origin is in the file
  identity: "anonymous",       // or "identified": decides what is sent
  consent: false,              // default; true, or a global's name, unlocks storage
  storage: "localStorage",     // default; sessionStorage, memory, cookie, or a driver object
  autoPageviews: true,         // default; false for programmatic page() calls
  taggedEvents: true,          // default; false to ignore data-twillingate-event markup
  maskUrl: "uuid",
  routing: "history",
  kind: "app",                 // → $kind ("app" for Electron/Tauri, "cli", …)
  platform: "electron",        // → $platform; defaults to "web" only for kind "web"
  appVersion: "2.4.1",         // → $app_version
  flushInterval: 1000,
  optOut: () => location.hostname === "localhost",   // OR-ed with twillingate_ignore
  debug: false,                // OR-ed with the twillingate_debug flag
});
```

`init()` returns the instance. A second `init()` on a live instance warns
and is ignored.

**Every call is legal before `init()`.** Configuration — `onPage`,
`onEvent`, `attrs`, `identify`, `group`, `installId`, `consent`, `optOut`,
`debug` — takes effect at once; `page`, `screen`, `track` and `flush` are
held in order and run right after `init()`, entry pageview first. That is
what puts the user on the first event of a programmatic instance:

```js
const et = twillingate.create("econumo");   // dormant until init()
et.identify(userHash);                       // known at render time
et.group(workspaceId);
et.init({ key: "ak_econumo…", identity: "identified", autoPageviews: false, taggedEvents: false });
```

Identity set before `init()` stands; a stored value loads only for what
was not set. The hold is capped at 500 calls, oldest dropped.

### Runtime API

```js
twillingate.page();                            // $page_view for the current page
twillingate.page("/settings");                 // $page_view for an explicit path
twillingate.page({ section: "docs" });         // current page with extra attributes
twillingate.onPage((p) => ({ ab: "b" }));      // pageview listener; page(fn) is deprecated
twillingate.onEvent((e) => ({ app: "x" }));    // listener for every event
twillingate.screen("/settings");               // $screen_view (any kind)
twillingate.track("signup", { plan: "pro" });  // opt-in product event
twillingate.attrs({ tier: "beta" });           // default attributes on every event
twillingate.identify("user-123", "Ada");       // $user_id + optional $user_name
twillingate.group("org-9", "Acme Corp");       // $group_id + optional $group_name
twillingate.installId("018f…");                // $install_id for apps; no argument reads it
twillingate.reset();                           // on logout — see below
twillingate.consent(true);                     // pin storage consent; false withdraws, null hands back, none reads
twillingate.optOut(true);                      // write twillingate_ignore; false clears; none reads
twillingate.debug(true);                       // write twillingate_debug; false clears; none reads
twillingate.flush();                           // force-send the queue
twillingate.create("et", { key: "ak_…" });     // a second instance; see below
twillingate.get("et");                         // look it up from anywhere
twillingate.util.maskIds(path);                // helpers, see Masking
```

- `track(name, attrs)` — product event; don't `$`-prefix your names.
- `attrs(attrs)` — default attributes under every event. Successive calls
  merge; `attrs(null)` clears.
- **Precedence** for every event: values the SDK derives (`$host`,
  `$path`, `$referrer`, campaign parameters, display size), then `attrs()`
  defaults, then the call's own attributes, then what listeners return in
  registration order. Later layers win. A `null` or `undefined` value
  drops the key after the last layer, so
  `twillingate.attrs({ $host: "selfhosted_ab12", $referrer: null })` makes
  every pageview carry that host and no referrer. Batch attributes
  (`$os`, `$browser`, …) come from detection and are not changeable.
- `page(arg?, attrs?)` — no argument records the current page; a string
  records that path (with no campaign parameters); an object is extra
  attributes for the current page.
- `onPage(fn)` — a pageview listener; see [Listeners](#listeners).
  `page(fn)` still registers one, with a deprecation warning.
- `onEvent(fn)` — runs for every event after `onPage` has finished with a
  pageview. Receives `{ name, attributes }`; returns attributes to merge,
  `false` to drop the event, anything else to observe. A listener that
  throws drops the event with a warning.
- `consent(granted?)` — see [Consent and storage](#consent-and-storage).
- `identify(user, name?)` — sets `$user_id` and the optional `$user_name`.
  Inert on an anonymous instance, with one warning. Persisted for an
  identified instance with consent. Events already sent stay unattributed.
- `group(id, name?)` — sets `$group_id`/`$group_name` in every mode;
  persisted with consent for an identified instance.
- `installId(id?)` — sets the stable per-install id an app supplies as
  `$install_id`; with no argument returns what would be sent (the declared
  id, else the persisted visitor id, else `null`). Inert on an anonymous
  instance.
- `reset()` — **required on logout.** Clears user, group, names, the
  visitor id and the retry queue.
- `optOut(flag?)` — `true` writes `twillingate_ignore`, `false` clears it;
  the call returns the effective state, the `optOut` callback included.
- `debug(flag?)` — `true` writes `twillingate_debug`, `false` clears it;
  returns the effective state. See [Debugging](#debugging).
- `screen(name, attrs?)` — an explicit `$screen_view`.

### Two projects on one page

`window.twillingate` is the default instance and a factory. `create(name,
opts?)` builds a second instance whose storage keys are prefixed with the
name (`et_visitor`, `et_user`, `et_queue`, …), registers it and, with
options, initialises it; `get(name)` finds it from any later script;
`get()` is the default. A name already in the registry returns the
existing instance with a warning; the name must match
`^[a-z][a-z0-9_]{0,15}$`. No `window.<name>` is ever created.

```js
const et = twillingate.create("et", { key: "ak_app…", identity: "identified" });
et.track("budget_created");
twillingate.get("et").track("export");
```

A tag can do the same: `data-instance="et"` registers its instance as
`twillingate.get("et")`, with or without `data-key`.

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_web…"></script>
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_app…" data-instance="et" data-auto="off"></script>
```

Browser hooks are installed once: one `pushState` patch, one set of
`popstate`, `hashchange`, `online`, `pagehide`, `visibilitychange` and
tagged-element listeners, whatever the number of instances. Each instance
applies its own settings to what it is handed: `autoPageviews` off
ignores navigations, history routing ignores `hashchange`, `taggedEvents`
off ignores markup. `twillingate_ignore` and `twillingate_debug` stay
global and unprefixed: they are about the person and the page, not one
instance.

### Consent and storage

`consent` answers one question: may this instance keep anything on the
device. It defaults to `false`. Without it, records live in memory:
nothing is read from or written to storage apart from the
`twillingate_ignore` and `twillingate_debug` flags the person set. Reading
no consent also deletes anything an earlier session stored under this
instance's keys. A failed batch is retried from memory — when the browser
fires `online`, and once more through `sendBeacon` when the page is hidden
or unloaded — and lost if the tab closes while delivery keeps failing.

With consent, an `identified` instance persists the visitor id, user and
group, and any instance persists the retry queue. An `anonymous` instance
never sends an identifier at all, so there consent only unlocks the queue.

| `data-consent` / `consent` | Meaning |
| --- | --- |
| absent, `""`, `false`, `"false"` | no consent — the default |
| `true`, `"true"` | consent given |
| a function, or a string naming one on `window` | called whenever consent is consulted; the return value is coerced to a boolean |
| a string naming a non-function global | read whenever consent is consulted, so a variable the consent manager flips is picked up live |

A name that resolves to nothing, or a function that throws, fails closed
to no consent with a console warning. The value is consulted at every
storage decision, never cached. When it flips to true, anything waiting in
the memory retry queue is written to storage and an identified instance
starts persisting a visitor id and the user and group it already holds;
when it flips to false, every key this instance owns is deleted.
`consent(true)` / `consent(false)` pins a value over whatever was
declared; `consent(null)` hands control back; `consent()` reads.

**Where keys live** is the `storage` option:

| `storage` | Meaning |
| --- | --- |
| `"localStorage"` (default) | the browser's localStorage |
| `"sessionStorage"` | identity and queue live for the tab |
| `"memory"` | nothing on the device; equivalent to no consent |
| `"cookie"` | one host-only cookie per identity key, `SameSite=Lax`, one year, `Secure` on https; the retry queue stays in memory |
| `{ get(key), set(key, value), remove(key) }` | a custom driver; a driver that throws counts as unavailable |

A cookie on a shared parent domain is a custom driver:

```js
const cookies = {
  get: (k) => document.cookie.match(new RegExp("(?:^|; )" + k + "=([^;]*)"))?.[1] ?? null,
  set: (k, v) => { document.cookie = `${k}=${v}; domain=.example.com; path=/; max-age=31536000; SameSite=Lax; Secure`; },
  remove: (k) => { document.cookie = `${k}=; domain=.example.com; path=/; max-age=0`; },
};
twillingate.init({ key: "ak_…", identity: "identified", consent: true, storage: cookies });
```

### Detection

The SDK detects the operating system, browser and form factor on the
client and sends them on every batch — `$os`, `$browser` and `$device`
always, `$os_version`, `$os_name` and `$browser_version` when it can
determine them. iPadOS in desktop mode and Brave are the two cases a
User-Agent cannot tell apart; Windows 11 and true macOS versions come from
`navigator.userAgentData.getHighEntropyValues`, started at `init()` and
read at flush time. Detection is the only source: there is no override.

```js
twillingate.detectOS();       // { os: "ipados", osVersion: "17.2", osName: "iPadOS 17.2" }
twillingate.detectBrowser();  // { browser: "brave", browserVersion: "126" }
twillingate.detectDevice();   // { device: "tablet" }
```

Each takes an optional `ClientSignals` — the only inputs detection reads
(`userAgent`, `platform`, `maxTouchPoints`, `brave`, `brands`,
`uaPlatform`, `mobile`, `platformVersion`) — and when one is supplied
consults only what it contains. Detection returns `other` for a
User-Agent that names nothing on the list and `unknown` when there is no
User-Agent at all; `watchos`, `visionos` and `wearable` are never
returned. `$platform` is never detected: it is the option, or `web` while
`kind` is `web`, or absent.

### Masking URLs

A pageview carries its location **already split**: `$host` and `$path`,
stored verbatim. `data-mask-url` receives `location.href` and returns a
URL string, which the SDK then splits:

```
https://www.shop.example.com/account/3f8a91c2-…/edit?utm_source=news#top
  → mask  → https://www.shop.example.com/account/[id]/edit
  → split → { $host: "www.shop.example.com", $path: "/account/[id]/edit" }
```

It is resolved inside `init()`, **before the first pageview**, so the entry
page is covered. Three value forms:

| Form | Detection | Behaviour |
| --- | --- | --- |
| Built-ins | every comma-separated token is `uuid`, `numeric` or `hex` | `util.maskIds(href, opts)` |
| Regexp | value starts with `/` | parsed as `/pattern/flags`; matches replaced with `[id]` |
| Function | anything else | `window[value]`, called with the href |

```html
<script>
  function maskPath(href) { return href.replace(/\/orders\/[A-Z]{2}\d+/, "/orders/[ref]"); }
</script>
<script defer … data-mask-url="maskPath"></script>
```

`init({ maskUrl })` accepts all three plus a `RegExp` or a function
directly. **Campaign parameters are read from the original href before
the mask runs.** **Masking fails closed, loudly:** a named function that
does not exist, an unparseable regexp, a mask that throws or returns a
non-string drops pageviews with one `console.warn`.

#### `twillingate.util`

```js
twillingate.util.maskIds(value, opts?)      // uuid + ulid by default; { numeric, hex } opt-in
twillingate.util.withQuery(path, url, keys) // allowlisted query params, sorted
```

`maskIds` works segment by segment and is URL-aware: given an absolute URL
it masks the path and hash segments and never the host. Numeric is opt-in
because `/2024/annual-report` is a real path; `hex` covers 24+ character
hex blobs.

#### Listeners

`onPage(fn)` registers a listener that runs for every pageview, automatic
ones included. It receives `{url, host, path, referrer, attributes}` and
can return an object to merge attributes or `false` to cancel the
pageview. **Values thread through the chain**: each listener receives the
previous one's `host` and `path`.

```js
twillingate.onPage(({ path }) => ({ $path: path.replace(/^\/account\/[^/]+/, "/account/[id]") }));
twillingate.onPage(({ path }) => ({ $path: path.replace(/\/orders\/\d+/, "/orders/[id]") }));
// /account/88/orders/12 → /account/[id]/orders/[id]
```

- `url` is the **post-mask** URL, not `location.href`.
- Returning `false` cancels; later listeners do not run. A listener that
  throws drops the pageview, with a warning.
- A listener that produces no `$path` drops the pageview, with a warning.

The entry pageview is emitted **synchronously** during `init()`. A
listener registered from code before `init()` covers it; one registered
after cannot, and gets a warning. In snippet mode with a key, `init()` runs
when the tag executes, so use `data-mask-url` for anything that must
cover the entry page.

### Tagged elements

Any element with `data-twillingate-event="signup"` tracks that event on
click (main or middle button) or, for a `<form>`, on submit. The nearest
tagged ancestor of the click target wins; `path` is added as an attribute
so a nav CTA is distinguishable by page. Nothing else is read off the
element. The listeners run in the capture phase, so a handler that stops
propagation cannot eat the event. Every instance with `taggedEvents` on
(the default) tracks it; a programmatic instance opts out with
`taggedEvents: false`.

```html
<button data-twillingate-event="signup">Start free trial</button>
<form data-twillingate-event="newsletter">…</form>
```

### Hash routing

`data-routing="hash"` for an app whose routes live in `location.hash`:

```
https://shop.example.com/app/?utm_source=news#/account/3f8a…/edit?tab=billing
  → { $host: "shop.example.com", $path: "/app/#/account/[id]/edit", $utm_source: "news" }
```

`$path` is `pathname + hash`, with the query stripped from both;
`hashchange` emits a pageview in hash mode only; dedup keys on
`pathname + search + hash`. `util.maskIds` masks hash segments too.

### Query-string routing

`pushState` is hooked and the dedup key includes `location.search`, so
query-only navigations fire distinct pageviews. `$path` carries no query
string by default; appending one is an explicit opt-in:

```js
twillingate.onPage(({ url, path }) => ({
  $path: twillingate.util.withQuery(path, url, ["tab", "view"]),
}));
// → /settings?tab=billing
```

`withQuery` appends only allowlisted parameters and sorts them by key.
Allowlist only low-cardinality parameters.

### Transport

Events queue briefly (~1s) and flush as one batch — on the timer, once 20
events accumulate, on `flush()`, and on page unload (`pagehide` /
`visibilitychange` via `sendBeacon`; the key travels in the JSON body
because beacons cannot set headers). Every event carries a UUID and a
client timestamp.

A batch that fails to send (network down, 5xx) is kept for retry — in
memory, or with [consent](#consent-and-storage) in a bounded stored queue
(`twillingate_queue`, or `<instance>_queue`, 50 batches) — and replays
when the browser fires `online`, once more through `sendBeacon` on unload,
and from storage on the next load. Replays dedupe server-side by event id.
A 4xx response drops the batch instead.

### Debugging

`localStorage.twillingate_debug = "true"` in the console, `debug(true)`, or
the `debug` option logs every event and every send of every instance,
prefixed with the instance name, without changing what is sent:

```
[twillingate] $page_view { $host: "shop.example.com", $path: "/account/[id]" }
[twillingate] sent 1 event(s) → 202
[twillingate:et] budget_created { currency: "EUR" }
```

### Privacy behaviour

- Nothing is kept on the device unless the instance declares
  [consent](#consent-and-storage); an anonymous instance never sends an
  identifier even with it.
- Identified instances with consent: a visitor id persists in
  `twillingate_visitor` (or `<instance>_visitor`) together with the user
  and group, in the configured [storage](#consent-and-storage).
- The SDK does not decide where analytics runs: a `localhost` page, a
  `file://` URL and an automated browser are tracked like any other.
  Keep development traffic out with the `optOut` callback:
  `init({ optOut: () => location.hostname === "localhost" })`.
- Opt a device out: `twillingate.optOut(true)`, or
  `localStorage.twillingate_ignore = "true"` by hand.

### Helpers

| URL | What it does |
| --- | --- |
| `/js/plausible-shim.js` | Fires events from Plausible's `plausible-event-*` classes, so a site migrating off Plausible keeps its tagged CTAs without touching the markup. See `docs/plausible/`. |

````

- [ ] **Step 2: Identity section** — in `### Identity`, after "**The server is always the enforcement point.** A client cannot opt a project into storing raw identifiers." add one sentence:

```md
The JS SDK adds a client-side gate: an instance whose `identity` is
`anonymous` never sends `$user_id`, `$user_name` or `$install_id` at all,
whatever the project's mode.
```

- [ ] **Step 3: Spec status** — change `Status: approved (2026-09-22)` to `Status: implemented (2026-09-22)`.

- [ ] **Step 4: Run the Go docs checks and the full suite**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/api/ ./internal/server/ && cd sdk && npx vitest run && npm run typecheck && npm run build && cd .. && git diff --exit-code internal/server/twillingate.js`
Expected: PASS; the bundle is unchanged since Task 6 (docs do not affect it).

- [ ] **Step 5: Report**

---

## Self-review notes (controller)

- Spec coverage: §1 factory (Task 4), §1a runtime (Task 2 + Task 3 subscriber methods + Task 4 registry sharing), §2 hold (Task 3 + Task 5), §3 identity enforcement (Task 3 + identity tests), §4 precedence/hooks (Task 3 + api/hooks tests), §5 drivers (Task 1 + Task 3), §6 optOut/debug (Task 3 + Task 5), §7 tagged (Task 2 + Task 5), §8 option review incl. overrides removed, origin baked (Task 6), `url` removed (Task 3), §10/§11 (docs Task 7). Breaking list is the PR body's job.
- Type consistency: `Subscriber` methods `onNavigate/onOnline/onUnload/onTagged` in Task 2 match Task 3; `StorageSpec` in Task 1 matches `InitOptions.storage`; `TwillingateGlobal.create(name, opts?)` in Task 4 matches the spec and docs; `installId(id?: string | null)` in Task 3 matches the docs.
- Known residual: `twillingate.test.ts` and `consent.test.ts` are split in Task 3 so Task 4 can compile them; the final review checks nothing was dropped in the move.
