// Coordinate helpers of the map viewer (companion-services.md section 4.4.3 and 4.4.5): decimal and
// DMS formatting, parsing of what a person types, and bounds checks. Pure functions, unit tested.

export type Bounds = [west: number, south: number, east: number, north: number];

export function validLatLon(lat: number, lon: number): boolean {
  return (
    Number.isFinite(lat) &&
    Number.isFinite(lon) &&
    lat >= -90 &&
    lat <= 90 &&
    lon >= -180 &&
    lon <= 180
  );
}

/** formatDecimal is "9.93241, -84.07912" (5 decimals is about one metre). */
export function formatDecimal(lat: number, lon: number): string {
  return `${lat.toFixed(5)}, ${lon.toFixed(5)}`;
}

function dmsPart(value: number, pos: string, neg: string): string {
  const hemi = value < 0 ? neg : pos;
  let abs = Math.abs(value);
  let deg = Math.floor(abs);
  abs = (abs - deg) * 60;
  let min = Math.floor(abs);
  let sec = Math.round((abs - min) * 60);
  if (sec === 60) {
    sec = 0;
    min += 1;
  }
  if (min === 60) {
    min = 0;
    deg += 1;
  }
  return `${deg}°${min}'${sec}"${hemi}`;
}

/** toDMS is `9°55'48"N 84°4'48"W`. */
export function toDMS(lat: number, lon: number): string {
  return `${dmsPart(lat, "N", "S")} ${dmsPart(lon, "E", "W")}`;
}

const DECIMAL = /^\s*(-?\d+(?:\.\d+)?)\s*[,;\s]\s*(-?\d+(?:\.\d+)?)\s*$/;
const DMS_PART =
  /(\d+(?:\.\d+)?)\s*°\s*(?:(\d+(?:\.\d+)?)\s*['′]\s*)?(?:(\d+(?:\.\d+)?)\s*(?:"|″|'')\s*)?([NSEWnsew])/g;

/**
 * parseCoordinates reads decimal degrees ("9.93, -84.08") or DMS with hemisphere letters
 * (`9°55'48"N 84°4'48"W`). It returns null for anything else or for values out of range.
 */
export function parseCoordinates(
  text: string,
): { lat: number; lon: number } | null {
  const d = DECIMAL.exec(text);
  if (d) {
    const lat = Number(d[1]);
    const lon = Number(d[2]);
    return validLatLon(lat, lon) ? { lat, lon } : null;
  }
  let lat: number | null = null;
  let lon: number | null = null;
  let parts = 0;
  for (const m of text.matchAll(DMS_PART)) {
    parts++;
    const deg = Number(m[1]);
    const min = m[2] ? Number(m[2]) : 0;
    const sec = m[3] ? Number(m[3]) : 0;
    if (min >= 60 || sec >= 60) return null;
    const h = m[4].toUpperCase();
    let v = deg + min / 60 + sec / 3600;
    if (h === "S" || h === "W") v = -v;
    if (h === "N" || h === "S") {
      if (lat !== null) return null;
      lat = v;
    } else {
      if (lon !== null) return null;
      lon = v;
    }
  }
  if (parts !== 2 || lat === null || lon === null) return null;
  return validLatLon(lat, lon) ? { lat, lon } : null;
}

/** inBounds: is the point inside [west, south, east, north]? */
export function inBounds(lat: number, lon: number, b: Bounds): boolean {
  return lon >= b[0] && lon <= b[2] && lat >= b[1] && lat <= b[3];
}

export interface HasBounds {
  id: string;
  bounds: Bounds;
}

/** containingMap is the first map (other than `exceptId`) whose bounds contain the point. */
export function containingMap<T extends HasBounds>(
  maps: readonly T[],
  lat: number,
  lon: number,
  exceptId?: string,
): T | undefined {
  return maps.find((m) => m.id !== exceptId && inBounds(lat, lon, m.bounds));
}
