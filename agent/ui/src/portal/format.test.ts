import { describe, expect, it } from "vitest";
import { clockText, durationText } from "./format";
import { translate } from "./i18n/i18n";

describe("durationText", () => {
  it("formats minutes as hours and minutes (English)", () => {
    expect(durationText(145)).toBe("2 h 25 min");
    expect(durationText(45)).toBe("45 min");
    expect(durationText(60)).toBe("1 h 0 min");
    expect(durationText(-5)).toBe("0 min");
  });
  it("uses the same shape in Spanish", () => {
    expect(translate("es", "time.hm", { h: 2, m: 25 })).toBe("2 h 25 min");
    expect(translate("es", "time.m", { m: 45 })).toBe("45 min");
  });
});

describe("clockText", () => {
  it("shows the date in the language asked", () => {
    const iso = "2026-11-02T14:05:11Z";
    expect(clockText(iso, "en")).toContain("2026");
    expect(clockText(iso, "es")).toContain("2026");
    expect(clockText(iso, "en")).not.toBe(clockText(iso, "es"));
  });
  it("is empty for a bad date", () => {
    expect(clockText("not a date", "en")).toBe("");
  });
});
