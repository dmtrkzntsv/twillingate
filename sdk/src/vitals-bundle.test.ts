// The vitals bundle's own contract, with web-vitals mocked so each on*
// callback can be fired by hand: replay to late subscribers, fan-out to
// every subscriber, the metric id passed through, and one set of
// observers however many times the file loads.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { VitalsReport } from "./vitals-loader";

const wv = vi.hoisted(() => ({
  fire: {} as Record<string, (m: { name: string; value: number; id: string }) => void>,
  registrations: 0,
}));

vi.mock("web-vitals", () => {
  const on = (name: string) => (cb: (m: { name: string; value: number; id: string }) => void) => {
    wv.registrations++;
    wv.fire[name] = cb;
  };
  return { onLCP: on("LCP"), onINP: on("INP"), onCLS: on("CLS"), onFCP: on("FCP"), onTTFB: on("TTFB") };
});

function metric(name: string, value: number, id: string) {
  // The real Metric carries more (rating, delta, entries, …); the bundle
  // passes on only name, value and id.
  return { name, value, id, rating: "good", delta: value, entries: [], navigationType: "navigate" };
}

async function load(): Promise<void> {
  vi.resetModules();
  await import("./vitals-bundle");
}

beforeEach(() => {
  delete window.twillingateVitals;
  wv.fire = {};
  wv.registrations = 0;
});

afterEach(() => {
  delete window.twillingateVitals;
});

describe("vitals bundle", () => {
  it("defines the global, observes all five metrics and announces itself", async () => {
    const ready = vi.fn();
    window.addEventListener("twillingate-vitals", ready, { once: true });
    await load();
    expect(typeof window.twillingateVitals).toBe("function");
    expect(Object.keys(wv.fire).sort()).toEqual(["CLS", "FCP", "INP", "LCP", "TTFB"]);
    expect(wv.registrations).toBe(5);
    expect(ready).toHaveBeenCalledOnce();
  });

  it("replays a metric that fired before a subscriber arrived, once", async () => {
    await load();
    wv.fire.FCP(metric("FCP", 812, "v6-fcp"));
    const late = vi.fn<VitalsReport>();
    window.twillingateVitals!(late);
    expect(late).toHaveBeenCalledOnce();
    expect(late).toHaveBeenCalledWith({ name: "FCP", value: 812, id: "v6-fcp" });
  });

  it("hands a live metric to every subscriber once, id included", async () => {
    await load();
    const a = vi.fn<VitalsReport>();
    const b = vi.fn<VitalsReport>();
    window.twillingateVitals!(a);
    window.twillingateVitals!(b);
    wv.fire.LCP(metric("LCP", 1234.5, "v6-lcp"));
    for (const s of [a, b]) {
      expect(s).toHaveBeenCalledOnce();
      expect(s).toHaveBeenCalledWith({ name: "LCP", value: 1234.5, id: "v6-lcp" });
    }
  });

  it("leaves the first copy in place when the file loads twice", async () => {
    await load();
    const first = window.twillingateVitals;
    const s = vi.fn<VitalsReport>();
    first!(s);
    await load();
    expect(wv.registrations).toBe(5);
    expect(window.twillingateVitals).toBe(first);
    wv.fire.TTFB(metric("TTFB", 90, "v6-ttfb"));
    expect(s).toHaveBeenCalledOnce();
  });
});
