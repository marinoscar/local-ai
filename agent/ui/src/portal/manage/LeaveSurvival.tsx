import { useEffect, useRef, useState } from "react";
import { t } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ConfirmDialog } from "./ConfirmDialog";
import { FailureNote } from "./FailureNote";
import { changeMode, getMode, leaveDefault } from "./mode";
import { useAction } from "./useAction";

/**
 * The Leave survival mode dialog (dashboard.md sections 3.2 and 4.2): what happens, where to go
 * (Online or Offline), one button. No PIN for the app's operator session (ADR-0011).
 */
export function LeaveSurvivalDialog({
  connectivity,
  onClose,
}: {
  connectivity?: string;
  onClose: () => void;
}) {
  const [target, setTarget] = useState<"online" | "offline">(leaveDefault(connectivity));
  const touched = useRef(false);
  const { phase, run, busy } = useAction();
  // Without the connectivity (the status line's button) ask the agent once, unless a choice was made.
  useEffect(() => {
    if (connectivity !== undefined) return;
    getMode()
      .then((m) => {
        if (!touched.current) setTarget(leaveDefault(m?.connectivity));
      })
      .catch(() => undefined);
  }, [connectivity]);
  return (
    <ConfirmDialog
      title={t("mode.leave.title")}
      lines={[t("mode.leave.lead")]}
      confirmLabel={t("mode.leave.go")}
      busy={busy}
      onCancel={onClose}
      onConfirm={() =>
        void run(() => changeMode(target)).then((ok) => {
          if (ok) onClose();
        })
      }
    >
      <fieldset className="choice">
        <legend className="field-label">{t("mode.leave.where")}</legend>
        {(["online", "offline"] as const).map((m) => (
          <label key={m} className="choice-row">
            <input
              type="radio"
              name="leave-target"
              value={m}
              checked={target === m}
              onChange={() => {
                touched.current = true;
                setTarget(m);
              }}
            />
            <span>{t(m === "online" ? "mode.leave.online" : "mode.leave.offline")}</span>
          </label>
        ))}
      </fieldset>
      {phase.kind === "failed" ? <FailureNote failure={phase.failure} /> : null}
    </ConfirmDialog>
  );
}

/** The status line's button in survival mode; a Portal session is told to use the app instead. */
export function LeaveSurvivalButton({ status }: { status: PortalStatus }) {
  const [open, setOpen] = useState(false);
  if (status.mode !== "survival" || status.session !== "full") return null;
  return (
    <>
      <button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>
        {t("home.leaveSurvival")}
      </button>
      {open ? <LeaveSurvivalDialog onClose={() => setOpen(false)} /> : null}
    </>
  );
}
