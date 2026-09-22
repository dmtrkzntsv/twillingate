// The consent resolver: data-consent / init({consent}) → a function the
// SDK consults at every storage decision. Storage semantics under consent
// are in the second describe block, added in Task 2.
import { afterEach, describe, expect, it, vi } from "vitest";
import { resolveConsent } from "./consent";

const g = globalThis as Record<string, unknown>;

afterEach(() => {
  vi.restoreAllMocks();
  delete g.consentFlag;
  delete g.consentFn;
});

describe("resolveConsent", () => {
  it("treats absent, empty and false-ish literals as no consent", () => {
    for (const spec of [undefined, null, false, "", "false"] as const) {
      expect(resolveConsent(spec)(), String(spec)).toBe(false);
    }
  });

  it("treats true and \"true\" as consent", () => {
    expect(resolveConsent(true)()).toBe(true);
    expect(resolveConsent("true")()).toBe(true);
  });

  it("calls a function every time and coerces its result", () => {
    const fn = vi.fn().mockReturnValueOnce("yes").mockReturnValueOnce(0);
    const c = resolveConsent(fn);
    expect(c()).toBe(true);
    expect(c()).toBe(false);
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("reads a named global variable live", () => {
    g.consentFlag = false;
    const c = resolveConsent("consentFlag");
    expect(c()).toBe(false);
    g.consentFlag = true;
    expect(c()).toBe(true);
  });

  it("calls a named global function live", () => {
    let granted = false;
    g.consentFn = () => granted;
    const c = resolveConsent("consentFn");
    expect(c()).toBe(false);
    granted = true;
    expect(c()).toBe(true);
  });

  it("fails closed with one warning when the name resolves to nothing", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const c = resolveConsent("noSuchConsent");
    expect(c()).toBe(false);
    expect(c()).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0][0]).toContain("noSuchConsent");
  });

  it("fails closed with one warning when the function throws", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const c = resolveConsent(() => {
      throw new Error("boom");
    });
    expect(c()).toBe(false);
    expect(c()).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("picks up a global that appears after the first consult", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const c = resolveConsent("consentFlag");
    expect(c()).toBe(false);
    g.consentFlag = true;
    expect(c()).toBe(true);
  });
});
