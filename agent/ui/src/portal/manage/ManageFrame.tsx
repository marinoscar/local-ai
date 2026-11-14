import type { ReactNode } from "react";
import { t, useLang, type Key } from "../i18n/i18n";
import { MANAGE_SCREENS, navigate, useScreen, type ManageScreen } from "../router";
import "../styles/manage.css";

export const MANAGE_LABEL: Record<ManageScreen, Key> = {
  "manage/mode": "manage.mode",
  "manage/content": "manage.content",
  "manage/models": "manage.models",
  "manage/health": "manage.health",
  "manage/system": "manage.system",
};

/** The row of Manage screens at the top of each one, so moving between them never needs Home. */
function ManageTabs() {
  const current = useScreen();
  return (
    <nav aria-label={t("manage.title")} className="manage-tabs">
      {MANAGE_SCREENS.map((s) => (
        <button
          key={s}
          type="button"
          className={s === current ? "tab tab-on" : "tab"}
          aria-current={s === current ? "page" : undefined}
          onClick={() => navigate(s)}
        >
          {t(MANAGE_LABEL[s])}
        </button>
      ))}
      <button type="button" className="tab" onClick={() => navigate("help")}>
        {t("manage.help")}
      </button>
    </nav>
  );
}

/** The frame of a Manage screen: tabs, title, an optional lead sentence, the body, Back to Home. */
export function ManageFrame({
  title,
  lead,
  children,
}: {
  title: string;
  lead?: string;
  children: ReactNode;
}) {
  useLang();
  return (
    <section className="screen screen-wide manage" aria-labelledby="screen-title">
      <ManageTabs />
      <h1 id="screen-title" className="screen-title">
        {title}
      </h1>
      {lead ? <p className="lead">{lead}</p> : null}
      {children}
      <div className="actions">
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("manage.back")}
        </button>
      </div>
    </section>
  );
}

/** What a Manage screen shows without an operator session (a browser in survival mode). */
export function ManageLocked({ screen }: { screen: ManageScreen }) {
  useLang();
  return (
    <section className="screen manage" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t(MANAGE_LABEL[screen])}
      </h1>
      <p className="lead">{t("manage.locked")}</p>
      <p>{t("manage.lockedWhy")}</p>
      <div className="actions">
        <button type="button" className="btn btn-primary" onClick={() => navigate("help")}>
          {t("manage.help")}
        </button>
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("manage.back")}
        </button>
      </div>
    </section>
  );
}

/** The line under a button: working, done, or the failure with its code and fix. */
export { FailureNote } from "./FailureNote";
