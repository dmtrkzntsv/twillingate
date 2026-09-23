// Storage drivers: the built-ins, a custom object, a throwing one, and the
// two person-level flags that always live in localStorage.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DEBUG_FLAG, IGNORE_FLAG, cookieDriver, memoryDriver, readFlag, resolveStorage, writeFlag } from "./storage";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  for (const c of document.cookie.split(";")) {
    const k = c.split("=")[0].trim();
    if (k) document.cookie = `${k}=; path=/; max-age=0`;
  }
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("resolveStorage", () => {
  it("defaults to localStorage", () => {
    const d = resolveStorage(undefined);
    d.set("k", "v");
    expect(localStorage.getItem("k")).toBe("v");
    expect(d.get("k")).toBe("v");
    d.remove("k");
    expect(localStorage.getItem("k")).toBeNull();
  });

  it("sessionStorage lives for the tab", () => {
    const d = resolveStorage("sessionStorage");
    d.set("k", "v");
    expect(sessionStorage.getItem("k")).toBe("v");
    expect(localStorage.getItem("k")).toBeNull();
  });

  it("memory keeps nothing on the device", () => {
    const d = resolveStorage("memory");
    d.set("k", "v");
    expect(d.get("k")).toBe("v");
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
    d.remove("k");
    expect(d.get("k")).toBeNull();
    // two memory drivers do not share
    expect(memoryDriver().get("k")).toBeNull();
  });

  it("cookie stores identity keys as host-only cookies and skips the queue", () => {
    const d = resolveStorage("cookie");
    d.set("twillingate_visitor", "v 1");
    expect(document.cookie).toContain("twillingate_visitor=v%201");
    expect(d.get("twillingate_visitor")).toBe("v 1");
    d.set("twillingate_queue", "[{}]");
    expect(document.cookie).not.toContain("twillingate_queue");
    expect(d.get("twillingate_queue")).toBeNull();
    d.remove("twillingate_visitor");
    expect(d.get("twillingate_visitor")).toBeNull();
  });

  it("accepts a custom driver object", () => {
    const store = new Map<string, string>();
    const d = resolveStorage({
      get: (k) => store.get(k) ?? null,
      set: (k, v) => void store.set(k, v),
      remove: (k) => void store.delete(k),
    });
    d.set("k", "v");
    expect(store.get("k")).toBe("v");
    expect(d.get("k")).toBe("v");
  });

  it("treats a throwing driver as unavailable", () => {
    const d = resolveStorage({
      get: () => { throw new Error("no"); },
      set: () => { throw new Error("no"); },
      remove: () => { throw new Error("no"); },
    });
    expect(() => d.set("k", "v")).not.toThrow();
    expect(d.get("k")).toBeNull();
    expect(() => d.remove("k")).not.toThrow();
  });

  it("falls back to localStorage on an unusable spec, with a warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const d = resolveStorage("indexedDB" as never);
    d.set("k", "v");
    expect(localStorage.getItem("k")).toBe("v");
    expect(warn).toHaveBeenCalled();
    expect(resolveStorage({ get: () => null } as never)).toBeDefined();
  });
});

describe("flags", () => {
  it("read and write the two localStorage flags", () => {
    expect(readFlag(IGNORE_FLAG)).toBe(false);
    writeFlag(IGNORE_FLAG, true);
    expect(localStorage.getItem("twillingate_ignore")).toBe("true");
    expect(readFlag(IGNORE_FLAG)).toBe(true);
    writeFlag(IGNORE_FLAG, false);
    expect(localStorage.getItem("twillingate_ignore")).toBeNull();
    localStorage.setItem(DEBUG_FLAG, "true");
    expect(readFlag(DEBUG_FLAG)).toBe(true);
  });

  it("cookieDriver is host-only, one year, SameSite=Lax", () => {
    const set = vi.spyOn(document, "cookie", "set");
    cookieDriver().set("a_user", "u");
    const written = set.mock.calls[0][0] as string;
    expect(written).toContain("a_user=u");
    expect(written).toContain("path=/");
    expect(written).toContain("max-age=31536000");
    expect(written).toContain("SameSite=Lax");
    expect(written).not.toContain("domain=");
    expect(written).toContain("Secure"); // jsdom url is https
  });
});
