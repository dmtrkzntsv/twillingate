// The collector origin is baked into the served file. A build whose
// placeholder was never substituted (this test file: no vi.mock) has none.
import { describe, expect, it } from "vitest";
import { ORIGIN, collectorOrigin } from "./origin";

describe("collectorOrigin", () => {
  it("is null while the placeholder is unsubstituted", () => {
    expect(ORIGIN).toBe("__TWILLINGATE_URL__");
    expect(collectorOrigin()).toBeNull();
  });
});
