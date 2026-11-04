import { layers, namedFlavor } from "@protomaps/basemaps";
import type { StyleSpecification, LayerSpecification } from "maplibre-gl";
import type { MapEntry } from "./catalog";

// The runtime style (companion-services.md section 4.4.3). Every URL is built from the page origin
// (MapLibre needs absolute URLs), so nothing points at another host.

export type UiLang = "en" | "es";

export const ATTRIBUTION = "© OpenStreetMap contributors, Protomaps";
export const GLYPHS_PATH = "/maps/assets/fonts/{fontstack}/{range}.pbf";
export const SPRITE_PATH = "/maps/assets/sprites/v4/light";

/** stripSymbols removes every symbol layer (text and icons): the no-assets fallback. */
export function stripSymbols<T extends { type: string }>(
  list: readonly T[],
): T[] {
  return list.filter((l) => l.type !== "symbol");
}

export interface StyleOptions {
  origin: string;
  map: Pick<MapEntry, "url">;
  lang: UiLang;
  assetsAvailable: boolean;
}

export function buildStyle(o: StyleOptions): StyleSpecification {
  const all = layers("protomaps", namedFlavor("light"), {
    lang: o.lang,
  }) as LayerSpecification[];
  const style: StyleSpecification = {
    version: 8,
    sources: {
      protomaps: {
        type: "vector",
        url: `pmtiles://${o.origin}${o.map.url}`,
        attribution: ATTRIBUTION,
      },
    },
    layers: o.assetsAvailable ? all : stripSymbols(all),
  };
  if (o.assetsAvailable) {
    style.glyphs = `${o.origin}${GLYPHS_PATH}`;
    style.sprite = `${o.origin}${SPRITE_PATH}`;
  }
  return style;
}
