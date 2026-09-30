// Web Vitals: opt-in per instance, sampled once per page load, reported by
// the second bundle (stubbed here as window.twillingateVitals so no script
// loads) and sent as measures carrying the page load's location.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, type InitOptions } from "./twillingate";
import { tagOptions } from "./factory";
import { runtime } from "./runtime";
import { resetVitalsLoader, type VitalsReport } from "./vitals-loader";
import { storedEvents } from "./test-helpers";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

const VITALS_SRC = "https://collector.example.com/js/twillingate-vitals.js";

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];
let reporters: VitalsReport[];
let random: ReturnType<typeof vi.spyOn>;

function tg(opts: Partial<InitOptions> = {}, name?: string): Twillingate {
  const t = new Twillingate(name);
  t.init({ key: "ak_test", flushInterval: 0, ...opts });
  return t;
}

function report(name: "LCP" | "INP" | "CLS" | "FCP" | "TTFB", value: number): void {
  for (const r of reporters) r({ name, value });
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
}

// Every stored measure, with the batch's key, the event's top-level fields
// and the attributes the collector would store.
interface Measure {
  key: string;
  family: unknown;
  name: unknown;
  value: unknown;
  measure: unknown;
  attributes: Record<string, unknown>;
}

function measures(batches: Sent[] = sent): Measure[] {
  return batches.flatMap((b) => {
    const stored = storedEvents(b);
    return b.body.events
      .map((e, i) => ({ key: b.body.key, family: e.family, name: e.name, value: e.value, measure: e.measure, attributes: stored[i] }))
      .filter((e) => e.family === "measures");
  });
}

function vitalsScripts(): HTMLScriptElement[] {
  return Array.from(document.querySelectorAll("script")).filter((s) => s.src.includes("twillingate-vitals.js"));
}

function scriptTag(attrs: Record<string, string>): HTMLScriptElement {
  const s = document.createElement("script");
  for (const [k, v] of Object.entries(attrs)) s.setAttribute(k, v);
  return s;
}

beforeEach(() => {
  runtime.reset();
  resetVitalsLoader();
  vi.useFakeTimers();
  sent = [];
  reporters = [];
  vi.stubGlobal("fetch", (_url: string, init: { body: string }) => {
    sent.push({ body: JSON.parse(init.body) });
    return Promise.resolve({ status: 202 });
  });
  window.twillingateVitals = (cb) => {
    reporters.push(cb);
  };
  random = vi.spyOn(Math, "random").mockReturnValue(0.5);
  localStorage.clear();
  document.head.innerHTML = "";
  history.replaceState(null, "", "/start");
});

afterEach(() => {
  delete window.twillingateVitals;
  random.mockRestore();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Web Vitals", () => {
  it("sends each reported vital as a measure with the rate and the page load's location", async () => {
    const t = tg({ vitals: 1 });
    history.pushState(null, "", "/later");
    t.page();
    report("LCP", 1234.5);
    report("CLS", 0.05);
    t.flush();
    await drain();
    const got = measures();
    expect(got).toHaveLength(2);
    expect(got[0]).toMatchObject({ family: "measures", name: "$lcp", value: 1234.5, measure: "time" });
    expect(got[1]).toMatchObject({ family: "measures", name: "$cls", value: 0.05, measure: "number" });
    for (const m of got) {
      expect(m.attributes).toMatchObject({ $sample_rate: 1, $host: "example.com", $path: "/start" });
    }
  });

  it("samples once per page load: a draw above the rate sends nothing", async () => {
    random.mockReturnValue(0.5);
    const t = tg({ vitals: 0.2 });
    for (const n of ["LCP", "INP", "CLS", "FCP", "TTFB"] as const) report(n, 10);
    t.flush();
    await drain();
    expect(reporters).toHaveLength(0);
    expect(measures()).toHaveLength(0);
    expect(vitalsScripts()).toHaveLength(0);
  });

  it("samples once per page load: a draw below the rate sends every vital with the rate", async () => {
    random.mockReturnValue(0.1);
    const t = tg({ vitals: 0.2 });
    report("LCP", 2000);
    report("INP", 120);
    report("CLS", 0.1);
    report("FCP", 800);
    report("TTFB", 90);
    t.flush();
    await drain();
    const got = measures();
    expect(got.map((m) => [m.name, m.measure])).toEqual([
      ["$lcp", "time"], ["$inp", "time"], ["$cls", "number"], ["$fcp", "time"], ["$ttfb", "time"],
    ]);
    for (const m of got) expect(m.attributes.$sample_rate).toBe(0.2);
  });

  it("does nothing without the vitals option", async () => {
    const spy = vi.fn();
    window.twillingateVitals = spy;
    const t = tg();
    t.flush();
    await drain();
    expect(spy).not.toHaveBeenCalled();
    expect(vitalsScripts()).toHaveLength(0);
    expect(measures()).toHaveLength(0);
  });

  it("loads the vitals script once for every instance that enables it", async () => {
    delete window.twillingateVitals;
    const a = tg({ key: "ak_a", vitals: 1 }, "a");
    const b = tg({ key: "ak_b", vitals: 1 }, "b");
    const scripts = vitalsScripts();
    expect(scripts).toHaveLength(1);
    expect(scripts[0].src).toBe(VITALS_SRC);

    // The bundle arrives: both instances subscribe to it.
    window.twillingateVitals = (cb) => {
      reporters.push(cb);
    };
    window.dispatchEvent(new Event("twillingate-vitals"));
    expect(reporters).toHaveLength(2);
    report("FCP", 500);
    a.flush();
    b.flush();
    await drain();
    expect(measures().map((m) => m.key).sort()).toEqual(["ak_a", "ak_b"]);
  });

  it("sends a vital reported while the page is hidden at once, through sendBeacon", async () => {
    tg({ vitals: 1, flushInterval: 60000 });
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { ...navigator, sendBeacon: beacon });
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    report("LCP", 1500);
    expect(beacon).toHaveBeenCalledOnce();
    const body = JSON.parse(beacon.mock.calls[0][1]);
    expect(body.events.map((e: { name: string }) => e.name)).toContain("$lcp");
    await drain();
    expect(measures()).toHaveLength(0); // nothing left for the timer to send
  });

  it("keeps each instance's own rate: 0 is off", async () => {
    const a = tg({ key: "ak_a", vitals: 1 }, "a");
    const b = tg({ key: "ak_b", vitals: 0 }, "b");
    expect(reporters).toHaveLength(1);
    report("LCP", 1000);
    a.flush();
    b.flush();
    await drain();
    expect(measures().map((m) => m.key)).toEqual(["ak_a"]);
  });

  it("sends nothing once the visitor opts out", async () => {
    const t = tg({ vitals: 1 });
    t.optOut(true);
    report("LCP", 1000);
    t.flush();
    await drain();
    expect(measures()).toHaveLength(0);
  });

  it("drops a report with an unknown name or an unusable value", async () => {
    const t = tg({ vitals: 1 });
    for (const r of reporters) {
      r({ name: "FID", value: 10 } as unknown as Parameters<VitalsReport>[0]);
      r({ name: "constructor", value: 10 } as unknown as Parameters<VitalsReport>[0]);
      r({ name: "LCP", value: -1 });
      r({ name: "LCP", value: NaN });
    }
    t.flush();
    await drain();
    expect(measures()).toHaveLength(0);
  });
});

describe("data-vitals", () => {
  it.each([
    ["1", 1],
    ["0.25", 0.25],
    ["0", undefined],
    ["2", undefined],
    ["x", undefined],
    ["", undefined],
  ])("reads %j as %j", (raw, want) => {
    expect(tagOptions(scriptTag({ "data-key": "ak_test", "data-vitals": raw }))?.vitals).toBe(want);
  });

  it("is off when absent", () => {
    expect(tagOptions(scriptTag({ "data-key": "ak_test" }))?.vitals).toBeUndefined();
  });
});
