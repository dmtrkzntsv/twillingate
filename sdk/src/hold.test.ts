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
