import { useEffect } from "react";
import { t } from "../i18n/i18n";
import {
  statusLevel,
  type CapName,
  type CapState,
  type Level,
  type PortalStatus,
} from "../status";
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

/** One sentence from the capabilities: dot and glyph, brain, three ticks, emergency number. */
export function StatusLine({ status }: { status: PortalStatus }) {
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
    </div>
  );
}
