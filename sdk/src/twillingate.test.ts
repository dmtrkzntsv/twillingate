// Core SDK behaviour: init modes, payload shape, batching, transport and
// failure handling. Identity and pageview behaviour live in identity.test.ts.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, autoInit, supersededBy } from "./twillingate";
import { resetPlatformVersion } from "./detect";
import twillingateSource from "./twillingate.ts?raw";

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

function tg(opts: Partial<Parameters<Twillingate["init"]>[0]> = {}): Twillingate {
  const t = new Twillingate();
  t.init({ key: "ak_test", url: URL_BASE, flushInterval: 0, ...opts });
  return t;
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  // let fetch promise callbacks (store-on-failure) settle
  await vi.waitFor(() => {});
}

// hookHistory() monkey-patches the shared history object with no unhook;
// restoring it after each test stops an earlier test's Twillingate instance
// from firing a ghost pageview when a later test calls pushState directly.
const nativePushState = history.pushState;

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
  history.pushState = nativePushState;
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

  it("warns when no url is available outside snippet mode", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    t.init({ key: "ak_test" }); // no url, no loading <script>
    t.track("x");
    expect(warn.mock.calls.flat().join(" ")).toContain("url");
    expect(sent).toHaveLength(0);
  });

  it("tracks nothing before init", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    new Twillingate().track("x");
    expect(warn).toHaveBeenCalled();
    expect(sent).toHaveLength(0);
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
    const t = tg({ kind: "app", platform: "ios", os: "ios", osVersion: "17.2", appVersion: "2.4.1", installId: "018f-install" });
    t.screen("/settings");
    await drain();
    const { attributes, events } = sent[0].body;
    expect(attributes).toMatchObject({
      $kind: "app",
      $platform: "ios",
      $os: "ios",
      $os_version: "17.2",
      $app_version: "2.4.1",
      $install_id: "018f-install",
    });
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

  // Replace the whole navigator: the ignore rules read webdriver, the
  // batch reads language, send reads sendBeacon, detection reads the rest.
  function stubNavigator(extra: Record<string, unknown>): void {
    vi.stubGlobal("navigator", { language: "en-US", webdriver: false, userAgent: CHROME_WIN, platform: "Win32", maxTouchPoints: 0, ...extra });
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

  it("lets an explicit option beat detection, while detect* still answers for the signals", async () => {
    stubNavigator({});
    const t = tg({ os: "linux", browser: "firefox", device: "tablet" });
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $os: "linux", $browser: "firefox", $device: "tablet" });
    // Pure detection ignores the option: it has to answer for THIS
    // User-Agent, or it is useless for the debugging case it exists for.
    expect(t.detectOS().os).toBe("windows");
    expect(t.detectBrowser().browser).toBe("chrome");
    expect(t.detectDevice().device).toBe("desktop");
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
    vi.stubGlobal("navigator", { ...navigator, sendBeacon: beacon, webdriver: false });
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
    vi.stubGlobal("navigator", { ...navigator, sendBeacon: beacon, webdriver: false });
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
    const t = tg();
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
    tg();
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events[0].name).toBe("old");
  });

  it("keeps a batch that fails on 5xx and drops one rejected with 4xx", async () => {
    fetchImpl = (url, init) => {
      sent.push({ url: String(url), body: JSON.parse(init.body) });
      return Promise.resolve({ status: 503 });
    };
    const t = tg();
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
    const t = tg();
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
    const t = tg();
    t.track("fine");
    await drain();
    expect(sent).toHaveLength(1);
  });
});

describe("ignore rules", () => {
  it("honours twillingate_ignore and the legacy analytics_ignore", async () => {
    const t = tg();
    localStorage.setItem("twillingate_ignore", "true");
    t.track("nope");
    localStorage.removeItem("twillingate_ignore");
    localStorage.setItem("analytics_ignore", "true");
    t.track("nope2");
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("stays silent under automated browsers", async () => {
    vi.stubGlobal("navigator", { ...navigator, webdriver: true });
    const t = tg();
    t.track("robot");
    await drain();
    expect(sent).toHaveLength(0);
  });
});

describe("snippet auto-init", () => {
  function scriptTag(attrs: Record<string, string>): HTMLScriptElement {
    const s = document.createElement("script");
    s.src = URL_BASE + "/js/twillingate.js";
    for (const [k, v] of Object.entries(attrs)) s.setAttribute(k, v);
    return s;
  }

  it("inits from data attributes and fires an automatic pageview", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet" }));
    t.flush();
    await drain();
    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(URL_BASE + "/ingest/events");
    expect(sent[0].body.key).toBe("ak_snippet");
    expect(sent[0].body.events[0].name).toBe("$page_view");
    const attrs = sent[0].body.events[0].attributes as Record<string, unknown>;
    expect(attrs.$host).toBe("example.com");
  });

  it("data-auto=off suppresses automatic pageviews", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet", "data-auto": "off" }));
    await drain();
    expect(sent).toHaveLength(0);
  });

  describe("loaded twice", () => {
    function loaded(key: string): Twillingate {
      const t = new Twillingate();
      autoInit(t, scriptTag({ "data-key": key }));
      return t;
    }

    it("defers to the first copy when the second tag has the same key", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      expect(supersededBy(loaded("ak_same"), scriptTag({ "data-key": "ak_same" }))).toBe(true);
      expect(warn.mock.calls.flat().join(" ")).toContain("loaded twice");
    });

    it("defers to the first copy when the second tag has no key", () => {
      vi.spyOn(console, "warn").mockImplementation(() => {});
      expect(supersededBy(loaded("ak_first"), scriptTag({}))).toBe(true);
      expect(supersededBy(new Twillingate(), scriptTag({}))).toBe(true);
    });

    it("lets a tag with a different key take over, with a warning", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      expect(supersededBy(loaded("ak_a"), scriptTag({ "data-key": "ak_b" }))).toBe(false);
      expect(warn.mock.calls.flat().join(" ")).toContain("ak_a -> ak_b");
    });

    it("lets a keyed tag replace a dormant copy silently", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      expect(supersededBy(new Twillingate(), scriptTag({ "data-key": "ak_b" }))).toBe(false);
      expect(warn).not.toHaveBeenCalled();
    });

    it("installs normally when nothing twillingate-shaped is on the page", () => {
      expect(supersededBy(undefined, scriptTag({ "data-key": "ak_b" }))).toBe(false);
      expect(supersededBy(document.createElement("div"), scriptTag({}))).toBe(false);
    });
  });

  it("stays dormant without data-key", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({}));
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("carries data-user and data-group into batch attributes", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_s", "data-user": "u_1", "data-group": "org_9" }));
    t.flush();
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $user_id: "u_1", $group_id: "org_9" });
  });

  it("sends $kind web by default and $page_view on load", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet" }));
    t.flush();
    await drain();
    expect(sent[0].body.attributes.$kind).toBe("web");
    expect(sent[0].body.events[0].name).toBe("$page_view");
  });

  it("data-kind switches automatic tracking to $screen_view with the route path", async () => {
    history.replaceState(null, "", "/settings/profile?tab=1");
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet", "data-kind": "app", "data-os": "macos", "data-app-version": "2.4.1" }));
    t.flush();
    await drain();
    const attrs = sent[0].body.attributes;
    expect(attrs.$kind).toBe("app");
    expect(attrs.$os).toBe("macos");
    expect(attrs.$app_version).toBe("2.4.1");
    const ev = sent[0].body.events[0];
    expect(ev.name).toBe("$screen_view");
    const ea = ev.attributes as Record<string, unknown>;
    expect(ea.$screen).toBe("/settings/profile");
    expect(ea.$host).toBeUndefined();
    expect(ea.$referrer).toBeUndefined();
  });

  it("reads every environment data attribute", async () => {
    const s = scriptTag({
      "data-key": "ak_snip", "data-auto": "off", "data-kind": "app", "data-platform": "electron",
      "data-os": "macos", "data-os-version": "14.2", "data-os-name": "macOS 14.2",
      "data-browser": "chrome", "data-browser-version": "126", "data-device": "desktop",
    });
    const t = new Twillingate();
    autoInit(t, s);
    t.track("probe");
    await drain();
    expect(sent[0].body.attributes).toMatchObject({
      $kind: "app", $platform: "electron", $os: "macos", $os_version: "14.2", $os_name: "macOS 14.2",
      $browser: "chrome", $browser_version: "126", $device: "desktop",
    });
  });

  it("app kind tracks pushState navigations as screen views", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_snippet", "data-kind": "app" }));
    history.pushState(null, "", "/two");
    t.flush();
    await drain();
    const names = sent.flatMap((s) => s.body.events.map((e) => e.name));
    expect(names).toEqual(["$screen_view", "$screen_view"]);
  });
});

describe("script tag and init parity", () => {
  // Every data-* attribute must have an InitOptions field. A new attribute
  // added without one would silently do nothing in bundled apps.
  it("maps every data attribute to an InitOptions field", () => {
    const src = twillingateSource;
    // exec loop rather than matchAll: the tsconfig lib is pinned at ES2019
    // for the shipped bundle's browser target.
    const attrs: string[] = [];
    const re = /getAttribute\("data-([a-z-]+)"\)/g;
    for (let m = re.exec(src); m !== null; m = re.exec(src)) attrs.push(m[1]);
    const optionFor: Record<string, string> = {
      key: "key", identity: "identity", user: "user", group: "group",
      auto: "autoPageviews", "mask-url": "maskUrl", routing: "routing",
      kind: "kind", platform: "platform", os: "os", "os-version": "osVersion", "os-name": "osName",
      browser: "browser", "browser-version": "browserVersion", device: "device",
      "app-version": "appVersion",
    };
    expect(attrs.length).toBeGreaterThan(0);
    for (const a of attrs) {
      const opt = optionFor[a];
      expect(opt, `data-${a} has no InitOptions field`).toBeDefined();
      // Required (key: string) or optional (maskUrl?: MaskSpec) both count.
      const declared = new RegExp(`^\\s+${opt}\\??:`, "m");
      expect(declared.test(src), `InitOptions declares no ${opt}`).toBe(true);
    }
  });

  it("reads data-mask-url and data-routing", async () => {
    document.body.innerHTML = "";
    const s = document.createElement("script");
    s.src = URL_BASE + "/js/twillingate.js";
    s.setAttribute("data-key", "ak_test");
    s.setAttribute("data-mask-url", "uuid");
    s.setAttribute("data-routing", "hash");
    document.body.appendChild(s);
    history.replaceState(null, "", "/app/#/u/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31");

    const t = new Twillingate();
    autoInit(t, s);
    t.flush();
    await drain();
    const mine = sent.flatMap((x) => x.body.events);
    expect((mine[0].attributes as Record<string, string>).$path).toBe("/app/#/u/[id]");
  });

  it("exposes the helpers under twillingate.util", () => {
    const t = new Twillingate();
    expect(typeof t.util.maskIds).toBe("function");
    expect(typeof t.util.withQuery).toBe("function");
  });

  // The entry pageview is synchronous, so a listener registered after
  // init cannot affect it. That is a warning, not a silent miss -- and
  // data-mask-url, resolved during init, is the mechanism that does cover
  // the entry page.
  it("warns when a listener is registered after the first pageview", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    history.replaceState(null, "", "/account/88");
    const t = tg({ key: "ak_late", autoPageviews: true });
    t.page(({ path }) => ({ $path: path.replace(/\/\d+$/, "/[id]") }));
    t.flush();
    await drain();
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("after the first pageview"));
    const mine = sent.filter((x) => x.body.key === "ak_late").flatMap((x) => x.body.events);
    expect((mine[0].attributes as Record<string, string>).$path).toBe("/account/88");
  });
});
