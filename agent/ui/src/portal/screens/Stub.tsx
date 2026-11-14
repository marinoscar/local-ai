import { t, type Key } from "../i18n/i18n";
import { navigate, type Screen } from "../router";

// Screens owned by later items (PORTAL-06 .. PORTAL-11) show their title until they land. Each of
// them replaces its entry in App.tsx; the lock and unavailable handling in front of it stays.
// Music has its own screen and words (screens/Music.tsx), so it never reaches the stub.
const TITLE: Record<Exclude<Screen, "music">, Key> = {
  chat: "btn.chat",
  photo: "photo.title",
  talk: "talk.title",
  library: "library.title",
  maps: "maps.title",
  manuals: "manuals.title",
  status: "status.title",
  brain: "brain.title",
  "first-aid": "firstaid.title",
  help: "help.title",
};

export function Stub({ screen }: { screen: Exclude<Screen, "music"> }) {
  return (
    <section className="screen" aria-labelledby="screen-title">
      <h1 id="screen-title" className="screen-title">
        {t(TITLE[screen])}
      </h1>
      <p className="lead">{t("common.notReady")}</p>
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          onClick={() => navigate("")}
        >
          {t("common.home")}
        </button>
      </div>
    </section>
  );
}
