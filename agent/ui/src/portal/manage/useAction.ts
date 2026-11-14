import { useCallback, useState } from "react";
import { failureOf, type Failure } from "../api";
import { announce } from "../components/LiveRegion";
import { t } from "../i18n/i18n";
import { recordError, refreshStatus } from "../status";

export type ActionPhase =
  | { kind: "idle" }
  | { kind: "working" }
  | { kind: "done"; text: string }
  | { kind: "failed"; failure: Failure };

/**
 * useAction runs one button's work and keeps what the screen says about it: working, done (with the
 * sentence to show) or failed (code, message, fix). The status is refetched after a success.
 */
export function useAction() {
  const [phase, setPhase] = useState<ActionPhase>({ kind: "idle" });
  const run = useCallback(
    async (work: () => Promise<unknown>, doneText?: string): Promise<boolean> => {
      setPhase({ kind: "working" });
      announce(t("manage.working"));
      try {
        await work();
        const text = doneText ?? t("manage.done");
        setPhase({ kind: "done", text });
        announce(text);
        void refreshStatus();
        return true;
      } catch (e) {
        const failure = failureOf(e);
        recordError(`${failure.code} ${failure.message}`);
        setPhase({ kind: "failed", failure });
        announce(t("manage.failed", { message: failure.message }), true);
        return false;
      }
    },
    [],
  );
  const reset = useCallback(() => setPhase({ kind: "idle" }), []);
  return { phase, run, reset, busy: phase.kind === "working" };
}
