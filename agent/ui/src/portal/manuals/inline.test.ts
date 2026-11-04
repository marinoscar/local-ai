import { describe, expect, it } from "vitest";
import en from "../i18n/en.json";
import es from "../i18n/es.json";
import { parseInline } from "./inline";

describe("parseInline", () => {
  it("splits bold, italic and code", () => {
    expect(parseInline("a **b** c `d` e *f*")).toEqual([
      { kind: "text", text: "a " },
      { kind: "strong", text: "b" },
      { kind: "text", text: " c " },
      { kind: "code", text: "d" },
      { kind: "text", text: " e " },
      { kind: "em", text: "f" },
    ]);
  });
  it("leaves plain text alone", () => {
    expect(parseInline("plain")).toEqual([{ kind: "text", text: "plain" }]);
  });
  it("handles every help string in both languages with no mark left over", () => {
    for (const table of [en, es] as Record<string, string>[]) {
      for (const key of ["help.survival", "help.drive", "help.library", "help.codes"]) {
        const segs = parseInline(table[key]);
        expect(segs.some((s) => s.kind !== "text"), key).toBe(true);
        for (const s of segs) expect(s.text, key).not.toMatch(/[*`]/);
      }
    }
  });
});
