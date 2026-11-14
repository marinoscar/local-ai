import { useEffect } from "react";
import { isFixAction } from "../actions";
import { t } from "../i18n/i18n";
import { navigate } from "../router";
import {
  statusLevel,
  type CapName,
  type CapState,
  type Level,
  type PortalStatus,
} from "../status";
import { FixButton } from "./FixButton";
import { announce } from "./LiveRegion";

const LEVEL_KEY = {
  green: "status.ready",
  amber: "status.readyProblems",
  red: "status.notReady",
} as const;
const LEVEL_GLYPH: Record<Level, string> = { green: "✓", amber: "!", red: "✗" };
const CAP_GLYPH: Record<CapState, string> = {
  ready: "✓",
  degraded: "!",
  unavailable: "✗",
};

const TICKS: {
  cap: CapName;
  label: "btn.library" | "btn.maps" | "btn.talk";
}[] = [
  { cap: "library", label: "btn.library" },
  { cap: "maps", label: "btn.maps" },
  { cap: "talk", label: "btn.talk" },
];

/**
 * NextStep is "the one next thing" of a status line that is not green (dashboard.md section 4.1): the
 * first alert's fix button, else Run a full check (the Health screen) for an operator session.
 */
function NextStep({ status }: { status: PortalStatus }) {
  const first = status.alerts.find((a) => isFixAction(a.action));
  if (first && isFixAction(first.action))
    return (
      <div className="next-step">
        <strong>{t("home.next")}</strong>
        <span>{first.title}</span>
        <FixButton action={first.action} />
      </div>
    );
  if (status.session !== "full") return null;
  return (
    <div className="next-step">
      <strong>{t("home.next")}</strong>
      <button type="button" className="btn" onClick={() => navigate("manage/health")}>
        {t("home.runCheck")}
      </button>
    </div>
  );
}

/** One sentence from the capabilities: dot and glyph, brain, three ticks, emergency number, Help. */
export function StatusLine({
  status,
  home,
}: {
  status: PortalStatus;
  /** On Home the line adds the one next thing to do; other screens have their own fix buttons. */
  home: boolean;
}) {
  const level = statusLevel(status.capabilities);
  const brain = status.brain.loaded[0]?.display_name ?? t("status.noBrain");
  const text = t(LEVEL_KEY[level]);
  useEffect(() => announce(text), [text]);
  return (
    <div className="statusline">
      <span className={`dot dot-${level}`} aria-hidden="true">
        {LEVEL_GLYPH[level]}
      </span>
      <strong className="statusline-main">{text}</strong>
      <span>{t("status.brain", { name: brain })}</span>
      {TICKS.map(({ cap, label }) => {
        const st = status.capabilities[cap]?.state ?? "unavailable";
        return (
          <span key={cap} className="tick">
            {t(label)} <span>{CAP_GLYPH[st]}</span>
          </span>
        );
      })}
      <span className="statusline-end">
        {t("status.emergency", { n: status.emergency_number })}
      </span>
      <span className="statusline-actions">
        <button type="button" className="btn" onClick={() => navigate("help")}>
          {t("manage.help")}
        </button>
      </span>
      {home && level !== "green" ? <NextStep status={status} /> : null}
    </div>
  );
}
