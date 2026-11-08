import { isFixAction } from "../actions";
import { FixButton } from "../components/FixButton";
import { t, type Key } from "../i18n/i18n";
import { navigate } from "../router";
import type { CapName, PortalStatus } from "../status";

// Plain-words problem per capability when the agent sent no alert for its code (survival-portal.md
// section 4.2). Each string already carries its one-sentence fix.
const NONE: Record<CapName, Key> = {
  chat: "chat.noBrain",
  photo: "photo.none",
  talk: "talk.unavailable",
  library: "library.none",
  maps: "maps.none",
  manuals: "manuals.none",
};

/** Full-screen explanation of an unavailable capability: what is missing, the fix, the code, the manual. */
export function Unavailable({
  cap,
  status,
}: {
  cap: CapName;
  status: PortalStatus;
}) {
  const code = status.capabilities[cap]?.code;
  const alert = code ? status.alerts.find((a) => a.code === code) : undefined;
  // The button of the fix (survival-portal.md section 4.2): the capability's, else its alert's.
  const action = status.capabilities[cap]?.action ?? alert?.action;
  return (
    <section className="screen" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("unavailable.title")}
      </h1>
      {alert ? (
        <>
          <p className="lead">{alert.title}</p>
          <p className="lead">{alert.fix}</p>
        </>
      ) : (
        <p className="lead">{t(NONE[cap])}</p>
      )}
      {code ? <p className="code">{t("common.code", { code })}</p> : null}
      {isFixAction(action) ? <FixButton action={action} /> : null}
      <div className="actions">
        {cap !== "manuals" ? (
          <button
            type="button"
            className={isFixAction(action) ? "btn" : "btn btn-primary"}
            onClick={() => navigate("manuals")}
          >
            {t("unavailable.manual")}
          </button>
        ) : null}
        <button type="button" className="btn" onClick={() => navigate("help")}>
          {t("help.title")}
        </button>
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("common.home")}
        </button>
      </div>
    </section>
  );
}
