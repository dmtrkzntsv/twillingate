// optOut: the person's flag OR the site's callback; debug: the option,
// the method, or the localStorage flag, all logging with the instance name.
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

function tg(opts: Partial<InitOptions> = {}, name?: string): Twillingate {
  const t = new Twillingate(name);
  t.init({ key: "ak_test", flushInterval: 0, autoPageviews: false, ...opts });
  return t;
}

async function drain(): Promise<void> {
  await vi.runAllTimersAsync();
  await vi.waitFor(() => {});
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

describe("optOut", () => {
  it("optOut(true) writes the flag and silences every instance; optOut(false) clears it", async () => {
    const a = tg();
    const b = tg({}, "et");
    expect(a.optOut(true)).toBe(true);
    expect(localStorage.getItem("twillingate_ignore")).toBe("true");
    a.track("a");
    b.track("b");
    a.flush();
    b.flush();
    await drain();
    expect(sent).toHaveLength(0);
    expect(b.optOut(false)).toBe(false);
    expect(localStorage.getItem("twillingate_ignore")).toBeNull();
    a.track("a2");
    a.flush();
    await drain();
    expect(sent).toHaveLength(1);
  });

  it("the callback is consulted at every event and OR-ed with the flag", async () => {
    let dev = true;
    const t = tg({ optOut: () => dev });
    expect(t.optOut()).toBe(true);
    t.track("dropped");
    dev = false;
    expect(t.optOut()).toBe(false);
    t.track("sent");
    t.flush();
    await drain();
    expect(sent.flatMap((s) => s.body.events.map((e) => e.name))).toEqual(["sent"]);
  });

  it("optOut: true is always out; a throwing callback counts as out, with one warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const a = tg({ optOut: true });
    a.track("x");
    const b = tg({ optOut: () => { throw new Error("boom"); } }, "et");
    b.track("y");
    b.track("z");
    a.flush();
    b.flush();
    await drain();
    expect(sent).toHaveLength(0);
    expect(warn).toHaveBeenCalledTimes(1);
  });
});

describe("debug", () => {
  it("logs each event and each send with the instance name, via the option", async () => {
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const t = tg({ debug: true }, "econumo");
    t.track("budget_created", { currency: "EUR" });
    t.flush();
    await drain();
    const lines = log.mock.calls.map((c) => String(c[0]));
    expect(lines.some((l) => l.startsWith("[twillingate:econumo] budget_created"))).toBe(true);
    expect(lines.some((l) => l.startsWith("[twillingate:econumo] sent 1 event(s) → 202"))).toBe(true);
    expect(sent).toHaveLength(1); // logging never changes what is sent
  });

  it("the localStorage flag turns logging on for every instance, and debug(flag) writes it", async () => {
    const log = vi.spyOn(console, "log").mockImplementation(() => {});
    const t = tg();
    t.track("quiet");
    t.flush();
    await drain();
    expect(log).not.toHaveBeenCalled();
    localStorage.setItem("twillingate_debug", "true");
    expect(t.debug()).toBe(true);
    t.track("loud");
    t.flush();
    await drain();
    expect(log.mock.calls.some((c) => String(c[0]).startsWith("[twillingate] loud"))).toBe(true);
    expect(t.debug(false)).toBe(false);
    expect(localStorage.getItem("twillingate_debug")).toBeNull();
  });
});
