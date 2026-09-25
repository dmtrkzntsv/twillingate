// Location on product events, the autoAttributes off switch, and null
// families: a $x: null drops $x and every reserved $x_* key.
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
});

function only(): { batch: Record<string, unknown>; event: Record<string, unknown> } {
  expect(sent).toHaveLength(1);
  return { batch: sent[0].body.attributes, event: sent[0].body.events[0].attributes as Record<string, unknown> };
}

describe("location on product events", () => {
  it("track() carries the page's $host and $path on the web kind", async () => {
    history.replaceState(null, "", "/pricing?utm_source=hn");
    tg().track("signup");
    await drain();
    const { event } = only();
    expect(event.$host).toBe(location.hostname);
    expect(event.$path).toBe("/pricing");
    expect(event).not.toHaveProperty("$utm_source");
    expect(event).not.toHaveProperty("$referrer");
  });

  it("track() carries $screen on an app kind", async () => {
    history.replaceState(null, "", "/settings");
    tg({ kind: "app" }).track("export");
    await drain();
    const { event } = only();
    expect(event.$screen).toBe("/settings");
    expect(event).not.toHaveProperty("$host");
  });

  it("a mask that throws costs the event its location, not the event", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    tg({ maskUrl: () => { throw new Error("boom"); } }).track("signup", { plan: "pro" });
    await drain();
    const { event } = only();
    expect(event.plan).toBe("pro");
    expect(event).not.toHaveProperty("$path");
    expect(warn).toHaveBeenCalled();
  });

  it("the call's own $path wins over the derived one", async () => {
    history.replaceState(null, "", "/a");
    tg().track("signup", { $path: "/b" });
    await drain();
    expect(only().event.$path).toBe("/b");
  });
});

describe("autoAttributes: false", () => {
  it("sends nothing derived but keeps what a view needs, the flags and what the site set", async () => {
    history.replaceState(null, "", "/x?utm_source=hn");
    const t = tg({ autoAttributes: false, appVersion: "2.4.1", appLocale: "de" });
    t.attrs({ tier: "beta" });
    t.page();
    t.track("signup");
    await drain();
    const batch = sent[0].body.attributes;
    for (const k of ["$os", "$os_version", "$os_name", "$browser", "$browser_version", "$device", "$browser_locale"]) {
      expect(batch).not.toHaveProperty(k);
    }
    expect(batch).toMatchObject({ $kind: "web", $platform: "web", $app_version: "2.4.1", $app_locale: "de" });
    expect(batch).toHaveProperty("$consent");
    const [view, event] = sent[0].body.events.map((e) => e.attributes as Record<string, unknown>);
    expect(view).toMatchObject({ $path: "/x", tier: "beta" });
    for (const k of ["$referrer", "$utm_source", "$display_width"]) expect(view).not.toHaveProperty(k);
    expect(event).toEqual({ tier: "beta" });
  });
});

describe("null drops a key wherever it came from", () => {
  it("attrs({ $browser: null }) drops the whole $browser family from the batch", async () => {
    const t = tg();
    t.attrs({ $browser: null });
    t.track("signup");
    await drain();
    const { batch, event } = only();
    for (const k of ["$browser", "$browser_version", "$browser_locale"]) expect(batch).not.toHaveProperty(k);
    expect(batch).toHaveProperty("$os");
    expect(event.$browser).toBeNull();
    expect(event.$browser_locale).toBeNull();
  });

  it("a per-call $os: null sends null on that event only", async () => {
    const t = tg();
    t.track("a", { $os: null });
    t.track("b");
    await drain();
    const [a, b] = sent[0].body.events.map((e) => e.attributes as Record<string, unknown>);
    expect(a.$os).toBeNull();
    expect(a.$os_version === undefined || a.$os_version === null).toBe(true);
    expect(b).not.toHaveProperty("$os");
    expect(sent[0].body.attributes).toHaveProperty("$os");
  });

  it("a per-call $utm: null drops all three campaign keys from a view", async () => {
    history.replaceState(null, "", "/landing?utm_source=hn&utm_medium=social&utm_campaign=launch");
    tg().page({ $utm: null });
    await drain();
    const { event } = only();
    for (const k of ["$utm_source", "$utm_medium", "$utm_campaign"]) expect(event).not.toHaveProperty(k);
  });

  it("a later explicit value beats an earlier family null", async () => {
    const t = tg();
    t.attrs({ $utm: null });
    t.track("signup", { $utm_source: "mail" });
    await drain();
    expect(only().event.$utm_source).toBe("mail");
  });

  it("a null on a custom key never expands", async () => {
    const t = tg();
    t.attrs({ plan: null, plan_tier: "gold" });
    t.track("signup");
    await drain();
    expect(only().event).toEqual(expect.objectContaining({ plan_tier: "gold" }));
  });
});
