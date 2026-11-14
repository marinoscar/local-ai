import { useCallback, useEffect, useState } from "react";
import { t, useLang } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ConfirmDialog } from "./ConfirmDialog";
import { FailureNote } from "./FailureNote";
import { LeaveSurvivalDialog } from "./LeaveSurvival";
import { ManageFrame } from "./ManageFrame";
import {
  changeMode,
  getMode,
  isMode,
  MODE_CONFIRM,
  MODE_LABEL,
  MODE_WHAT,
  MODES,
  type ModeName,
  type ModeView,
} from "./mode";
import { useAction } from "./useAction";

/** How often GET /mode is asked while a change runs, and otherwise. */
const FAST_MS = 2000;
const SLOW_MS = 10000;

/**
 * The Mode screen (dashboard.md section 4.2): three choices with one sentence each, the current one
 * marked, a confirmation that says what changes. Leaving survival asks where to go; no PIN (ADR-0011).
 */
export function ModeScreen({ status }: { status: PortalStatus }) {
  useLang();
  const [view, setView] = useState<ModeView | null>(null);
  const [asking, setAsking] = useState<ModeName | null>(null);
  const { phase, run, busy, reset } = useAction();

  const load = useCallback(async () => {
    try {
      setView(await getMode());
    } catch {
      // The status line and the next poll say it; the screen keeps the last answer.
    }
  }, []);
  const changing = view?.transition ?? null;
  useEffect(() => {
    void load();
    const h = window.setInterval(() => void load(), changing ? FAST_MS : SLOW_MS);
    return () => window.clearInterval(h);
  }, [load, changing, status.mode]);

  const current = isMode(view?.mode) ? view.mode : isMode(status.mode) ? status.mode : null;
  const target = changing && isMode(changing.target) ? changing.target : null;

  return (
    <ManageFrame title={t("mode.title")} lead={t("mode.lead")}>
      {view?.suggest_survival ? (
        <p className="card warn-text" role="status">
          {t("mode.suggest")}
        </p>
      ) : null}
      {target ? (
        <p className="card" role="status">
          {t("mode.changing", { mode: t(MODE_LABEL[target]) })}
        </p>
      ) : null}
      <ul className="mode-list">
        {MODES.map((m) => {
          const on = m === current && !target;
          return (
            <li key={m} className={on ? "card mode-card mode-on" : "card mode-card"}>
              <h2 className="card-title">
                {t(MODE_LABEL[m])}
                {on ? <span className="mode-badge">{` ✓ ${t("mode.current")}`}</span> : null}
              </h2>
              <p>{t(MODE_WHAT[m])}</p>
              {on ? null : (
                <button
                  type="button"
                  className="btn btn-primary"
                  disabled={busy || target !== null}
                  onClick={() => {
                    reset();
                    setAsking(m);
                  }}
                >
                  {t("mode.choose", { mode: t(MODE_LABEL[m]) })}
                </button>
              )}
            </li>
          );
        })}
      </ul>
      {phase.kind === "done" && current ? (
        <p className="fix-result" role="status">
          {phase.text}
        </p>
      ) : null}
      {phase.kind === "failed" ? <FailureNote failure={phase.failure} /> : null}
      {asking && current === "survival" && asking !== "survival" ? (
        <LeaveSurvivalDialog
          connectivity={view?.connectivity}
          onClose={() => {
            setAsking(null);
            void load();
          }}
        />
      ) : asking ? (
        <ConfirmDialog
          title={t("mode.confirm.title", { mode: t(MODE_LABEL[asking]) })}
          lines={[t(MODE_CONFIRM[asking])]}
          confirmLabel={t("mode.choose", { mode: t(MODE_LABEL[asking]) })}
          busy={busy}
          onCancel={() => setAsking(null)}
          onConfirm={() => {
            const to = asking;
            setAsking(null);
            void run(
              () => changeMode(to),
              t("mode.changing", { mode: t(MODE_LABEL[to]) }),
            ).then(() => load());
          }}
        />
      ) : null}
    </ManageFrame>
  );
}
