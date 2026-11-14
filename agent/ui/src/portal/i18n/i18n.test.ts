import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import en from "./en.json";
import es from "./es.json";
import { resolveLang, translate } from "./i18n";
import { MANAGE_FILES } from "./manage";

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

describe("Manage copy (dashboard.md section 9)", () => {
  const spec = readFileSync(
    resolve(__dirname, "../../../../../docs/specs/dashboard.md"),
    "utf8",
  );
  const section = spec.slice(spec.indexOf("## 9. Copy"), spec.indexOf("## 10."));
  const unescape = (c: string) => c.replace(/\\\|/g, "|");

  it("each file pair has the same keys, placeholders and no empty value", () => {
    for (const [name, e, s] of MANAGE_FILES) {
      expect(Object.keys(s).sort(), name).toEqual(Object.keys(e).sort());
      for (const k of Object.keys(e)) {
        expect(placeholders(s[k]), k).toEqual(placeholders(e[k]));
        expect(e[k].trim(), k).not.toBe("");
        expect(s[k].trim(), k).not.toBe("");
      }
    }
  });

  it("no key is defined twice (across the files and the Portal's own table)", () => {
    const seen = new Set(Object.keys(en));
    for (const [name, e] of MANAGE_FILES)
      for (const k of Object.keys(e)) {
        expect(seen.has(k), `${name}: ${k}`).toBe(false);
        seen.add(k);
      }
  });

  it("are exactly the tables of the spec, file by file", () => {
    for (const [name, e, s] of MANAGE_FILES) {
      const start = section.indexOf(`<!-- copy:${name} -->`);
      expect(start, `dashboard.md section 9 has no <!-- copy:${name} --> table`).toBeGreaterThan(-1);
      const rest = section.slice(start);
      const end = rest.indexOf("<!-- /copy -->");
      const rows = [
        ...rest.slice(0, end).matchAll(/^\| `([^`]+)` \| (.*) \| (.*) \|$/gm),
      ];
      expect(rows.map((r) => r[1]).sort(), name).toEqual(Object.keys(e).sort());
      for (const [, key, en1, es1] of rows) {
        expect(e[key], key).toBe(unescape(en1));
        expect(s[key], key).toBe(unescape(es1));
      }
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
