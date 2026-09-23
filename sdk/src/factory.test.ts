// The global: default instance plus registry, and the bundle entry's
// decisions about what it finds at window.twillingate.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate } from "./twillingate";
import { TwillingateGlobal, bootstrap, isSDK, supersededBy } from "./factory";
import { runtime } from "./runtime";

vi.mock("./origin", () => ({
  ORIGIN: "https://collector.example.com",
  collectorOrigin: () => "https://collector.example.com",
}));

const URL_BASE = "https://collector.example.com";
const g = globalThis as Record<string, unknown>;

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
}

async function lastAttributes(t: Twillingate): Promise<Record<string, unknown>> {
  t.track("probe");
  t.flush();
  await drain();
  return sent[sent.length - 1].body.attributes;
}

function scriptTag(attrs: Record<string, string>): HTMLScriptElement {
  const s = document.createElement("script");
  s.src = URL_BASE + "/js/twillingate.js";
  for (const [k, v] of Object.entries(attrs)) s.setAttribute(k, v);
  return s;
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
  delete g.twillingate;
  delete g.et;
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  delete g.twillingate;
  delete g.et;
});

describe("registry", () => {
  it("create(name, opts) builds, inits and registers; get(name) finds it; get() is the default", async () => {
    const tg = new TwillingateGlobal();
    const et = tg.create("et", { key: "ak_et", flushInterval: 0, autoPageviews: false, identity: "identified", consent: true });
    expect(et).toBeInstanceOf(Twillingate);
    expect(et).not.toBe(tg);
    expect(tg.get("et")).toBe(et);
    expect(tg.get()).toBe(tg);
    expect(tg.get("twillingate")).toBe(tg);
    expect(tg.get("nope")).toBeUndefined();
    expect(tg.instances()).toEqual(["et"]);
    const attrs = await lastAttributes(et);
    expect(sent[0].body.key).toBe("ak_et");
    expect(localStorage.getItem("et_visitor")).toBe(attrs.$install_id);
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    expect(typeof tg.VERSION).toBe("string");
  });

  it("create(name) without options is dormant until init()", async () => {
    const tg = new TwillingateGlobal();
    const et = tg.create("et");
    et.track("early");
    await drain();
    expect(sent).toHaveLength(0);
    et.init({ key: "ak_code", flushInterval: 0, autoPageviews: false });
    et.flush();
    await drain();
    expect(sent[0].body.key).toBe("ak_code");
    expect(sent[0].body.events[0].name).toBe("early");
  });

  it("a duplicate name returns the existing instance and ignores new options, with a warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const tg = new TwillingateGlobal();
    const a = tg.create("et", { key: "ak_a", flushInterval: 0, autoPageviews: false });
    const b = tg.create("et", { key: "ak_b", flushInterval: 0 });
    expect(b).toBe(a);
    expect(warn.mock.calls.flat().join(" ")).toContain("et");
    expect(tg.create("et")).toBe(a);
  });

  it("refuses the default name and an invalid name, returning the default instance", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const tg = new TwillingateGlobal();
    expect(tg.create("twillingate", { key: "ak_x" })).toBe(tg);
    expect(tg.create("Not-Valid", { key: "ak_x" })).toBe(tg);
    expect(tg.instances()).toEqual([]);
    expect(warn).toHaveBeenCalledTimes(2);
    expect(g.twillingate).toBeUndefined();
  });

  it("two instances share neither a visitor id nor a queue", async () => {
    const tg = new TwillingateGlobal();
    const a = tg.create("alpha", { key: "ak_a", flushInterval: 0, autoPageviews: false, identity: "identified", consent: true });
    const b = tg.create("beta", { key: "ak_b", flushInterval: 0, autoPageviews: false, identity: "identified", consent: true });
    expect((await lastAttributes(a)).$install_id).not.toBe((await lastAttributes(b)).$install_id);
    vi.stubGlobal("fetch", () => Promise.reject(new TypeError("down")));
    a.track("a-fails");
    a.flush();
    await drain();
    expect(JSON.parse(localStorage.getItem("alpha_queue")!)).toHaveLength(1);
    expect(localStorage.getItem("beta_queue")).toBeNull();
  });
});

describe("bootstrap", () => {
  it("registers the global and auto-inits from the tag", async () => {
    const tg = bootstrap(scriptTag({ "data-key": "ak_web" }));
    expect(g.twillingate).toBe(tg);
    expect(isSDK(g.twillingate)).toBe(true);
    (tg as Twillingate).flush();
    await drain();
    expect(sent[0].body.key).toBe("ak_web");
    expect(sent[0].body.events[0].name).toBe("$page_view");
  });

  it("registers a dormant global without data-key", async () => {
    const tg = bootstrap(scriptTag({}))!;
    expect(g.twillingate).toBe(tg);
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("data-instance lands in the registry; no window.<name>", async () => {
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-identity": "identified", "data-consent": "true", "data-auto": "off" }))!;
    const tg = g.twillingate as TwillingateGlobal;
    expect(isSDK(tg)).toBe(true);
    expect(tg.get("et")).toBe(et);
    expect(g.et).toBeUndefined();
    const attrs = await lastAttributes(et);
    expect(sent[0].body.key).toBe("ak_et");
    expect(localStorage.getItem("et_visitor")).toBe(attrs.$install_id);
  });

  it("a data-instance tag without data-key registers a dormant named instance", async () => {
    const et = bootstrap(scriptTag({ "data-instance": "et" }))!;
    expect((g.twillingate as TwillingateGlobal).get("et")).toBe(et);
    await drain();
    expect(sent).toHaveLength(0);
    et.init({ key: "ak_code", flushInterval: 0, autoPageviews: false });
    expect((await lastAttributes(et)).$kind).toBe("web");
  });

  it("a second bundle copy with data-instance joins the first copy's registry", async () => {
    const web = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }))!;
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }))!;
    expect(g.twillingate).toBe(web);
    expect((web as TwillingateGlobal).get("et")).toBe(et);
    expect(runtime.subscribers()).toBe(2);
    history.pushState(null, "", "/second");
    // both instances have autoPageviews off: one navigation reached both, neither emitted
    web.flush();
    et.flush();
    await drain();
    expect(sent).toHaveLength(0);
  });

  it("one pushState patch serves two tags, each into its own project", async () => {
    bootstrap(scriptTag({ "data-key": "ak_web" }));
    bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et" }));
    history.pushState(null, "", "/second");
    const tg = g.twillingate as TwillingateGlobal;
    tg.flush();
    tg.get("et")!.flush();
    await drain();
    const byKey = (key: string) =>
      sent.filter((s) => s.body.key === key).flatMap((s) => s.body.events.map((e) => (e.attributes as Record<string, string>).$path));
    expect(byKey("ak_web")).toEqual(["/start", "/second"]);
    expect(byKey("ak_et")).toEqual(["/start", "/second"]);
  });

  it("stands down for a duplicate default tag with the same key or no key", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const web = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }));
    expect(bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }))).toBeNull();
    expect(bootstrap(scriptTag({}))).toBeNull();
    expect(g.twillingate).toBe(web);
  });

  it("a duplicate named tag stands down inside the registry", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }));
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }));
    expect(bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }))).toBe(et);
    expect((g.twillingate as TwillingateGlobal).instances()).toEqual(["et"]);
  });

  it("a different key takes over the default instance, with a warning", async () => {
    const first = bootstrap(scriptTag({ "data-key": "ak_a", "data-auto": "off" }));
    (first as TwillingateGlobal).create("et", { key: "ak_et", flushInterval: 0, autoPageviews: false });
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const second = bootstrap(scriptTag({ "data-key": "ak_b", "data-auto": "off" }));
    expect(second).not.toBe(first);
    expect(g.twillingate).toBe(second);
    expect(warn.mock.calls.flat().join(" ")).toContain("ak_a -> ak_b");
    // the old default's registry carries over to the new one...
    expect((second as TwillingateGlobal).get("et")).toBe((first as TwillingateGlobal).get("et"));
    expect((second as TwillingateGlobal).instances()).toEqual(["et"]);
    expect(runtime.subscribers()).toBe(2); // the new default plus et
    // ...and the old default is retired: it produces nothing more under ak_a
    history.pushState(null, "", "/after");
    (first as Twillingate).flush();
    (second as Twillingate).flush();
    await drain();
    (first as Twillingate).track("dead");
    (first as Twillingate).flush();
    await drain();
    expect(sent.some((s) => s.body.key === "ak_a")).toBe(false);
  });

  it("never overwrites a foreign global, and still tracks unregistered", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const foreign = { init() {} };
    g.twillingate = foreign;
    const tg = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }))!;
    expect(g.twillingate).toBe(foreign);
    expect(warn.mock.calls.flat().join(" ")).toContain("taken");
    expect((await lastAttributes(tg)).$kind).toBe("web");
  });

  it("an invalid data-instance falls back to the default instance with a warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = bootstrap(scriptTag({ "data-key": "ak_web", "data-instance": "Not-Valid", "data-auto": "off" }));
    expect(g.twillingate).toBe(t);
    expect(warn).toHaveBeenCalled();
  });
});

describe("supersededBy", () => {
  it("reads the loaded key structurally", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const tg = new TwillingateGlobal();
    tg.init({ key: "ak_same", flushInterval: 0, autoPageviews: false });
    expect(supersededBy(tg, scriptTag({ "data-key": "ak_same" }))).toBe(true);
    expect(supersededBy(tg, scriptTag({}))).toBe(true);
    expect(supersededBy(tg, scriptTag({ "data-key": "ak_other" }))).toBe(false);
    expect(supersededBy(new TwillingateGlobal(), scriptTag({ "data-key": "ak_b" }))).toBe(false);
    expect(supersededBy(undefined, scriptTag({ "data-key": "ak_b" }))).toBe(false);
    expect(supersededBy({ init() {} }, scriptTag({}))).toBe(false);
  });
});
