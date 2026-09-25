// Tagged elements reach every instance with taggedEvents on, through the
// one listener set the runtime owns; the event carries its name and path.
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

function click(id: string): void {
  document.getElementById(id)!.dispatchEvent(new MouseEvent("click", { bubbles: true, button: 0 }));
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
  history.replaceState(null, "", "/pricing");
  document.body.innerHTML = '<button id="b" data-twillingate-event="signup" data-twillingate-plan="pro">Go</button>';
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("tagged elements", () => {
  it("track the event name with the path and the event's own location, on every instance with them on", async () => {
    const web = tg({ key: "ak_web" });
    const et = tg({ key: "ak_et" }, "et");
    click("b");
    web.flush();
    et.flush();
    await drain();
    const byKey = (key: string) => sent.filter((s) => s.body.key === key).flatMap((s) => s.body.events);
    expect(byKey("ak_web")).toHaveLength(1);
    expect(byKey("ak_web")[0].name).toBe("signup");
    expect(byKey("ak_web")[0].attributes).toEqual({ path: "/pricing", $host: "example.com", $path: "/pricing" });
    expect(byKey("ak_et")).toHaveLength(1);
  });

  it("taggedEvents: false opts one instance out", async () => {
    const web = tg({ key: "ak_web" });
    const et = tg({ key: "ak_et", taggedEvents: false }, "et");
    click("b");
    web.flush();
    et.flush();
    await drain();
    expect(sent.map((s) => s.body.key)).toEqual(["ak_web"]);
  });

  it("go through attrs() defaults and onEvent like any event", async () => {
    const t = tg();
    t.attrs({ site: "docs" });
    t.onEvent(() => ({ via: "markup" }));
    click("b");
    t.flush();
    await drain();
    expect(sent[0].body.events[0].attributes).toEqual({
      path: "/pricing", $host: "example.com", $path: "/pricing", site: "docs", via: "markup",
    });
  });
});
