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
