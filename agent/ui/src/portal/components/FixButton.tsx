import { useState } from "react";
import { ACTIONS, runFix, type FixAction, type FixResult } from "../actions";
import { t, useLang } from "../i18n/i18n";
import { navigate } from "../router";
import { recordError, refreshStatus } from "../status";
import { announce } from "./LiveRegion";

type Phase = { kind: "idle" } | { kind: "working" } | { kind: "done"; result: FixResult };

/**
 * FixButton is the primary button of a problem whose code has an action (doctor-codes.md section
 * 1.7): it does the fix, says what happened in plain words, and shows the typed command as a
 * second line. Errors keep their doctor code.
 */
export function FixButton({ action }: { action: FixAction }) {
  useLang();
  const info = ACTIONS[action];
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });

  async function press() {
    if (info.path === null) {
      navigate("brain");
      return;
    }
    setPhase({ kind: "working" });
    announce(t("fix.working"));
    const result = await runFix(action);
    setPhase({ kind: "done", result });
    if (result.ok) {
      announce(t(info.done));
      void refreshStatus();
    } else {
      recordError(`${result.code} ${result.message}`);
      announce(t("fix.failed", { message: result.message }), true);
    }
  }

  return (
    <div className="fix">
      <button
        type="button"
        className="btn btn-primary"
        disabled={phase.kind === "working"}
        onClick={() => void press()}
      >
        {t(info.label)}
      </button>
      {info.command === "" ? null : (
        <p className="fix-command">
          {t("fix.orType", { cmd: info.command })}
        </p>
      )}
      {phase.kind === "working" ? (
        <p className="fix-result" role="status">
          {t("fix.working")}
        </p>
      ) : null}
      {phase.kind === "done" && phase.result.ok ? (
        <p className="fix-result" role="status">
          {t(info.done)}
        </p>
      ) : null}
      {phase.kind === "done" && !phase.result.ok ? (
        <div className="fix-result fix-failed" role="alert">
          <p>{t("fix.failed", { message: phase.result.message })}</p>
          {phase.result.fix === "" ? null : <p>{phase.result.fix}</p>}
          {phase.result.code === "" ? null : (
            <p className="code">{t("common.code", { code: phase.result.code })}</p>
          )}
        </div>
      ) : null}
    </div>
  );
}
