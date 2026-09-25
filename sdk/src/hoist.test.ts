// Attributes every event in a batch shares go out once, at the batch level.
// The collector lays batch attributes under each event's own, so what it
// stores is unchanged; only the body shrinks. Display size is a batch
// attribute of its own, like $os.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, hoistShared, type InitOptions } from "./twillingate";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<{ name: string; attributes: Record<string, unknown> }> };
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

/** What the collector stores for event i: batch attributes under the event's own. */
function stored(i: number): Record<string, unknown> {
  const merged: Record<string, unknown> = { ...sent[0].body.attributes, ...sent[0].body.events[i].attributes };
  for (const k of Object.keys(merged)) if (merged[k] === null) delete merged[k];
  return merged;
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

describe("hoistShared", () => {
  const ev = (attributes: Record<string, unknown>) => ({ id: "i", ts: "t", name: "n", attributes });

  it("moves a key every event carries with the same value, and only that key", () => {
    const out = hoistShared({
      key: "k",
      attributes: { $os: "macos" },
      events: [ev({ plan: "pro", $host: "a.com", $path: "/a" }), ev({ plan: "pro", $host: "a.com", $path: "/b" })],
    });
    expect(out.attributes).toEqual({ $os: "macos", plan: "pro", $host: "a.com" });
    expect(out.events.map((e) => e.attributes)).toEqual([{ $path: "/a" }, { $path: "/b" }]);
  });

  it("leaves a key set on only some events, or with differing values", () => {
    const out = hoistShared({
      key: "k",
      attributes: {},
      events: [ev({ plan: "pro", tier: 1 }), ev({ plan: "free" }), ev({ plan: "pro", tier: 1 })],
    });
    expect(out.attributes).toEqual({});
    expect(out.events[0].attributes).toEqual({ plan: "pro", tier: 1 });
  });

  it("never moves a null: an event's null drops a batch value for that event only", () => {
    const out = hoistShared({
      key: "k",
      attributes: { $os: "macos" },
      events: [ev({ $os: null }), ev({ $os: null })],
    });
    expect(out.attributes).toEqual({ $os: "macos" });
    expect(out.events.map((e) => e.attributes)).toEqual([{ $os: null }, { $os: null }]);
  });

  it("a shared value replaces a different batch value, which every event overrode anyway", () => {
    const out = hoistShared({
      key: "k",
      attributes: { $app_locale: "de" },
      events: [ev({ $app_locale: "fr" }), ev({ $app_locale: "fr" })],
    });
    expect(out.attributes).toEqual({ $app_locale: "fr" });
    expect(out.events.map((e) => e.attributes)).toEqual([{}, {}]);
  });

  it("leaves a one-event batch alone: there is nothing to save", () => {
    const batch = { key: "k", attributes: { $os: "macos" }, events: [ev({ plan: "pro" })] };
    expect(hoistShared(batch)).toEqual(batch);
  });

  it("does not mutate its input", () => {
    const events = [ev({ plan: "pro" }), ev({ plan: "pro" })];
    const batch = { key: "k", attributes: {}, events };
    hoistShared(batch);
    expect(events[0].attributes).toEqual({ plan: "pro" });
    expect(batch.attributes).toEqual({});
  });
});

describe("on the wire", () => {
  it("sends attrs() defaults once per batch and keeps what the collector stores", async () => {
    const t = tg();
    t.attrs({ access_state: "active", accounts: 3 });
    t.track("signup", { plan: "pro" });
    t.track("export", { plan: "free" });
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.attributes).toMatchObject({ access_state: "active", accounts: 3 });
    for (const e of sent[0].body.events) {
      expect(e.attributes).not.toHaveProperty("access_state");
      expect(e.attributes).not.toHaveProperty("accounts");
    }
    expect(stored(0)).toMatchObject({ access_state: "active", accounts: 3, plan: "pro" });
    expect(stored(1)).toMatchObject({ access_state: "active", accounts: 3, plan: "free" });
  });
});

describe("display size is a batch attribute", () => {
  beforeEach(() => {
    Object.defineProperty(window, "screen", { value: { width: 1920, height: 1080 }, configurable: true });
  });

  it("is sent once in the batch, not on the view or the event", async () => {
    const t = tg();
    t.page("/x");
    await drain();
    t.track("probe");
    await drain();
    for (const s of sent) {
      expect(s.body.attributes).toMatchObject({ $display_width: 1920, $display_height: 1080 });
      for (const e of s.body.events) expect(e.attributes).not.toHaveProperty("$display_width");
    }
  });

  it("is not sent with autoAttributes: false", async () => {
    tg({ autoAttributes: false }).track("probe");
    await drain();
    expect(sent[0].body.attributes).not.toHaveProperty("$display_width");
    expect(sent[0].body.events[0].attributes).not.toHaveProperty("$display_width");
  });

  it("a $display: null drops it for that event on the wire", async () => {
    tg().track("probe", { $display: null });
    await drain();
    expect(sent[0].body.events[0].attributes).toMatchObject({ $display_width: null, $display_height: null });
  });
});
