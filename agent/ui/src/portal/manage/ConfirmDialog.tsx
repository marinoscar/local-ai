import { useEffect, useRef, type ReactNode } from "react";
import { t } from "../i18n/i18n";

/**
 * The confirmation of a Manage button (ADR-0009: say what changes or what is lost, then ask). Esc and
 * Cancel close it without doing anything.
 */
export function ConfirmDialog({
  title,
  lines,
  confirmLabel,
  danger,
  busy,
  onConfirm,
  onCancel,
  children,
}: {
  title: string;
  lines: string[];
  confirmLabel?: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  children?: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal?.();
  }, []);
  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby="confirm-title"
      onCancel={(e) => {
        e.preventDefault();
        onCancel();
      }}
    >
      <h2 id="confirm-title" className="dialog-title">
        {title}
      </h2>
      {lines.map((l) => (
        <p key={l}>{l}</p>
      ))}
      {children}
      <div className="actions">
        <button
          type="button"
          className={danger ? "btn btn-danger" : "btn btn-primary"}
          disabled={busy}
          onClick={onConfirm}
        >
          {confirmLabel ?? t("manage.confirm")}
        </button>
        <button type="button" className="btn" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
    </dialog>
  );
}
