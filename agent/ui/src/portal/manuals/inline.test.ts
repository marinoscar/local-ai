import { describe, expect, it } from "vitest";
import en from "../i18n/en.json";
import guideEn from "../i18n/manage/guide.en.json";
import guideEs from "../i18n/manage/guide.es.json";
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
  it("handles every help and guide string in both languages with no mark left over", () => {
    for (const table of [
      { ...en, ...guideEn },
      { ...es, ...guideEs },
    ] as Record<string, string>[]) {
      const keys = Object.keys(table).filter((k) => k === "help.codes" || k.startsWith("guide."));
      expect(keys.length).toBeGreaterThan(40);
      for (const key of keys) {
        for (const s of parseInline(table[key])) expect(s.text, key).not.toMatch(/[*`]/);
      }
    }
  });
});
