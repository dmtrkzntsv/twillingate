// Snippet mode: auto-init from a loaded <script> tag's data-* attributes,
// and the data attribute / InitOptions field parity that keeps a new
// attribute from silently doing nothing in bundled apps.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, type InitOptions } from "./twillingate";
import { autoInit, supersededBy } from "./factory";
import { runtime } from "./runtime";
import twillingateSource from "./twillingate.ts?raw";
import factorySource from "./factory.ts?raw";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

const URL_BASE = "https://collector.example.com";
const g = globalThis as Record<string, unknown>;

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

function scriptTag(attrs: Record<string, string>): HTMLScriptElement {
  const s = document.createElement("script");
  s.src = URL_BASE + "/js/twillingate.js";
  for (const [k, v] of Object.entries(attrs)) s.setAttribute(k, v);
  return s;
}

async function lastAttributes(t: Twillingate): Promise<Record<string, unknown>> {
  t.track("probe");
  t.flush();
  await drain();
  return sent[sent.length - 1].body.attributes;
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
  delete g.consentFlag;
  delete g.consentFn;
});

describe("snippet auto-init", () => {
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

  it("ignores data-user and data-group: identity is set from code", async () => {
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_s", "data-identity": "identified", "data-user": "u_1", "data-group": "org_9", "data-auto": "off" }));
    t.track("probe");
    t.flush();
    await drain();
    expect(sent[0].body.attributes).not.toHaveProperty("$user_id");
    expect(sent[0].body.attributes).not.toHaveProperty("$group_id");
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
    autoInit(t, scriptTag({ "data-key": "ak_snippet", "data-kind": "app" }));
    t.flush();
    await drain();
    const attrs = sent[0].body.attributes;
    expect(attrs.$kind).toBe("app");
    const ev = sent[0].body.events[0];
    expect(ev.name).toBe("$screen_view");
    const ea = ev.attributes as Record<string, unknown>;
    expect(ea.$screen).toBe("/settings/profile");
    expect(ea.$host).toBeUndefined();
    expect(ea.$referrer).toBeUndefined();
  });

  it("ignores environment data attributes", async () => {
    const s = scriptTag({
      // "wearable" is reachable only through the init() override, never
      // through detection, so seeing anything else proves the attribute
      // was ignored rather than coincidentally matching a detected value.
      "data-key": "ak_snip", "data-auto": "off", "data-kind": "app", "data-platform": "electron",
      "data-os": "macos", "data-device": "wearable", "data-app-version": "9.9.9",
    });
    const t = new Twillingate();
    autoInit(t, s);
    t.track("probe");
    await drain();
    const attrs = sent[0].body.attributes;
    expect(attrs.$kind).toBe("app");
    // platform was never passed to init(), and $platform only defaults for kind "web"
    expect(attrs).not.toHaveProperty("$platform");
    // appVersion is code-only; the attribute is never read
    expect(attrs).not.toHaveProperty("$app_version");
    // os/device are still detected by the SDK; the attributes did not override them
    expect(attrs.$os).not.toBe("macos");
    expect(attrs.$device).not.toBe("wearable");
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
  // Every data-* attribute the factory reads must have an InitOptions
  // field. A new attribute added without one would silently do nothing in
  // bundled apps.
  it("maps every data attribute to an InitOptions field", () => {
    const attrs: string[] = [];
    const re = /getAttribute\("data-([a-z-]+)"\)/g;
    for (let m = re.exec(factorySource); m !== null; m = re.exec(factorySource)) attrs.push(m[1]);
    const optionFor: Record<string, string> = {
      key: "key", identity: "identity", auto: "autoPageviews", "mask-url": "maskUrl",
      routing: "routing", kind: "kind", consent: "consent",
    };
    expect(attrs.length).toBeGreaterThan(0);
    for (const a of attrs) {
      if (a === "instance") continue; // maps to create()'s name, not an option
      const opt = optionFor[a];
      expect(opt, `data-${a} has no InitOptions field`).toBeDefined();
      const declared = new RegExp(`^\\s+${opt}\\??:`, "m");
      expect(declared.test(twillingateSource), `InitOptions declares no ${opt}`).toBe(true);
    }
    expect(attrs).toContain("instance");
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
    t.onPage(({ path }) => ({ $path: path.replace(/\/\d+$/, "/[id]") }));
    t.flush();
    await drain();
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("after the first pageview"));
    const mine = sent.filter((x) => x.body.key === "ak_late").flatMap((x) => x.body.events);
    expect((mine[0].attributes as Record<string, string>).$path).toBe("/account/88");
  });
});

describe("data-consent attribute", () => {
  it("data-consent resolves a literal, a global variable and a global function", async () => {
    g.consentFlag = true;
    g.consentFn = () => true;
    for (const value of ["true", "consentFlag", "consentFn"]) {
      localStorage.clear();
      sent = [];
      const t = new Twillingate();
      autoInit(t, scriptTag({ "data-key": "ak_s", "data-identity": "identified", "data-consent": value, "data-auto": "off" }));
      const attrs = await lastAttributes(t);
      expect(attrs.$install_id, value).toBe(localStorage.getItem("twillingate_visitor"));
      expect(localStorage.getItem("twillingate_visitor"), value).not.toBeNull();
    }
  });

  it("data-consent naming nothing fails closed with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = new Twillingate();
    autoInit(t, scriptTag({ "data-key": "ak_s", "data-identity": "identified", "data-consent": "noSuchThing", "data-auto": "off" }));
    const attrs = await lastAttributes(t);
    expect(attrs.$install_id).toBeUndefined();
    expect(localStorage.length).toBe(0);
    expect(warn.mock.calls.flat().join(" ")).toContain("noSuchThing");
  });
});
