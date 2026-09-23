// Core SDK behaviour: init modes, payload shape, batching, transport and
// failure handling. Identity and pageview behaviour live in identity.test.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, type InitOptions } from "./twillingate";
import { resetPlatformVersion } from "./detect";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

const URL_BASE = "https://collector.example.com";

interface Sent {
  url: string;
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];
let fetchImpl: (url: string, init: { body: string }) => Promise<{ status: number }>;

function okFetch(url: string, init: { body: string }): Promise<{ status: number }> {
  sent.push({ url: String(url), body: JSON.parse(init.body) });
  return Promise.resolve({ status: 202 });
}

function tg(opts: Partial<InitOptions> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
  return t;
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  // let fetch promise callbacks (store-on-failure) settle
  await vi.waitFor(() => {});
}

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

describe("init", () => {
  it("warns and stays dormant without a key", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    t.init({ key: "" });
    t.track("x");
    expect(warn).toHaveBeenCalled();
    expect(sent).toHaveLength(0);
  });

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
});

describe("payload shape", () => {
  it("posts the documented envelope to /ingest/events", async () => {
    const t = tg();
    t.track("signup", { plan: "pro" });
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(URL_BASE + "/ingest/events");
    const { body } = sent[0];
    expect(body.key).toBe("ak_test");
    expect(body.events).toHaveLength(1);
    const ev = body.events[0];
    expect(ev.name).toBe("signup");
    expect(ev.attributes).toEqual({ plan: "pro" });
    expect(String(ev.id)).toMatch(/^[0-9a-f-]{36}$/);
    expect(() => new Date(String(ev.ts)).toISOString()).not.toThrow();
  });

  it("carries app context as batch attributes", async () => {
    const t = tg({ identity: "identified", kind: "app", platform: "ios", appVersion: "2.4.1" });
    t.installId("018f-install");
    t.screen("/settings");
    await drain();
    const { attributes, events } = sent[0].body;
    expect(attributes).toMatchObject({
      $kind: "app",
      $platform: "ios",
      $app_version: "2.4.1",
      $install_id: "018f-install",
    });
    expect(typeof attributes.$os).toBe("string");
    expect(events[0].name).toBe("$screen_view");
    expect(events[0].attributes).toEqual({ $screen: "/settings" });
  });

  it("screen() merges extra attributes and requires a name", async () => {
    const t = tg();
    t.screen("", { a: 1 });
    t.screen("/home", { a: 1 });
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events[0].attributes).toEqual({ $screen: "/home", a: 1 });
  });

  it("stamps display size on views and locale on every batch", async () => {
    Object.defineProperty(window, "screen", { value: { width: 1920, height: 1080 }, configurable: true });
    Object.defineProperty(navigator, "language", { value: "de-DE", configurable: true });
    const t = tg();
    t.page("/x");
    t.track("probe");
    t.flush();
    await drain();
    const [view, probe] = sent[0].body.events;
    const va = view.attributes as Record<string, unknown>;
    expect(va.$display_width).toBe(1920);
    expect(va.$display_height).toBe(1080);
    expect((probe.attributes as Record<string, unknown>).$display_width).toBeUndefined();
    expect(sent[0].body.attributes.$locale).toBe("de-DE");
  });
});

describe("environment", () => {
  const CHROME_WIN = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36";

  // Replace the whole navigator: the batch reads language, send reads sendBeacon, detection reads the rest.
  function stubNavigator(extra: Record<string, unknown>): void {
    vi.stubGlobal("navigator", { language: "en-US", userAgent: CHROME_WIN, platform: "Win32", maxTouchPoints: 0, ...extra });
  }

  afterEach(() => resetPlatformVersion());

  it("sends the detected os, browser and device on every batch", async () => {
    stubNavigator({});
    const t = tg();
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({
      $platform: "web", $os: "windows", $os_version: "10", $os_name: "Windows 10",
      $browser: "chrome", $browser_version: "126", $device: "desktop",
    });
  });

  it("defaults $platform to web only while kind is web", async () => {
    stubNavigator({});
    const web = tg();
    web.track("a");
    await drain();
    expect(sent[0].body.attributes.$platform).toBe("web");

    sent = [];
    const app = tg({ kind: "app" });
    app.track("b");
    await drain();
    expect(sent[0].body.attributes).not.toHaveProperty("$platform");
    expect(sent[0].body.attributes.$os).toBe("windows"); // detection still runs on any kind

    sent = [];
    const electron = tg({ kind: "app", platform: "electron" });
    electron.track("c");
    await drain();
    expect(sent[0].body.attributes.$platform).toBe("electron");
  });

  it("consults only a supplied ClientSignals, never the ambient navigator", () => {
    stubNavigator({});
    const t = tg();
    expect(t.detectOS({ userAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0" }).os).toBe("linux");
    expect(t.detectOS({}).os).toBe("unknown");
  });

  it("carries the high-entropy platformVersion once it has resolved", async () => {
    stubNavigator({
      userAgentData: {
        brands: [{ brand: "Google Chrome", version: "126.0.0.0" }, { brand: "Chromium", version: "126.0.0.0" }],
        platform: "Windows", mobile: false,
        getHighEntropyValues: () => Promise.resolve({ platformVersion: "15.0.0" }),
      },
    });
    const t = tg({ flushInterval: 50 });
    // Immediately after init the promise has not settled: the User-Agent answer stands.
    expect(t.detectOS().osVersion).toBe("10");
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $os: "windows", $os_version: "11", $os_name: "Windows 11", $browser: "chrome" });
    expect(t.detectOS().osVersion).toBe("11");
  });

  it("sends unknown values honestly when there is no browser to read", async () => {
    // A runtime with no User-Agent at all — the case a bundled SDK meets
    // in a worker or a test harness — must say unknown, not other.
    stubNavigator({ userAgent: "", platform: undefined });
    const t = tg();
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $os: "unknown", $browser: "unknown", $device: "unknown" });
    expect(sent[0].body.attributes).not.toHaveProperty("$os_version");
    expect(sent[0].body.attributes).not.toHaveProperty("$os_name");
  });
});

describe("batching", () => {
  it("coalesces events queued within the flush window into one POST", async () => {
    const t = tg({ flushInterval: 50 });
    t.track("a");
    t.track("b");
    t.track("c");
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events.map((e) => e.name)).toEqual(["a", "b", "c"]);
  });

  it("flushes immediately once 20 events queue up", () => {
    const t = tg({ flushInterval: 60_000 });
    for (let i = 0; i < 20; i++) t.track(`e${i}`);
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events).toHaveLength(20);
  });

  it("flush() sends the queue without waiting for the timer", () => {
    const t = tg({ flushInterval: 60_000 });
    t.track("a");
    expect(sent).toHaveLength(0);
    t.flush();
    expect(sent).toHaveLength(1);
  });

  it("uses sendBeacon when the page unloads", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { ...navigator, sendBeacon: beacon });
    const t = tg({ flushInterval: 60_000 });
    t.track("bye");
    window.dispatchEvent(new Event("pagehide"));
    expect(beacon).toHaveBeenCalledOnce();
    const [url, body] = beacon.mock.calls[0];
    expect(url).toBe(URL_BASE + "/ingest/events");
    expect(JSON.parse(body).events[0].name).toBe("bye");
    expect(sent).toHaveLength(0); // beacon took it, fetch did not
  });

  it("drains on visibilitychange to hidden", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { ...navigator, sendBeacon: beacon });
    const t = tg({ flushInterval: 60_000 });
    t.track("away");
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    expect(beacon).toHaveBeenCalledOnce();
  });
});

describe("failure handling and the offline queue", () => {
  it("persists the batch when fetch rejects, and replays it on `online`", async () => {
    fetchImpl = () => Promise.reject(new TypeError("network down"));
    const t = tg({ consent: true });
    t.track("offline-event");
    await drain();
    expect(sent).toHaveLength(0);
    const stored = JSON.parse(localStorage.getItem("twillingate_queue")!);
    expect(stored).toHaveLength(1);
    expect(stored[0].events[0].name).toBe("offline-event");
    const originalId = stored[0].events[0].id;

    fetchImpl = okFetch;
    window.dispatchEvent(new Event("online"));
    await drain();
    expect(sent).toHaveLength(1);
    // replay resends the stored batch verbatim: same id, so the server dedupes
    expect(sent[0].body.events[0].id).toBe(originalId);
    expect(localStorage.getItem("twillingate_queue")).toBeNull();
  });

  it("replays a stored queue from a previous session on init", async () => {
    localStorage.setItem(
      "twillingate_queue",
      JSON.stringify([{ key: "ak_test", attributes: {}, events: [{ id: "x", ts: "t", name: "old", attributes: {} }] }]),
    );
    tg({ consent: true });
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events[0].name).toBe("old");
  });

  it("keeps a batch that fails on 5xx and drops one rejected with 4xx", async () => {
    fetchImpl = (url, init) => {
      sent.push({ url: String(url), body: JSON.parse(init.body) });
      return Promise.resolve({ status: 503 });
    };
    const t = tg({ consent: true });
    t.track("transient");
    await drain();
    expect(JSON.parse(localStorage.getItem("twillingate_queue")!)).toHaveLength(1);

    localStorage.removeItem("twillingate_queue");
    fetchImpl = (url, init) => {
      sent.push({ url: String(url), body: JSON.parse(init.body) });
      return Promise.resolve({ status: 401 });
    };
    t.track("permanent");
    await drain();
    expect(localStorage.getItem("twillingate_queue")).toBeNull();
  });

  it("bounds the offline queue at 50 batches, oldest dropped first", async () => {
    fetchImpl = () => Promise.reject(new TypeError("down"));
    const t = tg({ consent: true });
    for (let i = 0; i < 55; i++) {
      t.track(`e${i}`);
      t.flush();
    }
    await drain();
    const stored = JSON.parse(localStorage.getItem("twillingate_queue")!);
    expect(stored).toHaveLength(50);
    expect(stored[0].events[0].name).toBe("e5");
    expect(stored[49].events[0].name).toBe("e54");
  });

  it("survives a corrupt stored queue", async () => {
    localStorage.setItem("twillingate_queue", "{not json");
    const t = tg({ consent: true });
    t.track("fine");
    await drain();
    expect(sent).toHaveLength(1);
  });

  it("survives a stored queue holding unbatch-shaped elements", async () => {
    localStorage.setItem("twillingate_queue", JSON.stringify([null, {}, { events: [] }]));
    const t = tg({ consent: true });
    t.track("fine");
    await drain();
    expect(sent).toHaveLength(1);
  });
});
