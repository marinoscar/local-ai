import { useEffect } from "react";
import { t } from "../i18n/i18n";
import { readerPath } from "../library/reader";
import { navigate, navigatePath } from "../router";
import type { PortalStatus } from "../status";

export const CHAT_DELAY_MS = 2000;
export const CHAT_URL = "/chat/";

/**
 * The chat opens inside the Portal's reader frame, so its header (◄ Back, ⌂ Home) and Esc always lead
 * back to the Portal, in a browser and in the desktop app alike (issue #431, survival-portal.md 4.1.2).
 */
export const CHAT_READER = readerPath(CHAT_URL, "/portal");
const openChat = () => navigatePath(CHAT_READER);

const IGNORED_KEYS = new Set([
  "Escape",
  "Tab",
  "Shift",
  "Control",
  "Alt",
  "Meta",
]);

/** Chat interstitial (survival-portal.md section 4.1.2): 2 s, or any key, then the WebUI in the reader frame. */
export function ChatInterstitial({ status }: { status: PortalStatus | null }) {
  const noBrain = status !== null && status.brain.loaded.length === 0;
  useEffect(() => {
    if (noBrain) return;
    const timer = window.setTimeout(openChat, CHAT_DELAY_MS);
    const onKey = (e: KeyboardEvent) => {
      if (!IGNORED_KEYS.has(e.key) && !e.ctrlKey && !e.altKey && !e.metaKey)
        openChat();
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
          onClick={openChat}
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
