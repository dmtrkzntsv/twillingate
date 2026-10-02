// measure(): a third family, validated like the server, and every event's
// explicit family (views, product, measures).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, type InitOptions } from "./twillingate";
import { runtime } from "./runtime";
import { storedEvents } from "./test-helpers";

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

function lastEvent(): Record<string, unknown> {
  const events = sent[sent.length - 1].body.events;
  return events[events.length - 1];
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
  history.replaceState(null, "", "/start");
  document.body.innerHTML = "";
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("measure()", () => {
  it("sends family, name, value and measure, plus attrs and the page location", async () => {
    const t = tg();
    t.measure("checkout_api", 340, "time", { endpoint: "/x" });
    t.flush();
    await drain();
    const ev = lastEvent();
    expect(ev).toMatchObject({ family: "measures", name: "checkout_api", value: 340, measure: "time" });
    const [stored] = storedEvents(sent[sent.length - 1]);
    expect(stored).toMatchObject({ endpoint: "/x", $path: "/start" });
  });

  it.each([
    ["a negative value", "q", -1, "time"],
    ["a NaN value", "q", NaN, "time"],
    ["an infinite value", "q", Infinity, "time"],
    ["a value above 1e15", "q", 1.0000001e15, "time"],
    ["a non-number value", "q", "3", "time"],
    ["an unknown kind", "q", 1, "seconds"],
    ["an empty name", "", 1, "time"],
  ] as const)("sends nothing for %s", async (_label, name, value, kind) => {
    const t = tg();
    t.measure(name, value as unknown as number, kind as unknown as "time");
    t.flush();
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("marks page() views, track() and tagged clicks as product, distinct from measures", async () => {
    document.body.innerHTML = '<button id="b" data-twillingate-event="clicked"></button>';
    const t = tg();
    t.page();
    t.track("signup");
    click("b");
    t.measure("checkout_api", 340, "time");
    t.flush();
    await drain();
    const events = sent.flatMap((s) => s.body.events);
    expect(events.map((e) => e.family)).toEqual(["views", "product", "product", "measures"]);
  });

  it("accepts 1e15, the server's upper bound", async () => {
    const t = tg();
    t.measure("big", 1e15, "size");
    t.flush();
    await drain();
    expect(lastEvent()).toMatchObject({ family: "measures", name: "big", value: 1e15 });
  });

  it.each(["$page_view", "$pageview", "$screen_view"])(
    "track(%j) sends nothing and says why in debug mode: views go through page() and screen()",
    async (name) => {
      const log = vi.spyOn(console, "log").mockImplementation(() => {});
      const t = tg({ debug: true });
      t.track(name, { plan: "pro" });
      t.flush();
      await drain();
      expect(sent).toHaveLength(0);
      expect(log.mock.calls.some((c) => String(c[0]).includes(`track("${name}") ignored`))).toBe(true);
    },
  );

  it("is held before init() and replayed after, like track()", async () => {
    const t = new Twillingate();
    t.measure("checkout_api", 340, "time");
    t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false });
    t.flush();
    await drain();
    expect(lastEvent()).toMatchObject({ family: "measures", name: "checkout_api", value: 340, measure: "time" });
  });

  it("sends nothing once opted out", async () => {
    const t = tg();
    t.optOut(true);
    t.measure("checkout_api", 340, "time");
    t.flush();
    await drain();
    expect(sent).toHaveLength(0);
  });
});
