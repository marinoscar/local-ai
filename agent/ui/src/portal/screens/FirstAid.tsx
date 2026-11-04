import {
  arrowNav,
  availableEntries,
  EntryIcon,
  openEntry,
} from "../components/FirstAidRow";
import { t } from "../i18n/i18n";
import { navigate } from "../router";
import type { PortalStatus } from "../status";

/** The full first-aid grid at /portal/first-aid (survival-portal.md section 4.1.10). */
export function FirstAid({ status }: { status: PortalStatus | null }) {
  const entries = availableEntries(status);
  const alert = status?.alerts.find((a) => a.code === "LAI-SVC-109");
  return (
    <section className="screen" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("firstaid.title")}
      </h1>
      <p className="firstaid-note">
        {`${t("firstaid.disclaimer", { n: status?.emergency_number ?? "911" })} ${t("firstaid.readLater")}`}
      </p>
      {entries.length === 0 ? (
        <>
          <p className="lead">{t("firstaid.missing")}</p>
          {alert ? (
            <p className="code">{t("common.code", { code: alert.code })}</p>
          ) : null}
        </>
      ) : (
        <ul className="fa-grid" onKeyDown={arrowNav}>
          {entries.map((e) => (
            <li key={e.key}>
              <button
                type="button"
                className="btn fa-btn"
                onClick={() => openEntry(e, "/portal/first-aid")}
              >
                <EntryIcon k={e.key} />
                {e.title}
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="actions">
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("common.home")}
        </button>
      </div>
    </section>
  );
}
