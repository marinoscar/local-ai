import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import en from "./en.json";
import es from "./es.json";
import { resolveLang, translate } from "./i18n";

const placeholders = (s: string) =>
  [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();

describe("translation files", () => {
  it("have the same keys", () => {
    expect(Object.keys(es).sort()).toEqual(Object.keys(en).sort());
  });

  it("use the same placeholders in both languages", () => {
    for (const k of Object.keys(en) as (keyof typeof en)[]) {
      expect(placeholders(es[k]), k).toEqual(placeholders(en[k]));
    }
  });

  it("have no empty value", () => {
    for (const [k, v] of [...Object.entries(en), ...Object.entries(es)])
      expect(v.trim(), k).not.toBe("");
  });

  it("are exactly the table of survival-portal.md section 4.9 (same keys, same text)", () => {
    const spec = readFileSync(
      resolve(__dirname, "../../../../../docs/specs/survival-portal.md"),
      "utf8",
    );
    const section = spec.slice(
      spec.indexOf("### 4.9 Copy"),
      spec.indexOf("### 4.10"),
    );
    const rows = [...section.matchAll(/^\| `([^`]+)` \| (.*) \| (.*) \|$/gm)];
    expect(rows.length).toBeGreaterThan(100);
    expect(Object.keys(en).sort()).toEqual(rows.map((r) => r[1]).sort());
    for (const [, key, e, s] of rows) {
      expect(en[key as keyof typeof en], key).toBe(e);
      expect(es[key as keyof typeof es], key).toBe(s);
    }
  });
});

describe("translate", () => {
  it("substitutes placeholders", () => {
    expect(translate("en", "status.brain", { name: "Qwen" })).toBe(
      "Brain: Qwen",
    );
    expect(translate("es", "banner.batteryLow", { pct: 15 })).toBe(
      "Batería baja (15 %). Conecte el cargador cuando pueda.",
    );
  });
  it("keeps an unknown placeholder visible and an unknown key as itself", () => {
    expect(translate("en", "status.brain")).toBe("Brain: {name}");
    expect(translate("en", "no.such.key")).toBe("no.such.key");
  });
});

describe("resolveLang", () => {
  it("prefers the stored choice, then the agent, then the browser, then English", () => {
    expect(
      resolveLang({ stored: "es", status: "en", navigator: "en-US" }),
    ).toBe("es");
    expect(
      resolveLang({ stored: null, status: "es", navigator: "en-US" }),
    ).toBe("es");
    expect(
      resolveLang({ stored: "xx", status: null, navigator: "es-MX" }),
    ).toBe("es");
    expect(resolveLang({ navigator: "fr-FR" })).toBe("en");
    expect(resolveLang({})).toBe("en");
  });
});
