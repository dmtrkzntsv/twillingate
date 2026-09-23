// A build whose __TWILLINGATE_URL__ placeholder was never substituted (i.e.
// this file, deliberately without the `vi.mock("./origin")` every other
// suite uses) has no collector to post to. init() must warn and leave the
// instance dormant rather than ever calling fetch.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Twillingate } from "./twillingate";
import { runtime } from "./runtime";

beforeEach(() => {
  runtime.reset();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("an unsubstituted collector origin", () => {
  it("warns and stays dormant: init() refuses, and no call ever reaches fetch", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);

    const t = new Twillingate();
    t.init({ key: "ak_x", flushInterval: 0 });
    t.track("x");
    t.flush();
    await vi.runAllTimersAsync();

    expect(warn).toHaveBeenCalled();
    const messages = warn.mock.calls.map((c) => String(c[0]));
    expect(messages.some((m) => m.includes("collector origin"))).toBe(true);
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});
