// The v2 API surface: attrs defaults, identify/group with display
// names, and the page() overloads (current page / explicit path / listener).
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

function lastEvent(): Record<string, unknown> {
  const events = sent[sent.length - 1].body.events;
  return events[events.length - 1];
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
});

describe("attrs", () => {
  it("merges defaults under every event, event attrs winning", async () => {
    const t = tg();
    t.attrs({ tier: "beta", region: "eu" });
    t.track("signup", { tier: "pro" });
    t.flush();
    await drain();
    expect(lastEvent().attributes).toEqual({ tier: "pro", region: "eu" });
  });

  it("applies to pageviews and screens too", async () => {
    const t = tg();
    t.attrs({ ab_test: "b" });
    t.page();
    t.screen("/home");
    t.flush();
    await drain();
    const [pv, sc] = sent[0].body.events.map((e) => e.attributes as Record<string, unknown>);
    expect(pv.ab_test).toBe("b");
    expect(pv.$host).toBe("example.com"); // reserved keys unaffected
    expect(sc).toEqual({ ab_test: "b", $screen: "/home" });
  });

  it("successive calls merge; attrs(null) clears", async () => {
    const t = tg();
    t.attrs({ a: 1 });
    t.attrs({ b: 2 });
    t.track("both");
    t.attrs(null);
    t.track("none");
    t.flush();
    await drain();
    const [both, none] = sent[0].body.events.map((e) => e.attributes);
    expect(both).toEqual({ a: 1, b: 2 });
    expect(none).toEqual({});
  });
});

describe("identify and group with display names", () => {
  it("identify(user, name) sends $user_id and $user_name", async () => {
    const t = tg({ identity: "identified" });
    t.identify("user-123", "Ada Lovelace");
    t.track("probe");
    t.flush();
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $user_id: "user-123", $user_name: "Ada Lovelace" });
  });

  it("group(id, name) sends $group_id and $group_name, and persists them for an identified instance with consent", async () => {
    const t = tg({ identity: "identified", consent: true });
    t.group("org-9", "Acme Corp");
    t.track("probe");
    t.flush();
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $group_id: "org-9", $group_name: "Acme Corp" });
    expect(localStorage.getItem("twillingate_group")).toBe("org-9");
    expect(localStorage.getItem("twillingate_group_name")).toBe("Acme Corp");
  });

  it("an anonymous instance never carries a user name; an identified one persists and restores it", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const anon = tg();
    anon.identify("u_1", "Plain Name");
    anon.track("probe");
    anon.flush();
    await drain();
    expect(sent[0].body.attributes).not.toHaveProperty("$user_id");
    expect(sent[0].body.attributes).not.toHaveProperty("$user_name");
    expect(localStorage.getItem("twillingate_user")).toBeNull();
    expect(localStorage.getItem("twillingate_user_name")).toBeNull();
    expect(warn).toHaveBeenCalledTimes(1);

    sent = [];
    const t = tg({ identity: "identified", consent: true });
    t.identify("u_1", "Ada");
    expect(localStorage.getItem("twillingate_user_name")).toBe("Ada");

    const next = tg({ identity: "identified", consent: true }); // next page load
    next.track("probe");
    next.flush();
    await drain();
    expect(sent[0].body.attributes).toMatchObject({ $user_id: "u_1", $user_name: "Ada" });
  });

  it("reset() clears names along with ids", async () => {
    const t = tg({ identity: "identified" });
    t.identify("u_1", "Ada");
    t.group("org-9", "Acme");
    t.reset();
    expect(localStorage.getItem("twillingate_user_name")).toBeNull();
    expect(localStorage.getItem("twillingate_group_name")).toBeNull();
    t.track("probe");
    t.flush();
    await drain();
    const attrs = sent[0].body.attributes;
    expect(attrs.$user_name).toBeUndefined();
    expect(attrs.$group_name).toBeUndefined();
  });
});

describe("page() overloads", () => {
  it("page(path) records an explicit path", async () => {
    const t = tg();
    t.page("/settings");
    t.flush();
    await drain();
    const ev = lastEvent();
    expect(ev.name).toBe("$page_view");
    expect((ev.attributes as Record<string, unknown>).$path).toBe("/settings");
    expect((ev.attributes as Record<string, unknown>).$host).toBe("example.com");
  });

  it("dedupes on the explicit path", async () => {
    const t = tg();
    t.page("/settings");
    t.page("/settings");
    t.page("/other");
    t.flush();
    await drain();
    expect(sent[0].body.events).toHaveLength(2);
  });

  it("page(attrs) still treats an object as extra attributes for the current page", async () => {
    const t = tg();
    t.page({ section: "docs" });
    t.flush();
    await drain();
    const attrs = lastEvent().attributes as Record<string, unknown>;
    expect(attrs.section).toBe("docs");
    expect(attrs.$host).toBe("example.com");
  });

  it("page(listener) registers a pageview listener that can enrich attributes", async () => {
    const t = tg();
    const seen: string[] = [];
    t.onPage((p) => {
      seen.push(p.path);
      return { enriched: true };
    });
    t.page("/a");
    t.flush();
    await drain();
    expect(seen).toEqual(["/a"]);
    expect((lastEvent().attributes as Record<string, unknown>).enriched).toBe(true);
  });

  it("a listener returning false cancels the pageview", async () => {
    const t = tg();
    t.onPage((p) => (p.path === "/private" ? false : undefined));
    t.page("/private");
    t.page("/public");
    t.flush();
    await drain();
    const paths = sent[0].body.events.map((e) => (e.attributes as Record<string, string>).$path);
    expect(paths).toEqual(["/public"]);
  });

  it("listeners fire for automatic SPA pageviews too", async () => {
    const seen: string[] = [];
    const t = tg({ autoPageviews: true });
    t.onPage((p) => {
      seen.push(p.path);
    });
    history.pushState(null, "", "/second");
    t.flush();
    await drain();
    expect(seen).toContain("/second");
  });

  it("page(fn) still registers a listener, with a deprecation warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const t = tg();
    t.page(() => ({ legacy: true }));
    expect(warn.mock.calls.flat().join(" ")).toContain("deprecated");
    t.page("/a");
    t.flush();
    await drain();
    expect((lastEvent().attributes as Record<string, unknown>).legacy).toBe(true);
  });
});

describe("precedence: derived < attrs() defaults < call < listeners", () => {
  it("an attrs() default overrides a derived pageview value, and null drops it", async () => {
    Object.defineProperty(document, "referrer", { value: "https://news.example.org/", configurable: true });
    const t = tg();
    t.attrs({ $host: "selfhosted_ab12", $referrer: null });
    t.page("/budget");
    t.flush();
    await drain();
    const attrs = lastEvent().attributes as Record<string, unknown>;
    expect(attrs.$host).toBe("selfhosted_ab12");
    expect(attrs).not.toHaveProperty("$referrer");
    expect(attrs.$path).toBe("/budget");
  });

  it("the call's attributes beat the defaults, and a listener beats the call", async () => {
    const t = tg();
    t.attrs({ tier: "beta", region: "eu" });
    t.onEvent(() => ({ region: "us" }));
    t.track("e", { tier: "pro" });
    t.flush();
    await drain();
    expect(lastEvent().attributes).toEqual({ tier: "pro", region: "us" });
  });
});

describe("null drops an attribute", () => {
  it("omits null and undefined values, keeps 0 and empty strings", async () => {
    const t = tg();
    t.track("e", { a: null, b: undefined, c: 0, d: "" });
    t.flush();
    await drain();
    expect(lastEvent().attributes).toEqual({ c: 0, d: "" });
  });

  it("suppresses a derived pageview attribute for one call, while $host overrides reach the wire", async () => {
    Object.defineProperty(document, "referrer", { value: "https://news.example.org/", configurable: true });
    const t = tg();
    t.page("/budget", { $host: "selfhosted_ab12", $referrer: null });
    t.flush();
    await drain();
    const attrs = lastEvent().attributes as Record<string, unknown>;
    expect(attrs.$host).toBe("selfhosted_ab12");
    expect(attrs.$path).toBe("/budget");
    expect(attrs).not.toHaveProperty("$referrer");
  });

  it("lets an event null out an attrs() default", async () => {
    const t = tg();
    t.attrs({ region: "eu", tier: "beta" });
    t.track("e", { region: null });
    t.flush();
    await drain();
    expect(lastEvent().attributes).toEqual({ tier: "beta" });
  });
});

describe("flush timer", () => {
  it("holds events for 10 seconds by default", async () => {
    const t = new Twillingate();
    t.init({ key: "ak_test", autoPageviews: false });
    t.track("first");
    await vi.advanceTimersByTimeAsync(5000);
    t.track("second");
    await vi.advanceTimersByTimeAsync(4999);
    expect(sent).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    await vi.waitFor(() => {});
    expect(sent).toHaveLength(1);
    expect(sent[0].body.events.map((e) => e.name)).toEqual(["first", "second"]);
  });
});
