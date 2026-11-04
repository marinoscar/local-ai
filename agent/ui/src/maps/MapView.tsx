import maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import { Protocol } from "pmtiles";
import { useEffect, useRef } from "react";
import type { MapEntry } from "./catalog";
import type { Lang } from "./lang";
import { t, type Key } from "./lang";
import type { Place } from "./places";
import { buildStyle } from "./style";

// The map itself. MapLibre runs with a style assembled from this page's own origin (style.ts);
// pmtiles:// is the only protocol added, the RTL text plugin and the worker URL are left alone, so
// nothing is requested from another host (companion-services.md section 4.4.3).

let protocolAdded = false;
function ensureProtocol(): void {
  if (protocolAdded) return;
  const protocol = new Protocol();
  maplibregl.addProtocol("pmtiles", protocol.tile);
  protocolAdded = true;
}

export interface View {
  lat: number;
  lon: number;
  zoom: number;
}

/** MapApi is what the rest of the page can ask of the map. */
export interface MapApi {
  flyTo(lat: number, lon: number, zoom?: number): void;
  zoomBy(delta: number): void;
  center(): View;
}

interface Props {
  map: MapEntry;
  lang: Lang;
  assetsAvailable: boolean;
  initial: View;
  pin: { lat: number; lon: number } | null;
  places: Place[];
  onMove: (v: View) => void;
  onTap: (lat: number, lon: number) => void;
  apiRef: { current: MapApi | null };
}

function pinElement(): HTMLElement {
  const el = document.createElement("div");
  el.className = "pin";
  el.setAttribute("role", "img");
  el.setAttribute("aria-label", t("iAmHere"));
  return el;
}

function placeElement(p: Place): HTMLElement {
  const el = document.createElement("div");
  el.className = "place";
  const dot = document.createElement("span");
  dot.className = "place-dot";
  const label = document.createElement("span");
  label.className = "place-label";
  const kind = `kind.${p.kind}` as Key;
  label.textContent = p.name;
  el.title = `${p.name} (${t(kind)})`;
  el.append(dot, label);
  return el;
}

export function MapView(props: Props) {
  const { map: entry, lang, assetsAvailable, apiRef } = props;
  const host = useRef<HTMLDivElement>(null);
  const instance = useRef<maplibregl.Map | null>(null);
  const pinMarker = useRef<maplibregl.Marker | null>(null);
  const placeMarkers = useRef<maplibregl.Marker[]>([]);
  const cb = useRef({ onMove: props.onMove, onTap: props.onTap });
  cb.current = { onMove: props.onMove, onTap: props.onTap };

  // Create the map once.
  useEffect(() => {
    ensureProtocol();
    const m = new maplibregl.Map({
      container: host.current as HTMLDivElement,
      style: buildStyle({
        origin: window.location.origin,
        map: entry,
        lang,
        assetsAvailable,
      }),
      center: [props.initial.lon, props.initial.lat],
      zoom: props.initial.zoom,
      minZoom: entry.minzoom,
      maxZoom: Math.max(entry.maxzoom, 14) + 2,
      attributionControl: { compact: false },
      canvasContextAttributes: { preserveDrawingBuffer: true },
    });
    m.addControl(new maplibregl.ScaleControl({ unit: "metric" }), "bottom-left");
    const report = () => {
      const c = m.getCenter();
      cb.current.onMove({ lat: c.lat, lon: c.lng, zoom: m.getZoom() });
    };
    m.on("move", report);
    m.on("load", report);
    m.on("click", (e) => cb.current.onTap(e.lngLat.lat, e.lngLat.lng));
    m.on("contextmenu", (e) => cb.current.onTap(e.lngLat.lat, e.lngLat.lng));
    instance.current = m;
    apiRef.current = {
      flyTo: (lat, lon, zoom) =>
        m.flyTo({
          center: [lon, lat],
          zoom: zoom ?? Math.max(m.getZoom(), 13),
          essential: true,
        }),
      zoomBy: (d) => m.easeTo({ zoom: m.getZoom() + d, duration: 150 }),
      center: () => {
        const c = m.getCenter();
        return { lat: c.lat, lon: c.lng, zoom: m.getZoom() };
      },
    };
    return () => {
      apiRef.current = null;
      m.remove();
      instance.current = null;
    };
  }, []);

  // New map file, language or label availability: swap the style, keep the view.
  useEffect(() => {
    const m = instance.current;
    if (!m) return;
    m.setStyle(
      buildStyle({
        origin: window.location.origin,
        map: entry,
        lang,
        assetsAvailable,
      }),
    );
    m.setMinZoom(entry.minzoom);
  }, [entry, lang, assetsAvailable]);

  // The "I am here" pin.
  useEffect(() => {
    const m = instance.current;
    if (!m) return;
    pinMarker.current?.remove();
    pinMarker.current = null;
    if (props.pin) {
      pinMarker.current = new maplibregl.Marker({ element: pinElement() })
        .setLngLat([props.pin.lon, props.pin.lat])
        .addTo(m);
    }
  }, [props.pin, lang]);

  // Search results and nearest places: plain dots with an HTML label (also the no-labels fallback).
  useEffect(() => {
    const m = instance.current;
    if (!m) return;
    for (const mk of placeMarkers.current) mk.remove();
    placeMarkers.current = props.places.map((p) =>
      new maplibregl.Marker({ element: placeElement(p) })
        .setLngLat([p.lon, p.lat])
        .addTo(m),
    );
  }, [props.places, lang]);

  return (
    <div
      ref={host}
      className="map-host"
      role="application"
      aria-label={t("title")}
    />
  );
}
