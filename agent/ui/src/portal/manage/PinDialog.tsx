import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { t } from "../i18n/i18n";
import { cachedGrant, verifyPin, type PinResult } from "./pin";

/**
 * usePin gives a screen the PIN step of a PIN action: `ask(why)` resolves with a grant (at once when
 * a recent one is still valid) or null when the person cancelled. Render `dialog` once in the screen.
 */
export function usePin() {
  const [req, setReq] = useState<{
    why: string;
    resolve: (g: string | null) => void;
  } | null>(null);
  const ask = useCallback((why?: string): Promise<string | null> => {
    const g = cachedGrant();
    if (g) return Promise.resolve(g);
    return new Promise((resolve) =>
      setReq({ why: why ?? t("pin.why"), resolve }),
    );
  }, []);
  const dialog = req ? (
    <PinDialog
      why={req.why}
      onDone={(g) => {
        req.resolve(g);
        setReq(null);
      }}
    />
  ) : null;
  return { ask, dialog };
}

function message(r: Exclude<PinResult, { ok: true }>): string {
  switch (r.kind) {
    case "wrong":
      return r.remaining === null
        ? t("pin.wrongNoCount")
        : t("pin.wrong", { n: r.remaining });
    case "locked":
      return t("pin.locked", { s: r.retryAfterS });
    case "not_set":
      return t("pin.notSet");
    default:
      return t("manage.failed", {
        message: r.code ? `${r.message} (${r.code})` : r.message,
      });
  }
}

/** The PIN dialog: one numeric field, Continue and Cancel; Esc cancels. */
export function PinDialog({
  why,
  onDone,
}: {
  why: string;
  onDone: (grant: string | null) => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [pin, setPin] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal?.();
  }, []);
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (pin === "" || busy) return;
    setBusy(true);
    setError("");
    const r = await verifyPin(pin);
    setBusy(false);
    if (r.ok) {
      onDone(r.grant);
      return;
    }
    setPin("");
    setError(message(r));
  }
  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby="pin-title"
      onCancel={(e) => {
        e.preventDefault();
        onDone(null);
      }}
    >
      <form onSubmit={(e) => void submit(e)}>
        <h2 id="pin-title" className="dialog-title">
          {t("pin.title")}
        </h2>
        <p>{why}</p>
        <label className="field">
          <span className="field-label">{t("pin.label")}</span>
          <input
            className="field-input"
            type="password"
            inputMode="numeric"
            autoComplete="off"
            autoFocus
            maxLength={8}
            value={pin}
            onChange={(e) => setPin(e.target.value.replace(/\D/g, ""))}
          />
        </label>
        {error === "" ? null : (
          <p className="warn-text" role="alert">
            {error}
          </p>
        )}
        <div className="actions">
          <button type="submit" className="btn btn-primary" disabled={busy || pin === ""}>
            {t("pin.ok")}
          </button>
          <button type="button" className="btn" onClick={() => onDone(null)}>
            {t("common.cancel")}
          </button>
        </div>
      </form>
    </dialog>
  );
}
