import { useMemo, useState } from "react";
import "@localai/ui/creative.css";
import "../styles/music.css";
import {
  ErrorNotice,
  LangProvider,
  MusicStudio,
  SongLibrary,
} from "@localai/ui/music";
import { WorkloadSwitcher } from "@localai/ui/workloads";
import { announce } from "../components/LiveRegion";
import { t, useLang } from "../i18n/i18n";
import { MUSIC_WORKLOAD } from "../music/agent";
import { mt } from "../music/i18n/i18n";
import { musicTransports } from "../music/transports";
import { useMusicStatus } from "../music/useMusicStatus";
import {
  CODE_ENGINE_FAILED,
  CODE_NOT_INSTALLED,
  CODE_NOT_LOADED,
  CODE_SIGN_IN,
  CODE_SURVIVAL,
  musicView,
  showLibrary,
  showStudio,
} from "../music/view";
import { navigate } from "../router";
import type { PortalStatus } from "../status";

/**
 * The /music/ page (issue #512), state first: not installed (LAI-SVC-150), not loaded ([Switch to Music]), starting,
 * failed (LAI-LLM-040), ready (the studio). The song list shows whenever songs can be read.
 */
export function Music({ status }: { status: PortalStatus | null }) {
  const lang = useLang();
  const tr = useMemo(musicTransports, []);
  const enabled =
    status !== null && status.mode !== "survival" && status.session === "full";
  const ms = useMusicStatus(tr.music, enabled);
  const view = musicView(status, ms);
  const [songsKey, setSongsKey] = useState(0);

  const switcher = (
    <WorkloadSwitcher
      transport={tr.workloads}
      only={[MUSIC_WORKLOAD]}
      headingLevel={2}
      onActivated={() => {
        announce(mt("music.ready"));
        void ms.refetch();
      }}
    />
  );

  let state;
  switch (view.kind) {
    case "loading":
      state = <p className="lead">{mt("music.loading")}</p>;
      break;
    case "survival":
      state = (
        <ErrorNotice
          role="status"
          error={{
            code: CODE_SURVIVAL,
            message: mt("music.survival"),
            fix: mt("music.survivalFix"),
          }}
        />
      );
      break;
    case "sign_in":
      state = (
        <ErrorNotice
          role="status"
          error={{
            code: CODE_SIGN_IN,
            message: mt("music.signIn"),
            fix: mt("music.signInFix"),
          }}
        />
      );
      break;
    case "error":
      state = (
        <>
          <ErrorNotice
            error={{
              code: view.code,
              message: view.message || mt("music.statusFailed"),
              fix: view.fix,
            }}
          />
          <div className="actions">
            <button
              type="button"
              className="btn"
              onClick={() => void ms.refetch()}
            >
              {t("common.tryAgain")}
            </button>
          </div>
        </>
      );
      break;
    case "not_installed":
      state = (
        <>
          <p className="lead">{mt("music.notInstalledWhy")}</p>
          <ErrorNotice
            error={{
              code: CODE_NOT_INSTALLED,
              message: mt("music.notInstalled"),
              fix: mt("music.notInstalledFix"),
            }}
          />
        </>
      );
      break;
    case "not_loaded":
      state = (
        <div className="panel" data-testid="music-not-loaded">
          <p className="lead">{mt("music.notLoaded")}</p>
          <p>{mt("music.notLoadedWhy")}</p>
          <p className="code">{t("common.code", { code: CODE_NOT_LOADED })}</p>
          {switcher}
        </div>
      );
      break;
    case "starting":
      state = (
        <div className="panel" role="status">
          <p className="lead">{mt("music.starting")}</p>
          <progress className="meter" aria-label={mt("music.starting")} />
        </div>
      );
      break;
    case "stopping":
      state = (
        <div className="panel" role="status">
          <p className="lead">{mt("music.stopping")}</p>
        </div>
      );
      break;
    case "failed":
      state = (
        <div className="panel panel-bad">
          <ErrorNotice
            error={{
              code: CODE_ENGINE_FAILED,
              message: mt("music.failed"),
              fix: mt("music.failedFix"),
            }}
          />
          {switcher}
        </div>
      );
      break;
    case "ready":
      state = (
        <p className="lead music-ready" role="status">
          {mt("music.ready")}
        </p>
      );
      break;
  }

  return (
    <section
      className="screen music"
      aria-labelledby="screen-title"
      onKeyDown={(e) => {
        // Esc returns Home everywhere in the Portal; inside a text field here it would throw away the lyrics, so the
        // field keeps it (Tab out of the field, then Esc, still goes Home).
        const el = e.target as HTMLElement;
        if (
          e.key === "Escape" &&
          (el.tagName === "TEXTAREA" ||
            (el.tagName === "INPUT" &&
              (el as HTMLInputElement).type !== "file"))
        )
          e.stopPropagation();
      }}
    >
      <h1 id="screen-title" className="screen-title">
        {mt("music.title")}
      </h1>
      <LangProvider lang={lang}>
        {state}
        {showStudio(view) ? (
          <MusicStudio
            transport={tr.music}
            disabled={view.kind !== "ready"}
            defaultLanguage={lang}
            onSongsCreated={() => setSongsKey((k) => k + 1)}
          />
        ) : null}
        {showLibrary(view) ? (
          <SongLibrary transport={tr.music} refreshKey={songsKey} />
        ) : null}
      </LangProvider>
      <div className="actions">
        <button type="button" className="btn" onClick={() => navigate("")}>
          {t("common.home")}
        </button>
      </div>
    </section>
  );
}
