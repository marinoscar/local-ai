import { useState, type FormEvent } from "react";
import { formatDecimal, toDMS } from "./coords";
import { t, type Key } from "./lang";
import type { Place } from "./places";

export const NEAREST_KINDS = ["hospital", "clinic", "water", "town"] as const;
export type NearestKind = (typeof NEAREST_KINDS)[number];

interface SearchProps {
  available: boolean;
  onSearch: (text: string) => void;
}

/** SearchBox: a place name or typed coordinates. Disabled with a reason when no place index exists. */
export function SearchBox({ available, onSearch }: SearchProps) {
  const [text, setText] = useState("");
  function submit(e: FormEvent) {
    e.preventDefault();
    if (text.trim()) onSearch(text.trim());
  }
  return (
    <form className="search" role="search" onSubmit={submit}>
      <label className="sr-only" htmlFor="q">
        {t("search.label")}
      </label>
      <input
        id="q"
        className="field grow"
        type="search"
        value={text}
        disabled={!available}
        placeholder={t("search.placeholder")}
        onChange={(e) => setText(e.target.value)}
      />
      <button type="submit" className="btn btn-primary" disabled={!available}>
        {t("search.go")}
      </button>
    </form>
  );
}

interface ActionProps {
  searchAvailable: boolean;
  onWhereAmI: () => void;
  onIAmHere: () => void;
  onNearest: (kind: NearestKind) => void;
}

export function Actions({
  searchAvailable,
  onWhereAmI,
  onIAmHere,
  onNearest,
}: ActionProps) {
  return (
    <div className="actions" role="group" aria-label={t("whereami")}>
      <button type="button" className="btn btn-primary" onClick={onWhereAmI}>
        {t("whereami")}
      </button>
      <button type="button" className="btn" onClick={onIAmHere}>
        {t("iAmHere")}
      </button>
      <span className="nearest-label">{t("nearest")}</span>
      {NEAREST_KINDS.map((k) => (
        <button
          key={k}
          type="button"
          className="btn"
          disabled={!searchAvailable}
          data-kind={k}
          onClick={() => onNearest(k)}
        >
          {t(`kind.${k}` as Key)}
        </button>
      ))}
    </div>
  );
}

export function ZoomButtons({ onZoom }: { onZoom: (d: 1 | -1) => void }) {
  return (
    <div className="zoom" role="group">
      <button
        type="button"
        className="btn btn-icon"
        aria-label={t("zoomIn")}
        title={t("zoomIn")}
        onClick={() => onZoom(1)}
      >
        {"+"}
      </button>
      <button
        type="button"
        className="btn btn-icon"
        aria-label={t("zoomOut")}
        title={t("zoomOut")}
        onClick={() => onZoom(-1)}
      >
        {"−"}
      </button>
    </div>
  );
}

/** Readout shows the center in decimal degrees and DMS, with a copy button. */
export function Readout({ lat, lon }: { lat: number; lon: number }) {
  const [note, setNote] = useState<Key | null>(null);
  async function copy() {
    try {
      await navigator.clipboard.writeText(formatDecimal(lat, lon));
      setNote("copied");
    } catch {
      setNote("copyFailed");
    }
  }
  return (
    <div className="readout" aria-label={t("center.label")}>
      <div>
        <div className="readout-dec" data-testid="coords-decimal">
          {formatDecimal(lat, lon)}
        </div>
        <div className="readout-dms" data-testid="coords-dms">
          {toDMS(lat, lon)}
        </div>
      </div>
      <button type="button" className="btn" onClick={() => void copy()}>
        {t("copy")}
      </button>
      <span role="status" className="note">
        {note ? t(note) : ""}
      </span>
    </div>
  );
}

export function ResultList({
  places,
  onPick,
}: {
  places: Place[];
  onPick: (p: Place) => void;
}) {
  if (places.length === 0) return null;
  return (
    <section className="results" aria-label={t("search.results")}>
      <ul>
        {places.map((p, i) => (
          <li key={`${p.name}-${i}`}>
            <button
              type="button"
              className="btn result"
              title={t("search.pick")}
              onClick={() => onPick(p)}
            >
              <strong>{p.name}</strong>
              <span>
                {t(`kind.${p.kind}` as Key)}
                {p.admin1 ? `, ${p.admin1}` : ""}
                {p.distance_km !== undefined
                  ? ` · ${t("distance", { km: p.distance_km })}`
                  : ""}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}
