import { useEffect, useRef } from "react";
import { Shell } from "./components/Shell";
import { t, useLang } from "./i18n/i18n";
import { setScale, stepScale, useScale } from "./prefs";
import { navigate, useLocation, useScreen } from "./router";
import { FirstAid } from "./screens/FirstAid";
import { Library } from "./screens/Library";
import { Reader } from "./screens/Reader";
import { ChatInterstitial } from "./screens/ChatInterstitial";
import { Brain } from "./screens/Brain";
import { Home } from "./screens/Home";
import { Music } from "./screens/Music";
import { Status } from "./screens/Status";
import { LockDialog } from "./screens/LockDialog";
import { mapsManualsBody, useHelpRedirect } from "./screens/mapsManuals";
import { Photo } from "./screens/Photo";
import { Stub } from "./screens/Stub";
import { Talk } from "./screens/Talk";
import { Unavailable } from "./screens/Unavailable";
import { useStatus, type PortalStatus } from "./status";
import { viewFor } from "./view";

/** Esc always returns home; Ctrl +/- change the text scale (survival-portal.md section 4.10). */
function useGlobalKeys() {
  const scale = useScale();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        navigate("");
      } else if (
        e.ctrlKey &&
        !e.altKey &&
        !e.metaKey &&
        (e.key === "+" || e.key === "=")
      ) {
        e.preventDefault();
        setScale(stepScale(scale, 1));
      } else if (
        e.ctrlKey &&
        !e.altKey &&
        !e.metaKey &&
        (e.key === "-" || e.key === "_")
      ) {
        e.preventDefault();
        setScale(stepScale(scale, -1));
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [scale]);
}

export function App() {
  const lang = useLang();
  useGlobalKeys();
  const state = useStatus();
  const screen = useScreen();
  const status = state.status;
  const view = viewFor(screen, status);
  useHelpRedirect(screen, status);
  const loc = useLocation();
  const reading = loc.split("?")[0].replace(/\/+$/, "") === "/portal/read";

  useEffect(() => {
    document.title = t("app.title");
  }, [lang]);

  // After a screen change (not on first load) move focus to the content so keyboard and screen
  // reader users start at the top of the new screen.
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    document.getElementById("main")?.focus({ preventScroll: true });
  }, [screen]);

  let body;
  if (reading) {
    body = (
      <Reader search={loc.includes("?") ? loc.slice(loc.indexOf("?")) : ""} />
    );
  } else {
    switch (view.kind) {
    case "home":
      body = <Home status={status} dialogOpen={false} />;
      break;
    case "lock":
      body = (
        <>
          <Home status={status} dialogOpen />
          <LockDialog />
        </>
      );
      break;
    case "unavailable":
      body = <Unavailable cap={view.cap} status={status as PortalStatus} />;
      break;
    default:
      body =
        view.screen === "chat" ? (
          <ChatInterstitial status={status} />
        ) : view.screen === "library" ? (
          <Library status={status} />
        ) : view.screen === "first-aid" ? (
          <FirstAid status={status} />
        ) : view.screen === "photo" ? (
          <Photo status={status} />
        ) : view.screen === "talk" ? (
          <Talk status={status} />
        ) : view.screen === "status" && status ? (
          <Status status={status} />
        ) : view.screen === "brain" && status ? (
          <Brain status={status} />
        ) : view.screen === "music" ? (
          <Music status={status} />
        ) : (
          (mapsManualsBody(view.screen, status) ?? <Stub screen={view.screen} />)
        );
    }
  }
  return (
    <Shell state={state} home={view.kind === "home" || view.kind === "lock"}>
      {body}
    </Shell>
  );
}
