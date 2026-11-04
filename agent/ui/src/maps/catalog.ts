import type { Bounds } from "./coords";

// GET /maps/catalog.json (companion-services.md section 4.4.2).

export interface MapEntry {
  id: string;
  display_name: string;
  display_name_es: string;
  url: string;
  version: string;
  bounds: Bounds;
  minzoom: number;
  maxzoom: number;
  center: [lon: number, lat: number, zoom: number];
  default: boolean;
  root: string;
}

export interface Catalog {
  maps: MapEntry[];
  assets: {
    available: boolean;
    glyphs?: string;
    sprite?: string;
    fonts?: string[];
  };
  search: { available: boolean; places?: number; source?: string | null };
  gps: { available: boolean; source: string | null };
}

export const CATALOG_PATH = "/maps/catalog.json";

export async function loadCatalog(
  fetcher: typeof fetch = fetch,
): Promise<Catalog> {
  const res = await fetcher(CATALOG_PATH, { cache: "no-store" });
  if (!res.ok) throw new Error(`catalog ${res.status}`);
  const c = (await res.json()) as Partial<Catalog>;
  return {
    maps: c.maps ?? [],
    assets: c.assets ?? { available: false },
    search: c.search ?? { available: false },
    gps: c.gps ?? { available: false, source: null },
  };
}

/** defaultMap is the map flagged default, else the first. */
export function defaultMap(maps: readonly MapEntry[]): MapEntry | undefined {
  return maps.find((m) => m.default) ?? maps[0];
}

export function mapName(m: MapEntry, lang: "en" | "es"): string {
  return lang === "es" && m.display_name_es ? m.display_name_es : m.display_name;
}
