// The maps side of the fake agent (companion-services.md section 4.4): the viewer page and bundle,
// /maps/catalog.json, a tiny generated PMTiles file with Range support, and the place routes. Real
// behaviour is covered by the Go tests (agent/internal/maps, agent/internal/api/spa_test.go).
import { readFile } from "node:fs/promises";
import { dirname, extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const dist = join(here, "..", "dist");

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript",
  ".css": "text/css",
  ".json": "application/json",
};

// --- A valid PMTiles v3 archive with one z0 tile holding an "earth" polygon (the whole tile). ---

function varint(n) {
  const out = [];
  let v = BigInt(n);
  while (v >= 0x80n) {
    out.push(Number(v & 0x7fn) | 0x80);
    v >>= 7n;
  }
  out.push(Number(v));
  return Buffer.from(out);
}
const zz = (n) => (n << 1) ^ (n >> 31);
const field = (num, wire, payload) =>
  Buffer.concat([varint((num << 3) | wire), payload]);
const lenDelimited = (num, buf) =>
  field(num, 2, Buffer.concat([varint(buf.length), buf]));

function mvtTile() {
  const geom = Buffer.concat(
    [9, 0, 0, 26, zz(4096), 0, zz(0), zz(4096), zz(-4096), 0, 15].map((n) =>
      varint(n),
    ),
  );
  const feature = Buffer.concat([
    field(3, 0, varint(3)),
    lenDelimited(4, geom),
  ]);
  const layer = Buffer.concat([
    lenDelimited(1, Buffer.from("earth")),
    lenDelimited(2, feature),
    field(5, 0, varint(4096)),
    field(15, 0, varint(2)),
  ]);
  return lenDelimited(3, layer);
}

export function buildPmtiles() {
  const tile = mvtTile();
  const dir = Buffer.concat([
    varint(1), // entries
    varint(0), // tile id delta
    varint(1), // run length
    varint(tile.length),
    varint(1), // offset + 1: the first entry starts at 0
  ]);
  const meta = Buffer.from("{}");
  const h = Buffer.alloc(127);
  h.write("PMTiles", 0, "latin1");
  h.writeUInt8(3, 7);
  const dirOff = 127;
  const metaOff = dirOff + dir.length;
  const dataOff = metaOff + meta.length;
  h.writeBigUInt64LE(BigInt(dirOff), 8);
  h.writeBigUInt64LE(BigInt(dir.length), 16);
  h.writeBigUInt64LE(BigInt(metaOff), 24);
  h.writeBigUInt64LE(BigInt(meta.length), 32);
  h.writeBigUInt64LE(BigInt(dataOff + tile.length), 40); // leaf dirs: none
  h.writeBigUInt64LE(0n, 48);
  h.writeBigUInt64LE(BigInt(dataOff), 56);
  h.writeBigUInt64LE(BigInt(tile.length), 64);
  h.writeBigUInt64LE(1n, 72);
  h.writeBigUInt64LE(1n, 80);
  h.writeBigUInt64LE(1n, 88);
  h.writeUInt8(1, 96); // clustered
  h.writeUInt8(1, 97); // internal compression: none
  h.writeUInt8(1, 98); // tile compression: none
  h.writeUInt8(1, 99); // tile type: mvt
  h.writeUInt8(0, 100);
  h.writeUInt8(0, 101);
  h.writeInt32LE(-1800000000, 102);
  h.writeInt32LE(-850000000, 106);
  h.writeInt32LE(1800000000, 110);
  h.writeInt32LE(850000000, 114);
  h.writeUInt8(0, 118);
  h.writeInt32LE(0, 119);
  h.writeInt32LE(0, 123);
  return Buffer.concat([h, dir, meta, tile]);
}

const PMTILES = buildPmtiles();

function entry(id, name, bounds, center) {
  return {
    id,
    display_name: name,
    display_name_es: `${name} (es)`,
    url: `/maps/tiles/${id}.pmtiles`,
    version: "20261015",
    bounds,
    minzoom: 0,
    maxzoom: 15,
    center,
    default: false,
    root: "internal",
  };
}

function defaultCatalog() {
  const a = entry("map-a", "Map A", [-10, -10, 0, 0], [-5, -5, 4]);
  a.default = true;
  const b = entry("map-b", "Map B", [0.5, 0.5, 10, 10], [5, 5, 4]);
  return {
    maps: [a, b],
    assets: {
      available: true,
      glyphs: "/maps/assets/fonts/{fontstack}/{range}.pbf",
      sprite: "/maps/assets/sprites/v4/light",
      fonts: ["Noto Sans Regular"],
    },
    search: { available: true, places: 3, source: "places-fixture" },
    gps: { available: false, source: null },
  };
}

let catalog = defaultCatalog();
let position = { source: "none", lat: null, lon: null };

const PLACES = [
  { name: "Fixture Hospital", kind: "hospital", lat: -4.5, lon: -5.5, admin1: "Region One", country: "XX" },
  { name: "Fixture Clinic", kind: "clinic", lat: -5.2, lon: -4.8, admin1: "Region One", country: "XX" },
  { name: "Fixture Town", kind: "town", lat: 5.1, lon: 5.2, admin1: "Region Two", country: "XX" },
];

function json(res, code, body, headers = {}) {
  res.writeHead(code, { "Content-Type": TYPES[".json"], ...headers });
  res.end(JSON.stringify(body));
}

async function file(res, path, type, extra = {}) {
  try {
    res.writeHead(200, {
      "Content-Type": type,
      "X-Content-Type-Options": "nosniff",
      ...extra,
    });
    res.end(await readFile(path));
  } catch {
    res.writeHead(404);
    res.end("not found");
  }
}

function serveRange(req, res) {
  const range = /^bytes=(\d+)-(\d*)$/.exec(req.headers.range ?? "");
  const headers = {
    "Content-Type": "application/vnd.pmtiles",
    "Accept-Ranges": "bytes",
    "Cache-Control": "no-cache",
  };
  if (!range) {
    res.writeHead(200, headers);
    return res.end(PMTILES);
  }
  const start = Number(range[1]);
  const end = Math.min(range[2] ? Number(range[2]) : PMTILES.length - 1, PMTILES.length - 1);
  res.writeHead(206, {
    ...headers,
    "Content-Range": `bytes ${start}-${end}/${PMTILES.length}`,
  });
  res.end(PMTILES.subarray(start, end + 1));
}

async function readBody(req) {
  let s = "";
  for await (const c of req) s += c;
  return s;
}

const near = (a, b) => Math.hypot(a.lat - b.lat, a.lon - b.lon);

/**
 * handleMaps answers /maps/* and the /__maps test hook. It returns true when it handled the request.
 * csp is the Portal CSP the real agent sends with the viewer page.
 */
export async function handleMaps(req, res, url, csp) {
  const p = url.pathname;
  if (p === "/__maps") {
    // POST {catalog?, position?} changes the fixture; POST {} resets it.
    const body = JSON.parse((await readBody(req)) || "{}");
    catalog = body.catalog ?? defaultCatalog();
    position = body.position ?? { source: "none", lat: null, lon: null };
    res.writeHead(200);
    res.end("ok");
    return true;
  }
  if (!p.startsWith("/maps/")) return false;
  if (p === "/maps/") {
    await file(res, join(dist, "maps.html"), TYPES[".html"], {
      "Content-Security-Policy": csp,
    });
    return true;
  }
  if (p.startsWith("/maps/viewer/")) {
    const rel = normalize(p.slice("/maps/".length));
    if (rel.includes("..")) {
      res.writeHead(400);
      res.end("bad");
      return true;
    }
    await file(res, join(dist, rel), TYPES[extname(rel)] ?? "application/octet-stream");
    return true;
  }
  if (p === "/maps/catalog.json") {
    json(res, 200, catalog, { "Cache-Control": "no-cache" });
    return true;
  }
  if (p.startsWith("/maps/tiles/")) {
    serveRange(req, res);
    return true;
  }
  if (p === "/maps/whereami") {
    if (req.method === "PUT") {
      const b = JSON.parse(await readBody(req));
      position = { source: "manual", lat: b.lat, lon: b.lon, label: null };
    }
    if (req.method === "DELETE") position = { source: "none", lat: null, lon: null };
    json(res, 200, position);
    return true;
  }
  if (p === "/maps/search") {
    const q = (url.searchParams.get("q") ?? "").toLowerCase();
    json(res, 200, {
      results: PLACES.filter((x) => x.name.toLowerCase().includes(q)),
      attribution: "GeoNames (CC BY 4.0)",
    });
    return true;
  }
  if (p === "/maps/nearest") {
    const at = { lat: Number(url.searchParams.get("lat")), lon: Number(url.searchParams.get("lon")) };
    const kind = url.searchParams.get("kind");
    const results = PLACES.filter((x) => x.kind === kind)
      .sort((a, b) => near(a, at) - near(b, at))
      .map((x) => ({ ...x, distance_km: Math.round(near(x, at) * 111 * 10) / 10 }));
    json(res, 200, { results, attribution: "GeoNames (CC BY 4.0)" });
    return true;
  }
  // Fonts and sprites are not part of the fixture (assets.available true only makes the style ask).
  res.writeHead(404, { "Content-Type": TYPES[".json"] });
  res.end(JSON.stringify({ error: { code: "LAI-SVC-031" } }));
  return true;
}
