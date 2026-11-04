import { getLang } from "./lang";

// Client of the place routes (companion-services.md sections 4.4.4 and 4.4.5). Same origin only.

export interface Place {
  name: string;
  kind: string;
  lat: number;
  lon: number;
  admin1?: string;
  country?: string;
  distance_km?: number;
}

export type SearchResult =
  | { state: "ok"; results: Place[] }
  | { state: "building" }
  | { state: "missing" }
  | { state: "error" };

export interface Position {
  source: "gps" | "manual" | "none";
  lat: number | null;
  lon: number | null;
  accuracy_m?: number | null;
  label?: string | null;
  code?: string;
}

async function getPlaces(
  path: string,
  params: Record<string, string>,
  fetcher: typeof fetch,
): Promise<SearchResult> {
  try {
    const res = await fetcher(`${path}?${new URLSearchParams(params)}`);
    if (res.status === 503) return { state: "building" };
    if (res.status === 404) return { state: "missing" };
    if (!res.ok) return { state: "error" };
    const body = (await res.json()) as { results?: Place[] };
    return { state: "ok", results: body.results ?? [] };
  } catch {
    return { state: "error" };
  }
}

export function searchPlaces(
  q: string,
  near: { lat: number; lon: number } | null,
  fetcher: typeof fetch = fetch,
): Promise<SearchResult> {
  const p: Record<string, string> = { q, limit: "10", lang: getLang() };
  if (near) p.near = `${near.lat.toFixed(5)},${near.lon.toFixed(5)}`;
  return getPlaces("/maps/search", p, fetcher);
}

export function nearestPlaces(
  kind: string,
  at: { lat: number; lon: number },
  fetcher: typeof fetch = fetch,
): Promise<SearchResult> {
  return getPlaces(
    "/maps/nearest",
    { kind, lat: at.lat.toFixed(5), lon: at.lon.toFixed(5), limit: "5" },
    fetcher,
  );
}

export async function getPosition(
  fetcher: typeof fetch = fetch,
): Promise<Position | null> {
  try {
    const res = await fetcher("/maps/whereami", { cache: "no-store" });
    if (!res.ok) return null;
    return (await res.json()) as Position;
  } catch {
    return null;
  }
}

/** putPosition stores the manual location (PUT /maps/whereami). True when the agent accepted it. */
export async function putPosition(
  lat: number,
  lon: number,
  fetcher: typeof fetch = fetch,
): Promise<boolean> {
  try {
    const res = await fetcher("/maps/whereami", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ lat, lon }),
    });
    return res.ok;
  } catch {
    return false;
  }
}
