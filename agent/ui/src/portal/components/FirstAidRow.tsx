import type { KeyboardEvent } from "react";
import { t } from "../i18n/i18n";
import { firstAidIcon } from "../icons/firstaid";
import { readerPath } from "../library/reader";
import { navigate, navigatePath } from "../router";
import type { FirstAidEntry, PortalStatus } from "../status";

/** Entries the SPA shows: the available ones, in the order the agent sent (sorted by `order`). */
export function availableEntries(
  status: PortalStatus | null,
): FirstAidEntry[] {
  return (status?.first_aid ?? []).filter((e) => e.available);
}

/** openEntry shows the article in the reader frame; Back and Esc return Home or the grid. */
export function openEntry(e: FirstAidEntry, back: string): void {
  navigatePath(readerPath(e.url, back));
}

export function EntryIcon({ k }: { k: string }) {
  const url = firstAidIcon(k);
  return url ? <img className="fa-icon" src={url} alt="" /> : null;
}

/** Arrow keys move the focus between the buttons of a row or grid. */
export function arrowNav(e: KeyboardEvent<HTMLElement>): void {
  const dir =
    e.key === "ArrowRight" || e.key === "ArrowDown"
      ? 1
      : e.key === "ArrowLeft" || e.key === "ArrowUp"
        ? -1
        : 0;
  if (dir === 0) return;
  const items = Array.from(
    e.currentTarget.querySelectorAll<HTMLElement>("button, a[href]"),
  );
  const i = items.indexOf(document.activeElement as HTMLElement);
  if (i < 0) return;
  e.preventDefault();
  items[Math.min(items.length - 1, Math.max(0, i + dir))]?.focus();
}

/** The first-aid row of Home (survival-portal.md section 4.1.1): the first six available entries and "More". */
export function FirstAidRow({ status }: { status: PortalStatus | null }) {
  const entries = availableEntries(status).slice(0, 6);
  return (
    <div className="firstaid">
      <nav
        aria-labelledby="firstaid-title"
        className="firstaid-row"
        onKeyDown={arrowNav}
      >
        <h2 id="firstaid-title" className="firstaid-title">
          {t("firstaid.title")}
        </h2>
        {entries.map((e, i) => (
          <button
            key={e.key}
            id={i === 0 ? "firstaid-first" : undefined}
            type="button"
            className="chip"
            onClick={() => openEntry(e, "/portal")}
          >
            <EntryIcon k={e.key} />
            {e.title}
          </button>
        ))}
        <button
          type="button"
          id={entries.length === 0 ? "firstaid-first" : undefined}
          className="chip chip-more"
          onClick={() => navigate("first-aid")}
        >
          {t("firstaid.more")}
        </button>
      </nav>
      {status && entries.length === 0 ? (
        <p className="disclaimer">{t("firstaid.missing")}</p>
      ) : null}
      <p className="disclaimer">
        {t("firstaid.disclaimer", { n: status?.emergency_number ?? "911" })}
      </p>
    </div>
  );
}
