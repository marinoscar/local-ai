import { describe, expect, it } from "vitest";
import { brainChoices, speedOf } from "./brains";
import type { BrainChoice } from "./status";

const c = (
  id: string,
  name: string,
  o: Partial<BrainChoice> = {},
): BrainChoice => ({
  id,
  display_name: name,
  summary: "",
  loadable: true,
  survival: false,
  ...o,
});

describe("brainChoices", () => {
  it("keeps only loadable models, survival first, then by name", () => {
    const out = brainChoices([
      c("z", "Zeta"),
      c("n", "Hidden", { loadable: false, survival: true }),
      c("b", "Beta", { survival: true }),
      c("a", "Alpha"),
      c("m", "Aardvark", { survival: true }),
    ]);
    expect(out.map((x) => x.id)).toEqual(["m", "b", "a", "z"]);
  });
  it("handles a missing list", () => {
    expect(brainChoices(undefined)).toEqual([]);
  });
});

describe("speedOf", () => {
  const alert = { code: "LAI-SVC-113", severity: "info", title: "", fix: "" };
  it("uses the hint, or slow on a node without a graphics card", () => {
    expect(speedOf(c("a", "A", { speed_hint: "fast" }), [])).toBe("fast");
    expect(speedOf(c("a", "A", { speed_hint: "fast" }), [alert])).toBe("slow");
    expect(speedOf(c("a", "A"), [])).toBeNull();
  });
});
