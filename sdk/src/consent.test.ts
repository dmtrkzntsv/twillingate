// The consent resolver: data-consent / init({consent}) → a function the
// SDK consults at every storage decision. Storage semantics under consent
// are in the second describe block, added in Task 2.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resolveConsent } from "./consent";
import { Twillingate, type InitOptions } from "./twillingate";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

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

function tg(opts: Partial<InitOptions> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
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

const OWNED = ["visitor", "user", "user_name", "group", "group_name", "queue"].map((s) => `twillingate_${s}`);

describe("storage under consent", () => {
  beforeEach(() => {
    runtime.reset();
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

  it("without consent, identity still comes from identify() for the session", async () => {
    const t = new Twillingate();
    t.identify("u_1");
    t.group("org_1");
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, identity: "identified" });
    const attrs = await lastAttributes(t);
    expect(attrs).toMatchObject({ $user_id: "u_1", $group_id: "org_1" });
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

    // The beacon-accepted batch is retired from pending: a second pagehide
    // (visibilitychange to hidden fires flush(true) on every tab switch,
    // not only the final unload) must not re-beacon it.
    window.dispatchEvent(new Event("pagehide"));
    expect(beacon).toHaveBeenCalledOnce();

    // Grant consent: a further failure mirrors to storage. The beacon
    // retires the batch from the in-memory pending queue, but sendBeacon
    // returning true only means the browser accepted the payload, not that
    // it was delivered — offline it is dropped — so the stored copy must
    // survive.
    t.consent(true);
    fetchImpl = failFetch;
    t.track("third");
    await drain();
    window.dispatchEvent(new Event("pagehide"));
    expect(beacon).toHaveBeenCalledTimes(2);
    const stored = JSON.parse(localStorage.getItem("twillingate_queue")!);
    expect(stored).toHaveLength(1);
    expect(stored[0].events[0].name).toBe("third");

    // pending was retired by the first beacon, so a second pagehide must
    // not re-beacon it.
    window.dispatchEvent(new Event("pagehide"));
    expect(beacon).toHaveBeenCalledTimes(2);
  });

  it("replay merges the stored queue with pending when a write silently failed", async () => {
    // A store() write can fail without throwing (lsSet swallows quota and
    // partitioned-storage errors): the batch then lives only in pending.
    // Replay must not lose it just because storage came back empty.
    fetchImpl = failFetch;
    const t = tg({ consent: true });
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    t.track("unwritable");
    t.flush();
    await drain();
    setItem.mockRestore();
    expect(localStorage.getItem("twillingate_queue")).toBeNull(); // the write silently failed

    fetchImpl = okFetch;
    window.dispatchEvent(new Event("online"));
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events[0].name).toBe("unwritable");
  });

  it("bounds the memory retry queue at 50 batches, oldest dropped first", async () => {
    // A distinct key, filtered below: an earlier test's `t` also has an
    // `online` listener still attached to this shared jsdom window (it
    // never unregisters, by design -- see the retry test above, whose
    // instance intentionally keeps a failed batch in memory past the end
    // of that test) and would otherwise add its own replay to `sent`.
    fetchImpl = failFetch;
    const t = tg({ key: "ak_bounds" });
    for (let i = 0; i < 55; i++) {
      t.track(`e${i}`);
      t.flush();
    }
    await drain();
    fetchImpl = okFetch;
    window.dispatchEvent(new Event("online"));
    await drain();
    const mine = sent.filter((s) => s.body.key === "ak_bounds");
    expect(mine).toHaveLength(50);
    expect(mine[0].body.events[0].name).toBe("e5");
    expect(mine[49].body.events[0].name).toBe("e54");
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

  it("consent(true) persists a user and group the instance already holds", () => {
    const t = tg({ identity: "identified" });
    t.identify("u_pre", "Ada");
    t.group("org_pre");
    expect(localStorage.length).toBe(0);

    t.consent(true);
    expect(localStorage.getItem("twillingate_user")).toBe("u_pre");
    expect(localStorage.getItem("twillingate_user_name")).toBe("Ada");
    expect(localStorage.getItem("twillingate_group")).toBe("org_pre");
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
      // A distinct key: an earlier test's dead instance can still hold a
      // batch in memory (pending survives localStorage.clear()) and reacts
      // to the same `online` dispatch; filtering by key keeps this
      // assertion about this test's own instance only.
      const t = tg({ identity: "identified", consent, key: `ak_reset_${consent}` });
      t.track("stale");
      t.flush();
      await drain();
      t.reset();
      expect(localStorage.getItem("twillingate_queue"), String(consent)).toBeNull();
      fetchImpl = okFetch;
      window.dispatchEvent(new Event("online"));
      await drain();
      const mine = sent.filter((s) => s.body.key === `ak_reset_${consent}`);
      expect(mine, String(consent)).toHaveLength(0);
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
