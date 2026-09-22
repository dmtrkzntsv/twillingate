// Named instances: a second tag on one page gets its own global and its
// own storage prefix; a bundled consumer gets the prefix alone.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate, bootstrap, instanceName } from "./twillingate";

const URL_BASE = "https://collector.example.com";
const g = globalThis as Record<string, unknown>;

interface Sent {
  body: { key: string; attributes: Record<string, unknown>; events: Array<Record<string, unknown>> };
}

let sent: Sent[];
let fetchImpl: (url: string, init: { body: string }) => Promise<{ status: number }>;

function okFetch(_url: string, init: { body: string }): Promise<{ status: number }> {
  sent.push({ body: JSON.parse(init.body) });
  return Promise.resolve({ status: 202 });
}

function tg(opts: Partial<Parameters<Twillingate["init"]>[0]> = {}, instance?: string): Twillingate {
  const t = new Twillingate(instance);
  t.init({ key: "ak_test", url: URL_BASE, flushInterval: 0, ...opts });
  return t;
}

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

const nativePushState = history.pushState;

beforeEach(() => {
  vi.useFakeTimers();
  sent = [];
  fetchImpl = okFetch;
  vi.stubGlobal("fetch", (url: string, init: { body: string }) => fetchImpl(url, init));
  localStorage.clear();
  history.replaceState(null, "", "/start");
  delete g.twillingate;
  delete g.et;
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  history.pushState = nativePushState;
  delete g.twillingate;
  delete g.et;
});

describe("instanceName", () => {
  it("accepts an identifier and falls back to the default otherwise, with a warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    expect(instanceName("et")).toBe("et");
    expect(instanceName("a_1")).toBe("a_1");
    expect(instanceName(null)).toBe("twillingate");
    expect(instanceName(undefined)).toBe("twillingate");
    expect(instanceName("")).toBe("twillingate");
    expect(warn).not.toHaveBeenCalled();
    for (const bad of ["Et", "1et", "e-t", "e t", "a".repeat(17)]) {
      expect(instanceName(bad), bad).toBe("twillingate");
    }
    expect(warn).toHaveBeenCalledTimes(5);
  });
});

describe("data-instance", () => {
  it("registers the named global, leaves window.twillingate alone and prefixes storage keys", async () => {
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-identity": "identified", "data-consent": "true", "data-auto": "off" }));
    expect(et).toBeInstanceOf(Twillingate);
    expect(g.et).toBe(et);
    expect(g.twillingate).toBeUndefined();
    expect((et as Twillingate & { VERSION: string }).VERSION).toBeTypeOf("string");
    const attrs = await lastAttributes(et!);
    expect(localStorage.getItem("et_visitor")).toBe(attrs.$install_id);
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    et!.identify("u_1", "Ada");
    et!.group("org_1", "Acme");
    expect(localStorage.getItem("et_user")).toBe("u_1");
    expect(localStorage.getItem("et_user_name")).toBe("Ada");
    expect(localStorage.getItem("et_group")).toBe("org_1");
    expect(localStorage.getItem("et_group_name")).toBe("Acme");
  });

  it("loads dormant without data-key for init() in code", async () => {
    const et = bootstrap(scriptTag({ "data-instance": "et" }))!;
    expect(g.et).toBe(et);
    await drain();
    expect(sent).toHaveLength(0);
    et.init({ key: "ak_code", url: URL_BASE, flushInterval: 0 });
    expect((await lastAttributes(et)).$kind).toBe("web");
    expect(sent[0].body.key).toBe("ak_code");
  });

  it("without the attribute, behaviour is exactly as today", () => {
    const t = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }));
    expect(g.twillingate).toBe(t);
    expect(g.et).toBeUndefined();
  });

  it("beats a conflicting instance option, with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const et = bootstrap(scriptTag({ "data-instance": "et" }))!;
    et.init({ key: "ak_code", url: URL_BASE, flushInterval: 0, identity: "identified", consent: true, instance: "other" });
    await lastAttributes(et);
    expect(localStorage.getItem("et_visitor")).not.toBeNull();
    expect(localStorage.getItem("other_visitor")).toBeNull();
    expect(warn.mock.calls.flat().join(" ")).toContain("other");
  });

  it("refuses an invalid name and keeps the default", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = bootstrap(scriptTag({ "data-key": "ak_web", "data-instance": "Not-Valid", "data-auto": "off" }));
    expect(g.twillingate).toBe(t);
    expect(g["Not-Valid"]).toBeUndefined();
    expect(warn).toHaveBeenCalled();
  });

  it("never overwrites a foreign global", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    g.et = { theirs: true };
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }));
    expect(g.et).toEqual({ theirs: true });
    expect(warn.mock.calls.flat().join(" ")).toContain("et");
    // The tag still tracks; it is just not reachable through the global.
    expect((await lastAttributes(et!)).$kind).toBe("web");
  });

  it("stands down for a duplicate of the same named tag, not for the default tag beside it", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const web = bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }));
    const et = bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }));
    expect(web).not.toBeNull();
    expect(et).not.toBeNull();
    expect(bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et", "data-auto": "off" }))).toBeNull();
    expect(bootstrap(scriptTag({ "data-key": "ak_web", "data-auto": "off" }))).toBeNull();
    expect(g.twillingate).toBe(web);
    expect(g.et).toBe(et);
  });

  it("two tags both emit a pageview on pushState, each under its own key", async () => {
    bootstrap(scriptTag({ "data-key": "ak_web" }));
    bootstrap(scriptTag({ "data-key": "ak_et", "data-instance": "et" }));
    history.pushState(null, "", "/second");
    (g.twillingate as Twillingate).flush();
    (g.et as Twillingate).flush();
    await drain();
    const byKey = (key: string) =>
      sent.filter((s) => s.body.key === key).flatMap((s) => s.body.events.map((e) => (e.attributes as Record<string, string>).$path));
    expect(byKey("ak_web")).toEqual(["/start", "/second"]);
    expect(byKey("ak_et")).toEqual(["/start", "/second"]);
  });
});

describe("instance option", () => {
  it("prefixes storage keys and registers no global", async () => {
    const t = tg({ identity: "identified", consent: true, instance: "et" });
    const attrs = await lastAttributes(t);
    expect(localStorage.getItem("et_visitor")).toBe(attrs.$install_id);
    expect(localStorage.getItem("twillingate_visitor")).toBeNull();
    expect(g.et).toBeUndefined();
  });

  it("two code instances with different names share neither a visitor id nor a queue", async () => {
    const a = tg({ identity: "identified", consent: true, instance: "alpha" });
    const b = tg({ identity: "identified", consent: true, instance: "beta" });
    const va = (await lastAttributes(a)).$install_id;
    const vb = (await lastAttributes(b)).$install_id;
    expect(va).not.toBe(vb);
    fetchImpl = () => Promise.reject(new TypeError("down"));
    a.track("a-fails");
    a.flush();
    await drain();
    expect(JSON.parse(localStorage.getItem("alpha_queue")!)).toHaveLength(1);
    expect(localStorage.getItem("beta_queue")).toBeNull();
  });

  it("two code instances with no name share one visitor id", async () => {
    const a = tg({ identity: "identified", consent: true });
    const b = tg({ identity: "identified", consent: true });
    expect((await lastAttributes(a)).$install_id).toBe((await lastAttributes(b)).$install_id);
  });

  it("refuses an invalid name and keeps the default, with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = tg({ identity: "identified", consent: true, instance: "Bad Name" });
    await lastAttributes(t);
    expect(localStorage.getItem("twillingate_visitor")).not.toBeNull();
    expect(warn).toHaveBeenCalled();
  });
});
