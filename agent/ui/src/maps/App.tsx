import { useCallback, useEffect, useRef, useState } from "react";
import {
  loadCatalog,
  defaultMap,
  type Catalog,
} from "./catalog";
import {
  Actions,
  Readout,
  ResultList,
  SearchBox,
  ZoomButtons,
  type NearestKind,
} from "./Controls";
import { containingMap, inBounds, parseCoordinates } from "./coords";
import { setLang, t, useLang, type Key } from "./lang";
import { MapView, type MapApi, type View } from "./MapView";
import { Picker } from "./Picker";
import {
  getPosition,
  nearestPlaces,
  putPosition,
  searchPlaces,
  type Place,
  type SearchResult,
} from "./places";

type Load =
  | { phase: "loading" }
  | { phase: "error" }
  | { phase: "ready"; catalog: Catalog; start: View };

interface Msg {
  key: Key;
}

function failureKey(r: SearchResult): Key | null {
  switch (r.state) {
    case "building":
      return "search.building";
    case "missing":
      return "search.notInstalled";
    case "error":
      return "search.error";
    default:
      return null;
  }
}

export function App() {
  const lang = useLang();
  const [load, setLoad] = useState<Load>({ phase: "loading" });
  const [mapId, setMapId] = useState("");
  const [view, setView] = useState<View | null>(null);
  const [pin, setPin] = useState<{ lat: number; lon: number } | null>(null);
  const [places, setPlaces] = useState<Place[]>([]);
  const [msg, setMsg] = useState<Msg | null>(null);
  const api = useRef<MapApi | null>(null);

  useEffect(() => {
    let live = true;
    void (async () => {
      try {
        const catalog = await loadCatalog();
        const map = defaultMap(catalog.maps);
        if (!live) return;
        if (!map) {
          setLoad({ phase: "ready", catalog, start: { lat: 0, lon: 0, zoom: 2 } });
          return;
        }
        const pos = await getPosition();
        let start: View = {
          lat: map.center[1],
          lon: map.center[0],
          zoom: map.center[2],
        };
        let chosen = map;
        if (pos && pos.lat !== null && pos.lon !== null) {
          const home = inBounds(pos.lat, pos.lon, map.bounds)
            ? map
            : containingMap(catalog.maps, pos.lat, pos.lon);
          if (home) {
            chosen = home;
            start = { lat: pos.lat, lon: pos.lon, zoom: 12 };
            setPin({ lat: pos.lat, lon: pos.lon });
          }
        }
        if (!live) return;
        setMapId(chosen.id);
        setView(start);
        setLoad({ phase: "ready", catalog, start });
      } catch {
        if (live) setLoad({ phase: "error" });
      }
    })();
    return () => {
      live = false;
    };
  }, []);

  // + and - zoom the map when the focus is not in a text field or on the map (the map handles its own).
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const el = e.target as HTMLElement | null;
      if (
        el &&
        (el.tagName === "INPUT" ||
          el.tagName === "SELECT" ||
          el.tagName === "TEXTAREA" ||
          el.closest(".map-host"))
      )
        return;
      if (e.key === "+" || e.key === "=") api.current?.zoomBy(1);
      else if (e.key === "-" || e.key === "_") api.current?.zoomBy(-1);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const goTo = useCallback((lat: number, lon: number, zoom?: number) => {
    api.current?.flyTo(lat, lon, zoom);
  }, []);

  if (load.phase === "loading")
    return (
      <p className="lead" role="status">
        {t("loading")}
      </p>
    );
  if (load.phase === "error")
    return (
      <div className="panel" role="alert">
        <p className="lead">{t("error.catalog")}</p>
      </div>
    );

  const { catalog } = load;
  const map = catalog.maps.find((m) => m.id === mapId);
  if (!map || !view)
    return (
      <div className="panel" role="alert">
        <p className="lead">{t("none.title")}</p>
        <p className="lead">{t("none.fix")}</p>
        <p className="code">{"LAI-SVC-030"}</p>
      </div>
    );

  const suggestion = inBounds(view.lat, view.lon, map.bounds)
    ? undefined
    : containingMap(catalog.maps, view.lat, view.lon, map.id);
  const searchOn = catalog.search.available;

  async function whereAmI() {
    const pos = await getPosition();
    if (!pos || pos.lat === null || pos.lon === null) {
      setMsg({ key: pos?.code === "LAI-SVC-033" ? "whereami.gpsSilent" : "whereami.none" });
      return;
    }
    setPin({ lat: pos.lat, lon: pos.lon });
    const home = inBounds(pos.lat, pos.lon, map!.bounds)
      ? map!
      : containingMap(catalog.maps, pos.lat, pos.lon);
    if (home && home.id !== mapId) setMapId(home.id);
    goTo(pos.lat, pos.lon, 14);
    setMsg({ key: pos.source === "gps" ? "whereami.source.gps" : "whereami.source.manual" });
  }

  async function iAmHere() {
    const c = api.current?.center();
    if (!c) return;
    setPin({ lat: c.lat, lon: c.lon });
    setMsg((await putPosition(c.lat, c.lon)) ? { key: "iAmHere.saved" } : { key: "search.error" });
  }

  async function nearest(kind: NearestKind) {
    if (!pin) {
      setMsg({ key: "nearest.needPosition" });
      return;
    }
    const r = await nearestPlaces(kind, pin);
    show(r);
  }

  function show(r: SearchResult) {
    const bad = failureKey(r);
    if (bad || r.state !== "ok") {
      setPlaces([]);
      setMsg({ key: bad ?? "search.error" });
      return;
    }
    setPlaces(r.results);
    setMsg(r.results.length === 0 ? { key: "search.noResults" } : null);
    if (r.results[0]) goTo(r.results[0].lat, r.results[0].lon, 13);
  }

  async function search(text: string) {
    const c = parseCoordinates(text);
    if (c) {
      setPin(c);
      goTo(c.lat, c.lon, 14);
      setMsg((await putPosition(c.lat, c.lon)) ? { key: "iAmHere.saved" } : null);
      return;
    }
    show(await searchPlaces(text, api.current?.center() ?? null));
  }

  return (
    <div className="viewer">
      <header className="bar">
        <h1 className="title">{t("title")}</h1>
        <Picker maps={catalog.maps} currentId={map.id} onPick={(id) => {
          const m = catalog.maps.find((x) => x.id === id);
          if (!m) return;
          setMapId(id);
          goTo(m.center[1], m.center[0], m.center[2]);
        }} />
        <button
          type="button"
          className="btn"
          lang={lang === "en" ? "es" : "en"}
          onClick={() => setLang(lang === "en" ? "es" : "en")}
        >
          {t("lang.toggle")}
        </button>
      </header>

      {!catalog.assets.available ? (
        <p className="banner" role="status" data-code="LAI-SVC-031">
          {t("banner.labels")}
          {" "}
          <span className="code">{"LAI-SVC-031"}</span>
        </p>
      ) : null}

      {searchOn ? (
        <SearchBox available onSearch={(x) => void search(x)} />
      ) : (
        <>
          <SearchBox available={false} onSearch={() => undefined} />
          <p className="note">
            {t("search.notInstalled")} <span className="code">{"LAI-SVC-032"}</span>
          </p>
        </>
      )}
      <Actions
        searchAvailable={searchOn}
        onWhereAmI={() => void whereAmI()}
        onIAmHere={() => void iAmHere()}
        onNearest={(k) => void nearest(k)}
      />
      <p className="note" role="status">
        {msg ? t(msg.key) : t("iAmHere.hint")}
      </p>
      {suggestion ? (
        <p className="suggest" role="status">
          {t("suggest", { name: lang === "es" ? suggestion.display_name_es || suggestion.display_name : suggestion.display_name })}{" "}
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => setMapId(suggestion.id)}
          >
            {t("suggest.switch")}
          </button>
        </p>
      ) : null}

      <div className="stage">
        <MapView
          map={map}
          lang={lang}
          assetsAvailable={catalog.assets.available}
          initial={load.start}
          pin={pin}
          places={places}
          onMove={setView}
          onTap={(lat, lon) => goTo(lat, lon)}
          apiRef={api}
        />
        <div className="crosshair" aria-hidden="true" />
        <ZoomButtons onZoom={(d) => api.current?.zoomBy(d)} />
      </div>

      <Readout lat={view.lat} lon={view.lon} />
      <ResultList places={places} onPick={(p) => { setPin({ lat: p.lat, lon: p.lon }); goTo(p.lat, p.lon, 15); }} />
      <footer className="foot">
        <span>{"© OpenStreetMap contributors, Protomaps"}</span>
        {searchOn ? <span>{` · ${t("attribution.geonames")}`}</span> : null}
      </footer>
    </div>
  );
}
