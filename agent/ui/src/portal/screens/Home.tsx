import { useEffect, type ReactNode } from "react";
import { BigButton } from "../components/BigButton";
import { FirstAidRow } from "../components/FirstAidRow";
import { powerText } from "../format";
import { t, type Key } from "../i18n/i18n";
import {
  BatteryIcon,
  BookIcon,
  BrainIcon,
  CameraIcon,
  ChatIcon,
  MapIcon,
  ManualIcon,
  MicIcon,
} from "../icons/Icons";
import { MANAGE_LABEL } from "../manage/ManageFrame";
import { MANAGE_SCREENS, navigate, type Screen } from "../router";
import type { CapName, PortalStatus } from "../status";

interface Def {
  num: number;
  screen: Screen;
  icon: ReactNode;
  label: Key;
  sub?: Key;
  accent: string;
  /** Needs a session (shows the lock when the status says session none). */
  session?: boolean;
  cap?: CapName;
}

export const BUTTONS: Def[] = [
  {
    num: 1,
    screen: "chat",
    icon: <ChatIcon />,
    label: "btn.chat",
    sub: "btn.chat.sub",
    accent: "a-chat",
    session: true,
    cap: "chat",
  },
  {
    num: 2,
    screen: "photo",
    icon: <CameraIcon />,
    label: "btn.photo",
    sub: "btn.photo.sub",
    accent: "a-photo",
    session: true,
    cap: "photo",
  },
  {
    num: 3,
    screen: "talk",
    icon: <MicIcon />,
    label: "btn.talk",
    sub: "btn.talk.sub",
    accent: "a-talk",
    session: true,
    cap: "talk",
  },
  {
    num: 4,
    screen: "library",
    icon: <BookIcon />,
    label: "btn.library",
    sub: "btn.library.sub",
    accent: "a-library",
    cap: "library",
  },
  {
    num: 5,
    screen: "maps",
    icon: <MapIcon />,
    label: "btn.maps",
    sub: "btn.maps.sub",
    accent: "a-maps",
    cap: "maps",
  },
  {
    num: 6,
    screen: "manuals",
    icon: <ManualIcon />,
    label: "btn.manuals",
    sub: "btn.manuals.sub",
    accent: "a-manuals",
    cap: "manuals",
  },
  {
    num: 7,
    screen: "status",
    icon: <BatteryIcon />,
    label: "btn.status",
    accent: "a-status",
  },
  {
    num: 8,
    screen: "brain",
    icon: <BrainIcon />,
    label: "btn.brain",
    sub: "btn.brain.sub",
    accent: "a-brain",
    session: true,
  },
];

function typingTarget(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null;
  return (
    !!el &&
    (el.tagName === "INPUT" ||
      el.tagName === "TEXTAREA" ||
      el.tagName === "SELECT" ||
      el.isContentEditable)
  );
}

/**
 * The shortcuts of Home: 1-8 open the buttons, F focuses the first-aid row (survival-portal.md section
 * 4.1.1), M opens Manage and H opens Help (dashboard.md section 4.1).
 */
function useHomeKeys(enabled: boolean) {
  useEffect(() => {
    if (!enabled) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.altKey || e.metaKey || typingTarget(e)) return;
      const n = Number(e.key);
      const def = BUTTONS.find((b) => b.num === n);
      if (def) {
        e.preventDefault();
        navigate(def.screen);
      } else if (e.key === "f" || e.key === "F") {
        e.preventDefault();
        document.getElementById("firstaid-first")?.focus();
      } else if (e.key === "m" || e.key === "M") {
        e.preventDefault();
        navigate("manage/mode");
      } else if (e.key === "h" || e.key === "H") {
        e.preventDefault();
        navigate("help");
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [enabled]);
}

/** Home: the eight big buttons and the first-aid row. Buttons are never hidden, whatever the status says. */
export function Home({
  status,
  dialogOpen,
}: {
  status: PortalStatus | null;
  dialogOpen: boolean;
}) {
  useHomeKeys(!dialogOpen);
  const locked = status?.session === "none";
  return (
    <>
      <nav aria-label={t("app.title")}>
        <ul className="grid">
          {BUTTONS.map((b) => {
            const cap = b.cap ? status?.capabilities[b.cap] : undefined;
            const mark =
              cap?.state === "unavailable"
                ? "✗"
                : cap?.state === "degraded" && cap.code
                  ? "!"
                  : undefined;
            return (
              <li key={b.num}>
                <BigButton
                  num={b.num}
                  icon={b.icon}
                  label={t(b.label)}
                  sub={
                    b.screen === "status"
                      ? powerText(status?.power)
                      : t(b.sub as Key)
                  }
                  accent={b.accent}
                  lockHint={b.session && locked ? t("lock.title") : undefined}
                  mark={mark}
                  onActivate={() => navigate(b.screen)}
                />
              </li>
            );
          })}
        </ul>
      </nav>
      {status?.session === "full" ? <ManageRow /> : null}
      <FirstAidRow status={status} />
    </>
  );
}

/** The Manage row of Home (dashboard.md section 4.1), for an operator session in every mode. */
function ManageRow() {
  return (
    <nav aria-labelledby="manage-title" className="manage-row">
      <h2 id="manage-title" className="manage-row-title">
        {t("manage.title")}
      </h2>
      {MANAGE_SCREENS.map((s) => (
        <button
          key={s}
          type="button"
          className="btn"
          aria-keyshortcuts={s === "manage/mode" ? "M" : undefined}
          onClick={() => navigate(s)}
        >
          {t(MANAGE_LABEL[s])}
        </button>
      ))}
    </nav>
  );
}
