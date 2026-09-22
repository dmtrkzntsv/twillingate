# SDK consent, storage and named instances — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the JS SDK usable for an identified product project without a consent banner and for two projects on one page: storage is gated on a declared `consent`, a second tag names its own global and storage prefix with `data-instance`, a `null` attribute is dropped at emit time, and the loopback/webdriver guard and the legacy `analytics_*` keys go away.

**Architecture:** Everything lands in `sdk/src/` and `docs/`, plus three test-marker edits in Go and one prose string in `internal/api/guide.go`. A new `consent.ts` resolves `data-consent` / `consent` to a function consulted lazily at every storage decision (modelled on `mask.ts`). `Twillingate` keeps its failed-batch retry queue in memory and mirrors it to localStorage only while consent reads true; storage keys are computed from an instance name. Snippet bootstrap moves out of `entry.ts` into an exported `bootstrap()` so it can be tested. No migration, no server change.

**Tech Stack:** TypeScript (ES2019 lib, ES2018 bundle target), vitest + jsdom in `sdk/`, esbuild bundle committed at `internal/server/twillingate.js`, Go tests in `internal/server` and `internal/api`.

**Spec:** `docs/superpowers/specs/2026-09-20-sdk-consent-and-instances-design.md` (copied onto this branch; this plan argues from it).

## Global Constraints

- `consent` defaults to `false` in every identity mode. Without it nothing is read from or written to localStorage except the `twillingate_ignore` opt-out read.
- `consent` accepts exactly: absent / `""` / `false` / `"false"` → no consent; `true` / `"true"` → consent; a function, or a string naming a global function → called at every consult, result coerced to boolean; a string naming a non-function global → read at every consult, coerced to boolean. Unresolvable name or a throwing function → **fails closed to no consent with a console warning** (once per resolver).
- Consent is consulted lazily at every storage decision, never cached at `init()`. Grant transition: pending retry batches are written to storage; an identified instance mints and stores a visitor id from then on. Withdraw transition: every key the instance owns is deleted; records go back to memory.
- With consent: an identified instance persists visitor id, user, user name, group, group name; any instance persists the retry queue. An anonymous instance never persists a visitor id, user or group.
- `consent(granted)` pins; `consent(null)` hands back to the declared spec; `consent()` returns the effective value.
- `reset()` clears the retry queue in every mode.
- `ignored()` keeps only `localStorage.twillingate_ignore === "true"`. The `localhost` / `127.0.0.0/8` / `[::1]` / `file:` / `navigator.webdriver` rules and the `analytics_ignore` fallback are deleted. The `analytics_visitor` / `analytics_user` / `analytics_group` migration is deleted.
- Instance name: validated against `^[a-z][a-z0-9_]{0,15}$`; default `twillingate`. Storage keys are `<name>_visitor`, `<name>_user`, `<name>_user_name`, `<name>_group`, `<name>_group_name`, `<name>_queue`. `twillingate_ignore` is never prefixed.
- `data-instance="et"` registers `window.et` and prefixes keys; `instance: "et"` in `init()` prefixes keys and registers nothing. The attribute wins over a disagreeing option (warning). A foreign global (anything that is not a Twillingate instance) is never overwritten (warning, registration skipped). The duplicate-tag guard reads `window[name]`.
- An event attribute whose value is `null` or `undefined` is dropped at emit time. Applies to the event's own attributes and `attrs()` defaults; batch attributes are untouched.
- Only two new `data-` attributes: `data-consent` and `data-instance`. No environment or app-version `data-` attributes (user rule from #38). Every `data-*` attribute has an `InitOptions` field of the same name (existing parity test).
- The bundle `internal/server/twillingate.js` is build output: rebuild with `cd sdk && npm run build`; never edit by hand. CI diffs it.
- `docs/twillingate.md` changes ship with the code (CLAUDE.md). `TestDocumentMatchesSDK` in `internal/api/docs_sync_test.go` binds documented SDK symbols to `sdk/src/twillingate.ts` in both directions.
- Commit type for the squash: `feat(sdk)!:`. No Go production code changes except the prose string in `internal/api/guide.go` (ruling: it states the loopback rule, which becomes false).
- Implementers do not commit; the controller commits per task.

---

## File structure

| File | Responsibility |
| --- | --- |
| `sdk/src/consent.ts` (new) | `ConsentSpec` type and `resolveConsent()` — spec → lazily consulted `() => boolean`. |
| `sdk/src/consent.test.ts` (new) | Resolver behaviour and the storage semantics under consent. |
| `sdk/src/instances.test.ts` (new) | Named instances, `bootstrap()`, key prefixes, chained `pushState` hooks. |
| `sdk/src/twillingate.ts` | Instance-prefixed keys, `mayStore()` decision point, `consent()` method, memory retry queue mirrored under consent, `instanceName()`, `bootstrap()`, null-drop in `emit()`, reduced `ignored()`. |
| `sdk/src/entry.ts` | Shrinks to `bootstrap(document.currentScript)`. |
| `sdk/src/identity.test.ts`, `twillingate.test.ts`, `api.test.ts` | Adapted: consent opt-ins, legacy-migration block deleted, webdriver test inverted, parity map, null-drop tests. |
| `sdk/vitest.config.ts` | Comment only (the `example.com` URL stays for `$host` assertions). |
| `internal/server/twillingate.js` | Rebuilt bundle. |
| `internal/server/twillingate_script_test.go` | Drop three legacy markers, add `data-consent`, `data-instance`. |
| `internal/api/docs_sync_test.go` | Drop `analytics_ignore`, add the new symbols. |
| `internal/api/guide.go` | Web guide prose: no loopback rule; identified projects store nothing without consent. |
| `docs/twillingate.md` | Snippet table, SDK-only example, runtime API, new "Consent and storage" and "Two tags on one page" sections, transport, privacy, null rule. |
| `docs/superpowers/specs/2026-09-20-sdk-consent-and-instances-design.md` | Status → implemented, one implementation note. |

Task order: 1 (resolver) → 2 (consent-gated storage) → 3 (named instances) → 4 (null drop) → 5 (docs, Go markers, bundle). Tasks 2, 3 and 4 all edit `twillingate.ts` and run strictly in sequence.

Run the SDK suite from `sdk/`: `npm test` (all), `npx vitest run src/consent.test.ts` (one file), `npm run typecheck`. Go at `/usr/local/go/bin/go`.

---

### Task 1: `resolveConsent`

**Files:**
- Create: `sdk/src/consent.ts`
- Test: `sdk/src/consent.test.ts`

**Interfaces:**
- Produces: `export type ConsentSpec = boolean | string | (() => unknown)`; `export function resolveConsent(spec: ConsentSpec | undefined | null): () => boolean`. Task 2 imports both.

- [ ] **Step 1: Write the failing tests**

Create `sdk/src/consent.test.ts`:

```ts
// The consent resolver: data-consent / init({consent}) → a function the
// SDK consults at every storage decision. Storage semantics under consent
// are in the second describe block, added in Task 2.
import { afterEach, describe, expect, it, vi } from "vitest";
import { resolveConsent } from "./consent";

const g = globalThis as Record<string, unknown>;

afterEach(() => {
  vi.restoreAllMocks();
  delete g.consentFlag;
  delete g.consentFn;
});

describe("resolveConsent", () => {
  it("treats absent, empty and false-ish literals as no consent", () => {
    for (const spec of [undefined, null, false, "", "false"] as const) {
      expect(resolveConsent(spec)(), String(spec)).toBe(false);
    }
  });

  it("treats true and \"true\" as consent", () => {
    expect(resolveConsent(true)()).toBe(true);
    expect(resolveConsent("true")()).toBe(true);
  });

  it("calls a function every time and coerces its result", () => {
    const fn = vi.fn().mockReturnValueOnce("yes").mockReturnValueOnce(0);
    const c = resolveConsent(fn);
    expect(c()).toBe(true);
    expect(c()).toBe(false);
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("reads a named global variable live", () => {
    g.consentFlag = false;
    const c = resolveConsent("consentFlag");
    expect(c()).toBe(false);
    g.consentFlag = true;
    expect(c()).toBe(true);
  });

  it("calls a named global function live", () => {
    let granted = false;
    g.consentFn = () => granted;
    const c = resolveConsent("consentFn");
    expect(c()).toBe(false);
    granted = true;
    expect(c()).toBe(true);
  });

  it("fails closed with one warning when the name resolves to nothing", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const c = resolveConsent("noSuchConsent");
    expect(c()).toBe(false);
    expect(c()).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0][0]).toContain("noSuchConsent");
  });

  it("fails closed with one warning when the function throws", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const c = resolveConsent(() => {
      throw new Error("boom");
    });
    expect(c()).toBe(false);
    expect(c()).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("picks up a global that appears after the first consult", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const c = resolveConsent("consentFlag");
    expect(c()).toBe(false);
    g.consentFlag = true;
    expect(c()).toBe(true);
  });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd sdk && npx vitest run src/consent.test.ts`
Expected: FAIL — cannot resolve `./consent`.

- [ ] **Step 3: Implement**

Create `sdk/src/consent.ts`:

```ts
/**
 * What data-consent and init({consent}) accept. The attribute can only
 * carry a string; init additionally takes the native values. A string
 * resolves through this one function either way, so the two entry points
 * cannot drift apart (mask.ts is the model).
 */
export type ConsentSpec = boolean | string | (() => unknown);

/**
 * Resolve a consent spec to a function the SDK calls at every storage
 * decision. Literals become constants. A name is looked up on globalThis
 * at each call, never at resolve time, so a variable a consent manager
 * flips after page load is picked up live; a function is called each time
 * and its result coerced to a boolean.
 *
 * Anything that cannot answer — a name that resolves to nothing, a function
 * that throws — fails closed to false with one console warning. Masking
 * fails closed by dropping pageviews; consent fails closed by writing
 * nothing.
 */
export function resolveConsent(spec: ConsentSpec | undefined | null): () => boolean {
  if (spec === undefined || spec === null || spec === false || spec === "" || spec === "false") {
    return () => false;
  }
  if (spec === true || spec === "true") return () => true;

  let warned = false;
  const fail = (what: string): false => {
    if (!warned) {
      warned = true;
      console.warn(`twillingate: ${what}, treating as no consent`);
    }
    return false;
  };
  const call = (fn: () => unknown, what: string): boolean => {
    try {
      return Boolean(fn());
    } catch (e) {
      return fail(`${what} threw (${String(e)})`);
    }
  };

  if (typeof spec === "function") return () => call(spec, "the consent function");

  const name = spec;
  return () => {
    const v = (globalThis as Record<string, unknown>)[name];
    if (v === undefined) return fail(`consent names nothing on window: ${name}`);
    if (typeof v === "function") return call(v as () => unknown, `consent function ${name}`);
    return Boolean(v);
  };
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd sdk && npx vitest run src/consent.test.ts && npm run typecheck`
Expected: 8 passed; typecheck clean.

- [ ] **Step 5: Commit** (controller)

```bash
git add sdk/src/consent.ts sdk/src/consent.test.ts
git commit -m "feat(sdk): resolve a declared consent value lazily"
```

---

### Task 2: Consent-gated storage and the memory retry queue

**Files:**
- Modify: `sdk/src/twillingate.ts` (header comment, `InitOptions`, storage keys, `ignored()`, `migrated()` deleted, class fields, `init`, `identify`, `group`, `reset`, `flush`, `visitorId`, `store`, `storedBatches`, `replayStored` → `replay`, new `consent`, `mayStore`, `wipe`, `saveQueue`, `saveIdentity`; `autoInit` reads `data-consent`)
- Modify: `sdk/src/identity.test.ts`, `sdk/src/twillingate.test.ts`, `sdk/vitest.config.ts`
- Test: `sdk/src/consent.test.ts` (second describe block)

**Interfaces:**
- Consumes: `resolveConsent`, `ConsentSpec` from Task 1.
- Produces: `InitOptions.consent?: ConsentSpec`; `Twillingate.consent(granted?: boolean | null): boolean`; private `k: Keys` (per-instance key names, computed by `keysFor(name)`) and `private mayStore(): boolean` — Task 3 re-points `k` when an instance name is set. `keysFor(instance: string): Keys` and the `Keys` type are module-level.

- [ ] **Step 1: Write the failing tests**

Append to `sdk/src/consent.test.ts` (extend the import line to `import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";` and add `import { Twillingate, autoInit } from "./twillingate";`):

```ts
const URL_BASE = "https://collector.example.com";

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];
let fetchImpl: (url: string, init: { body: string }) => Promise<{ status: number }>;

function okFetch(_url: string, init: { body: string }): Promise<{ status: number }> {
  sent.push({ body: JSON.parse(init.body) });
  return Promise.resolve({ status: 202 });
}

const failFetch = (): Promise<{ status: number }> => Promise.reject(new TypeError("network down"));

function tg(opts: Partial<Parameters<Twillingate["init"]>[0]> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", url: URL_BASE, flushInterval: 0, ...opts });
  return t;
}

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

const OWNED = ["visitor", "user", "user_name", "group", "group_name", "queue"].map((s) => `twillingate_${s}`);

describe("storage under consent", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    sent = [];
    fetchImpl = okFetch;
    vi.stubGlobal("fetch", (url: string, init: { body: string }) => fetchImpl(url, init));
    localStorage.clear();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("without consent, writes nothing in either identity mode", async () => {
    for (const identity of ["anonymous", "identified"] as const) {
      fetchImpl = failFetch;
      const t = tg({ identity });
      t.track("fails");
      t.flush();
      await drain();
      fetchImpl = okFetch;
      t.identify("u_1", "Ada");
      t.group("org_1", "Acme");
      await lastAttributes(t);
      t.reset();
      expect(localStorage.length, identity).toBe(0);
    }
  });

  it("without consent, an identified instance sends no $install_id", async () => {
    const t = tg({ identity: "identified" });
    const attrs = await lastAttributes(t);
    expect(attrs.$install_id).toBeUndefined();
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
  });

  it("without consent, identity still comes from init and identify() for the session", async () => {
    const t = tg({ identity: "identified", user: "u_init", group: "org_init" });
    expect(await lastAttributes(t)).toMatchObject({ $user_id: "u_init", $group_id: "org_init" });
    t.identify("u_later", "Ada");
    expect(await lastAttributes(t)).toMatchObject({ $user_id: "u_later", $user_name: "Ada" });
    expect(localStorage.length).toBe(0);
  });

  it("retries a failed batch from memory: on `online`, and via sendBeacon on pagehide", async () => {
    fetchImpl = failFetch;
    const t = tg();
    t.track("offline-event");
    await drain();
    expect(sent).toHaveLength(0);
    expect(localStorage.getItem("twillingate_queue")).toBeNull();

    fetchImpl = okFetch;
    window.dispatchEvent(new Event("online"));
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events[0].name).toBe("offline-event");

    // A second failure, then pagehide: the pending batch goes out through sendBeacon.
    fetchImpl = failFetch;
    t.track("second");
    await drain();
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { ...navigator, sendBeacon: beacon });
    window.dispatchEvent(new Event("pagehide"));
    expect(beacon).toHaveBeenCalledOnce();
    expect(JSON.parse(beacon.mock.calls[0][1]).events[0].name).toBe("second");
  });

  it("bounds the memory retry queue at 50 batches, oldest dropped first", async () => {
    fetchImpl = failFetch;
    const t = tg();
    for (let i = 0; i < 55; i++) {
      t.track(`e${i}`);
      t.flush();
    }
    await drain();
    fetchImpl = okFetch;
    window.dispatchEvent(new Event("online"));
    await drain();
    expect(sent).toHaveLength(50);
    expect(sent[0].body.events[0].name).toBe("e5");
    expect(sent[49].body.events[0].name).toBe("e54");
  });

  it("consent(true) writes the pending queue to storage and starts persisting a visitor id", async () => {
    fetchImpl = failFetch;
    const t = tg({ identity: "identified" });
    t.track("pending");
    t.flush();
    await drain();
    expect(localStorage.length).toBe(0);

    expect(t.consent(true)).toBe(true);
    const stored = JSON.parse(localStorage.getItem("twillingate_queue")!);
    expect(stored).toHaveLength(1);
    expect(stored[0].events[0].name).toBe("pending");

    fetchImpl = okFetch;
    const attrs = await lastAttributes(t);
    const visitor = localStorage.getItem("twillingate_visitor");
    expect(visitor).toMatch(/^[0-9a-f-]{36}$/);
    expect(attrs.$install_id).toBe(visitor);
  });

  it("consent(false) deletes every key the instance owns and leaves the opt-out alone", () => {
    for (const k of OWNED) localStorage.setItem(k, "x");
    localStorage.setItem("twillingate_ignore", "false");
    localStorage.setItem("other_app", "keep");
    const t = tg({ identity: "identified", consent: true });
    expect(t.consent(false)).toBe(false);
    for (const k of OWNED) expect(localStorage.getItem(k), k).toBeNull();
    expect(localStorage.getItem("twillingate_ignore")).toBe("false");
    expect(localStorage.getItem("other_app")).toBe("keep");
  });

  it("wipes keys left by an earlier session when consent reads false at init", () => {
    for (const k of OWNED) localStorage.setItem(k, "stale");
    tg({ identity: "identified" });
    for (const k of OWNED) expect(localStorage.getItem(k), k).toBeNull();
  });

  it("a declared function is consulted at each decision; a pin overrides it; null hands back", async () => {
    let granted = false;
    const t = tg({ identity: "identified", consent: () => granted });
    expect((await lastAttributes(t)).$install_id).toBeUndefined();

    granted = true; // no call into the SDK
    const withConsent = await lastAttributes(t);
    expect(withConsent.$install_id).toBe(localStorage.getItem("twillingate_visitor"));

    t.consent(false); // pinned: the function is no longer consulted
    expect((await lastAttributes(t)).$install_id).toBeUndefined();
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    granted = true;
    expect(t.consent()).toBe(false);

    t.consent(null); // back to the function
    expect(t.consent()).toBe(true);
    expect((await lastAttributes(t)).$install_id).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("reset() clears the retry queue with and without consent", async () => {
    for (const consent of [false, true]) {
      sent = [];
      localStorage.clear();
      fetchImpl = failFetch;
      const t = tg({ identity: "identified", consent });
      t.track("stale");
      t.flush();
      await drain();
      t.reset();
      expect(localStorage.getItem("twillingate_queue"), String(consent)).toBeNull();
      fetchImpl = okFetch;
      window.dispatchEvent(new Event("online"));
      await drain();
      expect(sent, String(consent)).toHaveLength(0);
    }
  });

  it("with consent, an anonymous instance persists the queue but never a visitor id or group", async () => {
    fetchImpl = failFetch;
    const t = tg({ consent: true });
    t.group("org_1");
    t.track("x");
    t.flush();
    await drain();
    expect(JSON.parse(localStorage.getItem("twillingate_queue")!)).toHaveLength(1);
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    expect(localStorage.getItem("twillingate_group")).toBeNull();
  });

  it("data-consent resolves a literal, a global variable and a global function", async () => {
    g.consentFlag = true;
    g.consentFn = () => true;
    for (const value of ["true", "consentFlag", "consentFn"]) {
      localStorage.clear();
      sent = [];
      const t = new Twillingate();
      autoInit(t, scriptTag({ "data-key": "ak_s", "data-identity": "identified", "data-consent": value, "data-auto": "off" }));
      const attrs = await lastAttributes(t);
      expect(attrs.$install_id, value).toBe(localStorage.getItem("twillingate_visitor"));
      expect(localStorage.getItem("twillingate_visitor"), value).not.toBeNull();
    }
  });

  it("data-consent naming nothing fails closed with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_s", "data-identity": "identified", "data-consent": "noSuchThing", "data-auto": "off" }));
    const attrs = await lastAttributes(t);
    expect(attrs.$install_id).toBeUndefined();
    expect(localStorage.length).toBe(0);
    expect(warn.mock.calls.flat().join(" ")).toContain("noSuchThing");
  });

  it("tracks on a page an automated browser drives", async () => {
    vi.stubGlobal("navigator", { ...navigator, webdriver: true });
    const t = tg();
    t.track("robot");
    await drain();
    expect(sent).toHaveLength(1);
  });

  it("honours twillingate_ignore only", async () => {
    const t = tg();
    localStorage.setItem("analytics_ignore", "true");
    t.track("legacy-opt-out-no-longer-honoured");
    localStorage.removeItem("analytics_ignore");
    localStorage.setItem("twillingate_ignore", "true");
    t.track("nope");
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events[0].name).toBe("legacy-opt-out-no-longer-honoured");
  });
});
```

- [ ] **Step 2: Adapt the existing tests**

`sdk/src/identity.test.ts`:
- Header comment: `// Identity lifecycle: anonymous vs identified storage semantics under consent, identify/group/reset, and pageviews.`
- In `"identify() carries the user for the session but does not persist it"` replace the last three `expect` lines with `expect(localStorage.length).toBe(0);`.
- Every `tg({ identity: "identified" ... })` in `describe("identified mode")` gains `consent: true` (six call sites, including `t2`).
- Delete the whole `describe("migration from the legacy storage keys", …)` block.
- Replace `describe("group()", …)` with:

```ts
describe("group()", () => {
  it("persists the group for an identified instance with consent", async () => {
    const t = tg({ identity: "identified", consent: true });
    t.group("org_77");
    const attrs = await lastAttributes(t);
    expect(attrs.$group_id).toBe("org_77");
    expect(localStorage.getItem("twillingate_group")).toBe("org_77");
  });

  it("carries the group for the session only in anonymous mode, consent or not", async () => {
    const t = tg({ consent: true });
    t.group("org_77");
    expect((await lastAttributes(t)).$group_id).toBe("org_77");
    expect(localStorage.getItem("twillingate_group")).toBeNull();
  });
});
```

`sdk/src/twillingate.test.ts`:
- In `describe("failure handling and the offline queue")`: `"persists the batch when fetch rejects…"` → `tg({ consent: true })`; `"replays a stored queue from a previous session on init"` → `tg({ consent: true })`; `"keeps a batch that fails on 5xx…"` → `tg({ consent: true })`; `"bounds the offline queue at 50 batches…"` → `tg({ consent: true })`; `"survives a corrupt stored queue"` → `tg({ consent: true })`.
- Delete `describe("ignore rules", …)` entirely (replaced by the two tests in consent.test.ts).
- In `describe("environment")` change the `stubNavigator` comment to `// Replace the whole navigator: the batch reads language, send reads sendBeacon, detection reads the rest.` and drop `webdriver: false` from that stub and from the two `sendBeacon` stubs in `describe("batching")`.
- In the parity test `optionFor`, add `consent: "consent",`.

`sdk/vitest.config.ts`: replace the two comment lines with `// A real hostname, so $host assertions read example.com.`

- [ ] **Step 3: Run to verify the new tests fail**

Run: `cd sdk && npx vitest run src/consent.test.ts`
Expected: FAIL — `consent` is not a function / `$install_id` present without consent.

- [ ] **Step 4: Implement in `sdk/src/twillingate.ts`**

Header comment, replace lines 12–14 (`The identity model matches…fails safe.`) with:

```
 * Nothing is kept on the device unless the tag declares consent
 * (data-consent); with it, data-identity decides whether identity persists.
 * The server salts anonymous projects no matter what the client claims, so
 * a misconfigured client fails safe.
```

Imports: add `import { resolveConsent, type ConsentSpec } from "./consent";` after the mask import.

`InitOptions`: replace the `identity` doc comment with `/** Mirrors the project's identity mode. With consent it decides whether the visitor id, user and group persist; without consent nothing does. The server enforces the real mode. */` and add, directly after `group?: string;`:

```ts
  /**
   * May this instance keep anything on the device. Default false: records
   * live in memory and nothing is read from or written to localStorage.
   * true, or a function or global name a consent manager maintains,
   * unlocks it; consulted at every storage decision, never cached.
   */
  consent?: ConsentSpec;
```

Replace the storage-key block (the comment at line 129 through `const LEGACY = …`) with:

```ts
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
```

Replace `ignored()` and delete `migrated()`:

```ts
// The only thing the SDK decides on its own is the person's opt-out.
// Whether analytics should run at all (a developer's localhost, a test
// browser) belongs to the product, which can skip init() on a condition
// it knows.
function ignored(): boolean {
  return ls(IGNORE) === "true";
}
```

Class fields: after `private flushInterval = 1000;` add:

```ts
  private k: Keys = keysFor(DEFAULT_INSTANCE);
  private consentSpec: () => boolean = () => false;
  private consentPin: boolean | null = null;
  // null until the first decision point: the first false read wipes keys
  // an earlier session may have left behind.
  private lastConsent: boolean | null = null;
  // Failed batches waiting for a retry. Lives in memory; mirrored to
  // storage only while consent reads true.
  private pending: Batch[] = [];
```

`init()`: replace the five lines from `this.identified = …` through `this.groupName = ls(GROUP_NAME);` with:

```ts
    this.identified = opts.identity === "identified";
    this.consentSpec = resolveConsent(opts.consent);
    const stored = this.identified && this.mayStore();
    this.userId = opts.user ? String(opts.user) : stored ? ls(this.k.user) : null;
    this.userName = stored ? ls(this.k.user_name) : null;
    this.groupId = opts.group ? String(opts.group) : stored ? ls(this.k.group) : null;
    this.groupName = stored ? ls(this.k.group_name) : null;
```

and replace `this.replayStored();` / `addEventListener("online", () => this.replayStored());` with `this.replay();` / `addEventListener("online", () => this.replay());`.

`identify()` and `group()`:

```ts
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
```

Update the `identify` doc comment's second sentence to: `Persisted for identified projects with consent, so every later event — this page and future loads — carries the identity.`

`reset()`:

```ts
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
```

`flush()`: after the `while` loop add:

```ts
    // Unloading is the last chance for batches whose delivery failed: try
    // each once more through sendBeacon. They stay in pending (and, with
    // consent, in storage) so a hidden tab that comes back can retry; a
    // replay of an already-delivered batch dedupes on the server by id.
    if (unloading) for (const batch of this.pending) this.send(batch, true);
```

Add the public `consent()` method after `flush()`:

```ts
  /**
   * Pin storage consent (true/false) over whatever the tag declared, hand
   * control back to the declared value (null), or read the effective value
   * (no argument). Granting writes the pending retry queue to storage and,
   * for an identified instance, starts persisting a visitor id; withdrawing
   * deletes every key this instance owns.
   */
  consent(granted?: boolean | null): boolean {
    if (granted !== undefined) this.consentPin = granted === null ? null : Boolean(granted);
    return this.mayStore();
  }
```

Replace `visitorId()`:

```ts
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
```

Replace `store()`, `storedBatches()` and `replayStored()`:

```ts
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
        if (this.pending.length) lsSet(this.k.queue, JSON.stringify(this.pending));
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

  // With consent the stored copy is authoritative: it holds everything
  // pending was mirrored into, plus what another tab may have written.
  private replay(): void {
    const batches = this.mayStore() ? this.storedBatches() : this.pending;
    this.pending = [];
    this.saveQueue();
    for (const batch of batches) this.send(batch, false); // a failure re-stores itself
  }
```

`autoInit()`: add `consent: script.getAttribute("data-consent") || undefined,` after the `group:` line.

Remove every remaining reference to `VISITOR`, `USER`, `USER_NAME`, `GROUP`, `GROUP_NAME`, `QUEUE`, `LEGACY`, `migrated` (`npm run typecheck` will list any left).

- [ ] **Step 5: Run the whole suite**

Run: `cd sdk && npm test && npm run typecheck`
Expected: all green. If `identity.test.ts` "an explicit installId wins over the stored visitor id" fails, it needs `consent: true` too (it seeds `twillingate_visitor` — it passes either way since `installId` wins, but the seeded key is wiped at init without consent; both outcomes satisfy the assertion).

- [ ] **Step 6: Commit** (controller)

```bash
git add sdk/src sdk/vitest.config.ts
git commit -m "feat(sdk)!: keep records in memory unless the tag declares consent"
```

---

### Task 3: Named instances

**Files:**
- Modify: `sdk/src/twillingate.ts` (constructor, `InitOptions.instance`, `init()` instance handling, `instanceName()`, `supersededBy()` name parameter, `bootstrap()`, `autoInit` unchanged)
- Modify: `sdk/src/entry.ts`
- Modify: `sdk/src/twillingate.test.ts` (parity map)
- Test: `sdk/src/instances.test.ts`

**Interfaces:**
- Consumes: `keysFor`, `Keys`, `DEFAULT_INSTANCE`, `this.k` from Task 2.
- Produces: `constructor(instance?: string)`; `InitOptions.instance?: string`; `export function instanceName(name: string | null | undefined): string`; `export function supersededBy(existing: unknown, script: HTMLScriptElement | null, name = "twillingate"): boolean`; `export function bootstrap(script: HTMLScriptElement | null): Twillingate | null`.

- [ ] **Step 1: Write the failing tests**

Create `sdk/src/instances.test.ts`:

```ts
// Named instances: a second tag on one page gets its own global and its
// own storage prefix; a bundled consumer gets the prefix alone.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, bootstrap, instanceName } from "./twillingate";

const URL_BASE = "https://collector.example.com";
const g = globalThis as Record<string, unknown>;

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];
let fetchImpl: (url: string, init: { body: string }) => Promise<{ status: number }>;

function okFetch(_url: string, init: { body: string }): Promise<{ status: number }> {
  sent.push({ body: JSON.parse(init.body) });
  return Promise.resolve({ status: 202 });
}

function tg(opts: Partial<Parameters<Twillingate["init"]>[0]> = {}, instance?: string): Twillingate {
  const t = new Twillingate(instance);
  t.init({ key: "ak_test", url: URL_BASE, flushInterval: 0, ...opts });
  return t;
}

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

const nativePushState = history.pushState;

beforeEach(() => {
  vi.useFakeTimers();
  sent = [];
  fetchImpl = okFetch;
  vi.stubGlobal("fetch", (url: string, init: { body: string }) => fetchImpl(url, init));
  localStorage.clear();
  history.replaceState(null, "", "/start");
  delete g.twillingate;
  delete g.et;
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  history.pushState = nativePushState;
  delete g.twillingate;
  delete g.et;
});

describe("instanceName", () => {
  it("accepts an identifier and falls back to the default otherwise, with a warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    expect(instanceName("et")).toBe("et");
    expect(instanceName("a_1")).toBe("a_1");
    expect(instanceName(null)).toBe("twillingate");
    expect(instanceName(undefined)).toBe("twillingate");
    expect(instanceName("")).toBe("twillingate");
    expect(warn).not.toHaveBeenCalled();
    for (const bad of ["Et", "1et", "e-t", "e t", "a".repeat(17)]) {
      expect(instanceName(bad), bad).toBe("twillingate");
    }
    expect(warn).toHaveBeenCalledTimes(5);
  });
});

describe("data-instance", () => {
  it("registers the named global, leaves window.twillingate alone and prefixes storage keys", async () => {
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-identity": "identified", "data-consent": "true", "data-auto": "off" }));
    expect(et).toBeInstanceOf(Twillingate);
    expect(g.et).toBe(et);
    expect(g.twillingate).toBeUndefined();
    expect((et as Twillingate & { VERSION: string }).VERSION).toBeTypeOf("string");
    const attrs = await lastAttributes(et!);
    expect(localStorage.getItem("et_visitor")).toBe(attrs.$install_id);
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    et!.identify("u_1", "Ada");
    et!.group("org_1", "Acme");
    expect(localStorage.getItem("et_user")).toBe("u_1");
    expect(localStorage.getItem("et_user_name")).toBe("Ada");
    expect(localStorage.getItem("et_group")).toBe("org_1");
    expect(localStorage.getItem("et_group_name")).toBe("Acme");
  });

  it("loads dormant without data-key for init() in code", async () => {
    const et = bootstrap(scriptTag({ "data-instance": "et" }))!;
    expect(g.et).toBe(et);
    await drain();
    expect(sent).toHaveLength(0);
    et.init({ key: "ak_code", url: URL_BASE, flushInterval: 0 });
    expect((await lastAttributes(et)).$kind).toBe("web");
    expect(sent[0].body.key).toBe("ak_code");
  });

  it("without the attribute, behaviour is exactly as today", () => {
    const t = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }));
    expect(g.twillingate).toBe(t);
    expect(g.et).toBeUndefined();
  });

  it("beats a conflicting instance option, with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const et = bootstrap(scriptTag({ "data-instance": "et" }))!;
    et.init({ key: "ak_code", url: URL_BASE, flushInterval: 0, identity: "identified", consent: true, instance: "other" });
    await lastAttributes(et);
    expect(localStorage.getItem("et_visitor")).not.toBeNull();
    expect(localStorage.getItem("other_visitor")).toBeNull();
    expect(warn.mock.calls.flat().join(" ")).toContain("other");
  });

  it("refuses an invalid name and keeps the default", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = bootstrap(scriptTag({ "data-key": "ak_web", "data-instance": "Not-Valid", "data-auto": "off" }));
    expect(g.twillingate).toBe(t);
    expect(g["Not-Valid"]).toBeUndefined();
    expect(warn).toHaveBeenCalled();
  });

  it("never overwrites a foreign global", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    g.et = { theirs: true };
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }));
    expect(g.et).toEqual({ theirs: true });
    expect(warn.mock.calls.flat().join(" ")).toContain("et");
    // The tag still tracks; it is just not reachable through the global.
    expect((await lastAttributes(et!)).$kind).toBe("web");
  });

  it("stands down for a duplicate of the same named tag, not for the default tag beside it", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const web = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }));
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }));
    expect(web).not.toBeNull();
    expect(et).not.toBeNull();
    expect(bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }))).toBeNull();
    expect(bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }))).toBeNull();
    expect(g.twillingate).toBe(web);
    expect(g.et).toBe(et);
  });

  it("two tags both emit a pageview on pushState, each under its own key", async () => {
    bootstrap(scriptTag({ "data-key": "ak_web" }));
    bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et" }));
    history.pushState(null, "", "/second");
    (g.twillingate as Twillingate).flush();
    (g.et as Twillingate).flush();
    await drain();
    const byKey = (key: string) =>
      sent.filter((s) => s.body.key === key).flatMap((s) => s.body.events.map((e) => (e.attributes as Record<string, string>).$path));
    expect(byKey("ak_web")).toEqual(["/start", "/second"]);
    expect(byKey("ak_et")).toEqual(["/start", "/second"]);
  });
});

describe("instance option", () => {
  it("prefixes storage keys and registers no global", async () => {
    const t = tg({ identity: "identified", consent: true, instance: "et" });
    const attrs = await lastAttributes(t);
    expect(localStorage.getItem("et_visitor")).toBe(attrs.$install_id);
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    expect(g.et).toBeUndefined();
  });

  it("two code instances with different names share neither a visitor id nor a queue", async () => {
    const a = tg({ identity: "identified", consent: true, instance: "alpha" });
    const b = tg({ identity: "identified", consent: true, instance: "beta" });
    const va = (await lastAttributes(a)).$install_id;
    const vb = (await lastAttributes(b)).$install_id;
    expect(va).not.toBe(vb);
    fetchImpl = () => Promise.reject(new TypeError("down"));
    a.track("a-fails");
    a.flush();
    await drain();
    expect(JSON.parse(localStorage.getItem("alpha_queue")!)).toHaveLength(1);
    expect(localStorage.getItem("beta_queue")).toBeNull();
  });

  it("two code instances with no name share one visitor id", async () => {
    const a = tg({ identity: "identified", consent: true });
    const b = tg({ identity: "identified", consent: true });
    expect((await lastAttributes(a)).$install_id).toBe((await lastAttributes(b)).$install_id);
  });

  it("refuses an invalid name and keeps the default, with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = tg({ identity: "identified", consent: true, instance: "Bad Name" });
    await lastAttributes(t);
    expect(localStorage.getItem("twillingate_visitor")).not.toBeNull();
    expect(warn).toHaveBeenCalled();
  });
});
```

In `sdk/src/twillingate.test.ts` parity map add `instance: "instance",`.

- [ ] **Step 2: Run to verify it fails**

Run: `cd sdk && npx vitest run src/instances.test.ts`
Expected: FAIL — `bootstrap` / `instanceName` not exported.

- [ ] **Step 3: Implement**

`sdk/src/twillingate.ts`:

`InitOptions`, after `consent?: ConsentSpec;`:

```ts
  /**
   * Storage-key prefix for a bundled consumer that shares a page with
   * another instance. Registers no global. A tag that declared
   * data-instance ignores a disagreeing value here.
   */
  instance?: string;
```

Module-level, after `keysFor`:

```ts
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

function isInstance(x: unknown): x is Twillingate {
  return !!x && typeof (x as Twillingate).init === "function";
}
```

Class: add fields and a constructor (place the constructor right after `private ready = false;`):

```ts
  private instance = DEFAULT_INSTANCE;
  // True when the name came from data-instance: the tag is registered
  // under it and already reading prefixed keys, so init() cannot rename.
  private declared = false;

  constructor(instance?: string) {
    if (instance !== undefined) {
      this.declared = true;
      this.useInstance(instance);
    }
  }

  private useInstance(name: string): void {
    this.instance = name;
    this.k = keysFor(name);
  }
```

`init()`: insert right after the `this.url` check block (before `this.identified = …`):

```ts
    if (opts.instance !== undefined) {
      if (this.declared) {
        if (opts.instance !== this.instance) {
          console.warn(`twillingate: data-instance="${this.instance}" is already set; ignoring instance "${opts.instance}"`);
        }
      } else {
        this.useInstance(instanceName(opts.instance));
      }
    }
```

`supersededBy`: signature `export function supersededBy(existing: unknown, script: HTMLScriptElement | null, name = DEFAULT_INSTANCE): boolean`; use `isInstance(existing)` for the first check; the second warning becomes `` `twillingate: a second twillingate.js replaced window.${name} (${loadedKey} -> ${key})` ``. Update its doc comment first sentence to "…leave the instance already at `window[name]` in place."

Add after `supersededBy`:

```ts
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
  const g = globalThis as Record<string, unknown>;
  const existing = g[name];
  if (supersededBy(existing, script, name)) return null;
  const tg = new Twillingate(attr === null ? undefined : name);
  (tg as Twillingate & { VERSION: string }).VERSION = VERSION;
  if (existing === undefined || existing === null || isInstance(existing)) {
    g[name] = tg;
  } else {
    console.warn(`twillingate: window.${name} is already taken by something else; the instance is not registered there`);
  }
  autoInit(tg, script);
  return tg;
}
```

`sdk/src/entry.ts` becomes:

```ts
// Bundle entry: register the SDK on window (twillingate, or the name in
// data-instance) and auto-init in snippet mode (a data-key on the loading
// <script> tag).
import { Twillingate, bootstrap } from "./twillingate";

declare global {
  interface Window {
    twillingate: Twillingate & { VERSION: string };
  }
}

bootstrap(document.currentScript as HTMLScriptElement | null);
```

- [ ] **Step 4: Run the whole suite**

Run: `cd sdk && npm test && npm run typecheck`
Expected: all green.

- [ ] **Step 5: Commit** (controller)

```bash
git add sdk/src
git commit -m "feat(sdk): name a second instance with data-instance"
```

---

### Task 4: A `null` attribute is dropped at emit time

**Files:**
- Modify: `sdk/src/twillingate.ts` (`emit()`)
- Test: `sdk/src/api.test.ts`

**Interfaces:** none new.

- [ ] **Step 1: Write the failing tests**

Append to `sdk/src/api.test.ts`:

```ts
describe("null drops an attribute", () => {
  it("omits null and undefined values, keeps 0 and empty strings", async () => {
    const t = tg();
    t.track("e", { a: null, b: undefined, c: 0, d: "" });
    t.flush();
    await drain();
    expect(lastEvent().attributes).toEqual({ c: 0, d: "" });
  });

  it("suppresses a derived pageview attribute for one call, while $host overrides reach the wire", async () => {
    Object.defineProperty(document, "referrer", { value: "https://news.example.org/", configurable: true });
    const t = tg();
    t.page("/budget", { $host: "selfhosted_ab12", $referrer: null });
    t.flush();
    await drain();
    const attrs = lastEvent().attributes as Record<string, unknown>;
    expect(attrs.$host).toBe("selfhosted_ab12");
    expect(attrs.$path).toBe("/budget");
    expect(attrs).not.toHaveProperty("$referrer");
  });

  it("lets an event null out an attrs() default", async () => {
    const t = tg();
    t.attrs({ region: "eu", tier: "beta" });
    t.track("e", { region: null });
    t.flush();
    await drain();
    expect(lastEvent().attributes).toEqual({ tier: "beta" });
  });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd sdk && npx vitest run src/api.test.ts`
Expected: FAIL — `a: null` present in the body.

- [ ] **Step 3: Implement**

In `emit()` replace `attributes = { ...this.defaultAttrs, ...attributes };` with:

```ts
    // A null or undefined value drops the key: the way to suppress a value
    // the SDK derives on its own ($referrer) for one call. Applies to the
    // event's attributes and attrs() defaults; batch attributes are
    // untouched and the server layers the event over them key by key.
    const merged: Record<string, unknown> = { ...this.defaultAttrs, ...attributes };
    attributes = {};
    for (const key of Object.keys(merged)) {
      if (merged[key] !== null && merged[key] !== undefined) attributes[key] = merged[key];
    }
```

- [ ] **Step 4: Run the whole suite**

Run: `cd sdk && npm test && npm run typecheck`
Expected: all green.

- [ ] **Step 5: Commit** (controller)

```bash
git add sdk/src
git commit -m "feat(sdk): drop an attribute set to null before sending"
```

---

### Task 5: Docs, Go test markers, guide prose, bundle, spec status

**Files:**
- Modify: `docs/twillingate.md`
- Modify: `internal/api/docs_sync_test.go` (`TestDocumentMatchesSDK` list)
- Modify: `internal/api/guide.go` (two prose strings)
- Modify: `internal/server/twillingate_script_test.go` (markers)
- Modify: `docs/superpowers/specs/2026-09-20-sdk-consent-and-instances-design.md` (status)
- Rebuild: `internal/server/twillingate.js`

**Interfaces:** consumes every symbol from Tasks 1–4 by name: `data-consent`, `data-instance`, `consent`, `instance`, `consent()`.

- [ ] **Step 1: Update the Go tests first (they fail until the docs and bundle catch up)**

`internal/api/docs_sync_test.go`, in `TestDocumentMatchesSDK`:
- change the first two lines of the list to `"data-key", "data-identity", "data-user", "data-group", "data-auto",` / `"data-mask-url", "data-routing", "data-consent", "data-instance",`
- change `"init", "page", …, "flush",` to end with `"flush", "consent", "instance",`
- change `"twillingate_ignore", "analytics_ignore",` to `"twillingate_ignore",`.

`internal/server/twillingate_script_test.go`: delete the `analytics_ignore`, `analytics_visitor` and `webdriver` marker lines. Storage keys are now built as `<instance>_<suffix>`, so the literals `twillingate_visitor` and `twillingate_queue` no longer exist in the bundle: replace those two markers with `"user_name",           // stored identity (key suffix)` and `"queue",               // retry queue (key suffix)`. Add `"data-consent",        // storage consent wiring` and `"data-instance",       // second tag on one page` after `"data-key"`.

Run: `/usr/local/go/bin/go test ./internal/api -run TestDocumentMatchesSDK` → FAIL (docs lack `data-consent`).

- [ ] **Step 2: `docs/twillingate.md`**

Snippet table: replace the `data-identity` row with

```
| `data-identity` | `identity` | `anonymous` (default) or `identified`. Mirrors the project's server-side mode; the server enforces the real one regardless. With [consent](#consent-and-storage), `identified` persists the visitor id, user and group in localStorage; without it nothing persists in either mode. |
```

and add after the `data-kind` row:

```
| `data-consent` | `consent` | May this instance keep anything on the device. `false` (default): nothing is read from or written to localStorage. `true`, or the name of a global variable or function a consent manager maintains, unlocks it. See [Consent and storage](#consent-and-storage). |
| `data-instance` | `instance` | Name for a second tag on the same page: registers `window.<name>` instead of `window.twillingate` and prefixes this instance's storage keys. See [Two tags on one page](#two-tags-on-one-page). |
```

Replace the paragraph starting `Include the tag once.` with:

```
Include each tag once. If a second copy loads with the same `data-key`, or
with none, it leaves the first instance in place and logs a console warning,
so the page isn't counted twice. A second copy with a *different* key
replaces `window.twillingate` with a warning; two projects on one page give
the second tag a `data-instance` instead — see
[Two tags on one page](#two-tags-on-one-page).
```

SDK-only example: after the `identity:` line add

```
  consent: false,              // default; true, or a global's name, unlocks localStorage
  instance: "et",              // storage-key prefix when another instance shares the page
```

Runtime API code block: after the `reset()` line add
`twillingate.consent(true);                     // pin storage consent; false withdraws, null hands back, none reads`.
In the bullet list:
- `identify` bullet: `persisted for \`identified\` projects with consent so every later event carries the identity.`
- `group` bullet: `` - `group(id, name?)` — sets `$group_id`/`$group_name`; persisted with consent for `identified` projects. ``
- `reset` bullet last sentence: `Clears user, group, names, the visitor id and the retry queue.`
- add after the `page` bullet:

```
- An attribute whose value is `null` or `undefined` is dropped before the
  event is sent — the way to suppress a value the SDK derives on its own,
  such as `$referrer`, for one call:
  `twillingate.page("/budget", { $host: "selfhosted_ab12", $referrer: null })`
  sends neither the real host nor the referrer. The rule applies to the
  event's own attributes, including `attrs()` defaults; batch attributes
  (`$os`, `$browser`, …) come from detection or `init()` options and are
  changed there.
- `consent(granted?)` — `true`/`false` pins storage consent over what the
  tag declared, `null` hands control back to it, no argument returns the
  effective value. See [Consent and storage](#consent-and-storage).
```

Insert two new sections between `### Runtime API` and `### Detection`:

````
### Consent and storage

`consent` answers one question: may this instance keep anything on the
device. It defaults to `false` in every identity mode. Without it, records
live in memory: nothing is read from or written to localStorage, apart from
the `twillingate_ignore` opt-out the person set themselves. Identity comes
from the host application on each load, through `user` and `group` in
`init()` or `identify()`, and a failed batch is retried from memory — again
when the browser fires `online`, and once more through `sendBeacon` on
`pagehide` — and lost if the tab closes while delivery keeps failing.
Retries stay safe because every event carries an id the server dedupes.

With consent, an `identified` instance persists the visitor id, user and
group in localStorage and any instance persists the retry queue so it
survives a reload. An `anonymous` instance never persists a visitor id (the
server salts it daily, so it would buy nothing); there, consent only
unlocks the queue.

The SDK is not a consent manager: it does not ask, record proof or remember
the answer. The tag declares what the site holds for this person:

| `data-consent` / `consent` | Meaning |
| --- | --- |
| absent, `""`, `false`, `"false"` | no consent — the default |
| `true`, `"true"` | consent given |
| a function, or a string naming one on `window` | called whenever consent is consulted; the return value is coerced to a boolean |
| a string naming a non-function global | read whenever consent is consulted, so a variable the consent manager flips is picked up live |

A name that resolves to nothing, or a function that throws, fails closed to
no consent with a console warning. The value is consulted at every storage
decision, never cached at `init()`, so a consent manager that answers after
page load needs no extra call. When it flips to true, anything waiting in
the memory retry queue is written to localStorage and an identified
instance starts persisting a visitor id; when it flips to false, every key
this instance owns is deleted and records go back to memory. Events already
sent stay as they were sent.

`consent(true)` / `consent(false)` pins a value over whatever the tag
declared; `consent(null)` hands control back; `consent()` returns the
effective value. Passing the answer to `init()` (or naming a global that
holds it) is what lets the entry pageview carry the stored identity, which a
later call cannot do.

### Two tags on one page

`data-instance="et"` registers the tag at `window.et`, leaves
`window.twillingate` alone and prefixes its storage keys (`et_visitor`,
`et_user`, `et_user_name`, `et_group`, `et_group_name`, `et_queue`). It
works with or without `data-key`, so the tag can auto-init from its
attributes or load dormant for `et.init({...})` in code. Each instance
hooks `history.pushState` on its own and the patches chain, so both fire,
each into its own project.

```html
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_web…"></script>
<script defer src="https://twillingate.example.com/js/twillingate.js"
        data-key="ak_app…" data-instance="et" data-auto="off"></script>
```

`instance: "et"` in `init()` is the same name for a bundled consumer, which
has no tag: it prefixes storage keys and registers nothing, so two
npm-loaded instances stop sharing a visitor id and a queue. Three rules:

- the attribute wins — a tag that declared `data-instance` ignores an
  `instance` option that disagrees, with a warning;
- the name must match `^[a-z][a-z0-9_]{0,15}$`, the same shape as `$kind`;
  an invalid name is refused with a warning and the default kept;
- a global already holding something that is not a Twillingate instance is
  never overwritten (`data-instance="location"` costs a warning, not the
  page).

The default name is `twillingate`, which keeps the current global and the
current `twillingate_*` keys. `twillingate_ignore` stays global and
unprefixed: opting out is a decision about the person, not one tag.
````

Detection section: in the sentence listing what the SDK reads beyond detection, delete `` `navigator.webdriver`, ``.

Transport section, second paragraph → 

```
A batch that fails to send (network down, 5xx) is kept for retry — in
memory, or with [consent](#consent-and-storage) in a bounded localStorage
queue (`twillingate_queue`, or `<instance>_queue`, 50 batches) — and
replays when the browser fires `online`, once more through `sendBeacon` on
unload, and from storage on the next load. Replays dedupe server-side by
event id and keep their original timestamps. A 4xx response (bad key, bad
payload) drops the batch instead — resending it forever helps nobody.
```

Privacy behaviour section → 

```
### Privacy behaviour

- Nothing is written to the device unless the tag declares
  [consent](#consent-and-storage); anonymous projects never persist a
  visitor id even with it.
- Identified projects with consent: a visitor id persists in
  `twillingate_visitor` (or `<instance>_visitor`) together with the user and
  group.
- The SDK does not decide where analytics runs: a `localhost` page, a
  `file://` URL and an automated browser are tracked like any other, so
  keeping development traffic out of a project is the site's job — skip
  `init()` on a condition it knows.
- Opt a device out: `localStorage.twillingate_ignore = "true"`.
```

Then `grep -n "analytics_\|webdriver\|silent on localhost" docs/twillingate.md` must print nothing, and `grep -n "persist" docs/twillingate.md` must show no claim that the SDK persists without consent.

- [ ] **Step 3: `internal/api/guide.go`**

Replace the string `". The snippet is silent on localhost, so test on a real or staged domain.\n\n"` with `". Nothing is filtered on the client: a localhost page reports too, so keep development traffic out by not loading the tag there.\n\n"`.

Replace the IDENTIFIED string with:

```go
b.WriteString("This project is IDENTIFIED: the tag stores nothing on the device unless it\ndeclares consent (data-consent=\"true\", or the name of a global the site's\nconsent manager maintains); without it the visitor id is not persisted and\nsigned-out visitors fall back to the daily-rotating connection hash. Call\ntwillingate.identify(userId, userName) (and twillingate.group(groupId))\nafter login and twillingate.reset() on logout.\n\n")
```

Run: `/usr/local/go/bin/go test ./internal/api -run 'TestDocument|TestGuide|Guide' ` → PASS.

- [ ] **Step 4: Rebuild the bundle and run the server test**

Run: `cd sdk && npm run build && cd .. && /usr/local/go/bin/go test ./internal/server -run 'TestTwillingateSDKServed|TestLegacyScriptRemoved'`
Expected: PASS; `git status` shows `internal/server/twillingate.js` modified.

- [ ] **Step 5: Spec status**

In the spec: `Status: implemented (2026-09-22)`, and under `## Testing` add one paragraph:

```
Implementation note (2026-09-22): jsdom is configured to serve
`https://example.com` (sdk/vitest.config.ts) so `$host` assertions stay
readable; the guard's removal is covered by the `navigator.webdriver` test
and by the deleted code. Snippet bootstrap moved out of `entry.ts` into an
exported `bootstrap()` so the `data-instance` registration rules could be
tested. On the grant transition only the retry queue is written and the
visitor id starts persisting; a user set by `identify()` before consent is
persisted on the next `identify()`/`group()` call, not retroactively.
```

- [ ] **Step 6: Full check**

Run: `cd sdk && npm test && npm run typecheck && cd .. && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/go test ./internal/api ./internal/server`
Expected: green. (`make check` is the controller's job before the PR; ≈15 min.)

- [ ] **Step 7: Commit** (controller)

```bash
git add docs internal/api internal/server sdk
git commit -m "docs(sdk): document consent, named instances and the null-drops-an-attribute rule"
```

---

## Self-review

**Spec coverage.** §1 consent: default false (T2 field default), accepted values + fail-closed (T1), lazy consult + transitions (T2 `mayStore`), pin/null/read (T2 `consent()`), memory retry with `online` and `pagehide` (T2 `replay`/`flush`), cap 50 (T2 `store`), anonymous never persists visitor id (T2 `visitorId`), `reset()` clears the queue (T2), `identity` only gates persistence under consent (T2 `saveIdentity`), environment guard removed (T2 `ignored`), legacy keys removed (T2), breaking-change commit (`feat(sdk)!:`). §2 instances: attribute registers + prefixes (T3 `bootstrap`), option prefixes only (T3 `init`), attribute wins (T3), validation (T3 `instanceName`), foreign global (T3), guard reads `window[name]` (T3), prefixed keys + unprefixed ignore (T2 `keysFor`/`IGNORE`), chained pushState (existing code, T3 test). §3 null drop (T4). §4/§4.1/§5 are Econumo wiring and a hand-run SQL cleanup: no code. SDK surface block: matches after T2/T3 (`consent`, `instance` flat options; `consent` method; `bootstrap` is an extra export beside `supersededBy`/`autoInit`). Testing list: every bullet maps to a test in T1–T4 or a deletion in T2; docs bullets to T5.

**Placeholders.** None: every code step carries its code.

**Type consistency.** `ConsentSpec` (T1) is the type of `InitOptions.consent` (T2) and what `autoInit` passes (a string). `keysFor`/`Keys`/`DEFAULT_INSTANCE`/`SUFFIXES` (T2) are what `useInstance` and `wipe` use (T3/T2). `mayStore()` is private and consulted in `init`, `identify`/`group` via `saveIdentity`, `reset`, `visitorId`, `saveQueue`, `replay`, `consent`. `bootstrap` returns `Twillingate | null` and `instances.test.ts` uses `!` accordingly. `supersededBy`'s third parameter defaults so the existing tests in `twillingate.test.ts` still compile.
