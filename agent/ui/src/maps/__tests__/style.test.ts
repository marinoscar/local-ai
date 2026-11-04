import { describe, expect, it } from "vitest";
import { buildStyle, stripSymbols } from "../style";
import { pickLang, translate } from "../lang";
import en from "../i18n/en.json";
import es from "../i18n/es.json";

const origin = "http://127.0.0.1:7070";
const map = { url: "/maps/tiles/osm-central-america.pmtiles" };

describe("buildStyle", () => {
  const style = buildStyle({ origin, map, lang: "en", assetsAvailable: true });

  it("builds every URL from the page origin", () => {
    expect(style.glyphs).toBe(`${origin}/maps/assets/fonts/{fontstack}/{range}.pbf`);
    expect(style.sprite).toBe(`${origin}/maps/assets/sprites/v4/light`);
    const src = style.sources.protomaps;
    expect(src).toMatchObject({
      type: "vector",
      url: `pmtiles://${origin}${map.url}`,
    });
  });

  it("mentions no host but the origin", () => {
    const hosts = [...JSON.stringify(style).matchAll(/https?:\/\/[^/"]+/g)].map(
      (m) => m[0],
    );
    expect(new Set(hosts)).toEqual(new Set([origin]));
  });

  it("keeps symbol layers when the assets exist", () => {
    expect(style.layers.some((l) => l.type === "symbol")).toBe(true);
  });

  it("changes the label language", () => {
    const es = buildStyle({ origin, map, lang: "es", assetsAvailable: true });
    expect(JSON.stringify(es.layers)).not.toBe(JSON.stringify(style.layers));
  });
});

describe("no-assets fallback", () => {
  const style = buildStyle({ origin, map, lang: "es", assetsAvailable: false });
  it("removes every symbol layer and the glyph and sprite URLs", () => {
    expect(style.layers.length).toBeGreaterThan(10);
    expect(style.layers.some((l) => l.type === "symbol")).toBe(false);
    expect(style.glyphs).toBeUndefined();
    expect(style.sprite).toBeUndefined();
  });
  it("stripSymbols only drops symbols", () => {
    const out = stripSymbols([{ type: "fill" }, { type: "symbol" }, { type: "line" }]);
    expect(out.map((l) => l.type)).toEqual(["fill", "line"]);
  });
});

describe("viewer strings", () => {
  it("have the same keys in both languages", () => {
    expect(Object.keys(es).sort()).toEqual(Object.keys(en).sort());
  });
  it("carry the spec banner text", () => {
    expect(translate("en", "banner.labels")).toBe(
      "Map labels are missing. Names of places can't be shown.",
    );
    expect(translate("es", "banner.labels")).toBe(
      "Faltan las etiquetas del mapa. No se pueden mostrar nombres.",
    );
    expect(translate("es", "search.notInstalled")).toBe(
      "La búsqueda de lugares no está instalada.",
    );
  });
  it("picks the language: query, stored, browser, English", () => {
    expect(pickLang("es", "en", "en-US")).toBe("es");
    expect(pickLang(null, "es", "en-US")).toBe("es");
    expect(pickLang(null, null, "es-MX")).toBe("es");
    expect(pickLang("fr", "de", "fr-FR")).toBe("en");
  });
});
