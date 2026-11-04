import { useEffect, useRef } from "react";
import { useStartSurvival } from "../components/useStartSurvival";
import { t } from "../i18n/i18n";
import { navigate } from "../router";

/** Shown when a session-only button is pressed with session none (survival-portal.md section 4.1.1). */
export function LockDialog() {
  const ref = useRef<HTMLDialogElement>(null);
  const { busy, failed, start } = useStartSurvival();
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal();
  }, []);
  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby="lock-title"
      onCancel={() => navigate("")}
    >
      <h2 id="lock-title" className="dialog-title">
        {t("lock.title")}
      </h2>
      {failed ? (
        <p className="banner-note" role="alert">
          {t("banner.startFailed")}
        </p>
      ) : null}
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          disabled={busy}
          onClick={() => void start()}
        >
          {t("banner.startSurvival")}
        </button>
        <button
          type="button"
          className="btn"
          onClick={() => window.location.assign("/")}
        >
          {t("lock.login")}
        </button>
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("common.cancel")}
        </button>
      </div>
    </dialog>
  );
}
