import { useEffect } from "react";
import { t } from "../i18n/i18n";
import { navigate } from "../router";
import type { PortalStatus } from "../status";

export const CHAT_DELAY_MS = 2000;
export const CHAT_URL = "/chat/";

const IGNORED_KEYS = new Set([
  "Escape",
  "Tab",
  "Shift",
  "Control",
  "Alt",
  "Meta",
]);

/** Chat interstitial (survival-portal.md section 4.1.2): 2 s, or any key, then the WebUI. */
export function ChatInterstitial({ status }: { status: PortalStatus | null }) {
  const noBrain = status !== null && status.brain.loaded.length === 0;
  useEffect(() => {
    if (noBrain) return;
    const open = () => window.location.assign(CHAT_URL);
    const timer = window.setTimeout(open, CHAT_DELAY_MS);
    const onKey = (e: KeyboardEvent) => {
      if (!IGNORED_KEYS.has(e.key) && !e.ctrlKey && !e.altKey && !e.metaKey)
        open();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("keydown", onKey);
    };
  }, [noBrain]);

  if (noBrain) {
    return (
      <section className="screen" aria-labelledby="screen-title">
        <h1 id="screen-title" className="screen-title">
          {t("chat.noBrain")}
        </h1>
        <div className="actions">
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => navigate("brain")}
          >
            {t("chat.chooseBrain")}
          </button>
          <button type="button" className="btn" onClick={() => navigate("")}>
            {t("common.back")}
          </button>
        </div>
      </section>
    );
  }
  return (
    <section className="screen" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t("chat.opening")}
      </h1>
      <p className="lead">{t("chat.tip")}</p>
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          onClick={() => window.location.assign(CHAT_URL)}
        >
          {t("chat.openNow")}
        </button>
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("common.back")}
        </button>
      </div>
    </section>
  );
}
