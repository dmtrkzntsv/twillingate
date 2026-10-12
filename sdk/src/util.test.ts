import { describe, expect, it } from "vitest";
import { maskIds, uuid7, withQuery } from "./util";

describe("maskIds", () => {
  it("masks uuid segments", () => {
    expect(maskIds("/account/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31/edit"))
      .toBe("/account/[id]/edit");
  });

  it("masks uppercase uuids", () => {
    expect(maskIds("/u/3F8A91C2-4B7E-4D1A-9F2C-8E6B5A0D7C31")).toBe("/u/[id]");
  });

  it("masks ulid segments", () => {
    expect(maskIds("/o/01ARZ3NDEKTSV4RRFFQ69G5FAV")).toBe("/o/[id]");
  });

  // /2024/annual-report is a real path; masking digits by default would
  // silently mangle it, so numeric is opt-in.
  it("leaves numeric segments alone by default", () => {
    expect(maskIds("/2024/annual-report")).toBe("/2024/annual-report");
  });

  it("masks numeric segments when asked", () => {
    expect(maskIds("/account/8812/edit", { numeric: true })).toBe("/account/[id]/edit");
  });

  it("masks long hex blobs when asked", () => {
    expect(maskIds("/o/507f1f77bcf86cd799439011", { hex: true })).toBe("/o/[id]");
  });

  // The host must never be treated as a segment.
  it("leaves the host alone in an absolute url", () => {
    expect(maskIds("https://shop.example.com/account/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31"))
      .toBe("https://shop.example.com/account/[id]");
  });

  it("does not mask a host that looks id-shaped", () => {
    expect(maskIds("https://8812.example.com/a", { numeric: true }))
      .toBe("https://8812.example.com/a");
  });

  it("masks hash segments so hash routes are covered", () => {
    expect(maskIds("https://a.example.com/app/#/account/3f8a91c2-4b7e-4d1a-9f2c-8e6b5a0d7c31/edit"))
      .toBe("https://a.example.com/app/#/account/[id]/edit");
  });

  it("preserves the query string", () => {
    expect(maskIds("https://a.example.com/x?utm_source=news"))
      .toBe("https://a.example.com/x?utm_source=news");
  });

  it("handles a scheme and host with no path", () => {
    expect(maskIds("https://a.example.com")).toBe("https://a.example.com");
  });
});

describe("withQuery", () => {
  it("appends only allowlisted params", () => {
    expect(withQuery("/settings", "https://a.example.com/settings?tab=billing&secret=1", ["tab"]))
      .toBe("/settings?tab=billing");
  });

  // Sorting is the whole reason this ships as a helper: unsorted params
  // would put ?a=1&b=2 and ?b=2&a=1 in two different rows.
  it("sorts params by key", () => {
    expect(withQuery("/s", "https://a.example.com/s?b=2&a=1", ["a", "b"]))
      .toBe("/s?a=1&b=2");
  });

  it("returns the path unchanged when nothing matches", () => {
    expect(withQuery("/s", "https://a.example.com/s?x=1", ["tab"])).toBe("/s");
  });

  it("escapes values", () => {
    expect(withQuery("/s", "https://a.example.com/s?tab=a%20b", ["tab"]))
      .toBe("/s?tab=a%20b");
  });
});

describe("uuid7", () => {
  const V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

  it("is a version 7, variant 1 UUID", () => {
    expect(uuid7()).toMatch(V7);
  });

  it("carries the millisecond it was made in its first 48 bits", () => {
    const id = uuid7(0x018f1e5a1234);
    expect(id.slice(0, 13)).toBe("018f1e5a-1234");
  });

  it("sorts by time, and is unique within a millisecond", () => {
    const a = uuid7(1_700_000_000_000);
    const b = uuid7(1_700_000_000_001);
    expect(a < b).toBe(true);
    const same = new Set(Array.from({ length: 1000 }, () => uuid7(1_700_000_000_000)));
    expect(same.size).toBe(1000);
  });
});
