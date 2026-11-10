import { useEffect, useRef, useState } from "react";
import {
  STATE_MARK,
  cancelEnsure,
  canInstall,
  ensureStore,
  startEnsure,
  stepsNeedingOk,
  useEnsureStore,
  type EnsureCall,
  type EnsureView,
} from "../ensure";
import { t, useLang, type Key } from "../i18n/i18n";
import { recordError } from "../status";
import { announce } from "./LiveRegion";

type Failure = Extract<EnsureCall, { ok: false }>;

/** The typed command a person at the computer uses to approve a step (the Portal cannot confirm one). */
const CONFIRM_COMMAND = "sudo aictl ensure --yes";

/**
 * EnsureCard is the state-first card of "Install what's missing" (ADR-0010, survival-portal.md
 * section 4.1.8): "Complete", "Installing (n of m)" or "Needs you: <one thing>" first, the one next
 * thing to do, one button, progress and a stop button while it runs, then each part. A Portal session
 * cannot confirm compiling or restarting a running service, so those steps say who may.
 */
export function EnsureCard() {
  useLang();
  const state = useEnsureStore();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [stopped, setStopped] = useState(false);

  useEffect(() => {
    const s = ensureStore();
    s.start();
    return () => s.stop();
  }, []);

  const view = state.view;
  const last = useRef<string | null>(null);
  useEffect(() => {
    if (!view || last.current === view.state) return;
    // Say the new state once; the progress count is not read out every second.
    if (last.current !== null) announce(view.message);
    last.current = view.state;
  }, [view]);

  async function run(call: () => Promise<EnsureCall>, wasStop: boolean) {
    setBusy(true);
    setFailure(null);
    setStopped(false);
    announce(t("fix.working"));
    const res = await call();
    setBusy(false);
    if (res.ok) {
      ensureStore().accept(res.view);
      if (wasStop) {
        setStopped(true);
        announce(t("ensure.stopped"));
      }
    } else {
      setFailure(res);
      recordError(`${res.code} ${res.message}`);
      announce(t("fix.failed", { message: res.message }), true);
    }
  }

  if (!view) {
    return state.phase === "unavailable" ? (
      <section className="ensure ensure-warn" aria-labelledby="ensure-title">
        <h2 id="ensure-title" className="status-h">
          {t("ensure.title")}
        </h2>
        <p role="status">{t("ensure.unavailable")}</p>
      </section>
    ) : null;
  }

  return (
    <section className={`ensure ensure-${view.state}`} aria-labelledby="ensure-title">
      <h2 id="ensure-title" className="status-h">
        {t("ensure.title")}
      </h2>
      <p className="ensure-state">
        <span className="mark" aria-hidden="true">
          {STATE_MARK[view.state]}
        </span>
        <span className="sr-only">{`${t(`ensure.state.${view.state}` as Key)}: `}</span>
        <span>{view.message}</span>
      </p>
      {view.state === "installing" && view.progress.total > 0 ? (
        <div className="meter-row">
          <progress
            className="meter"
            max={view.progress.total}
            value={view.progress.done}
            aria-label={t("ensure.progress", {
              done: view.progress.done,
              total: view.progress.total,
            })}
          />
          <span className="meter-text">
            {t("ensure.progress", {
              done: Math.min(view.progress.done + 1, view.progress.total),
              total: view.progress.total,
            })}
          </span>
        </div>
      ) : null}
      {view.next_action.kind !== "none" && view.next_action.kind !== "wait" && view.next_action.text !== "" ? (
        <p className="ensure-next">
          <strong>{t("ensure.next")}</strong>
          <span className="block">{view.next_action.text}</span>
        </p>
      ) : null}
      <div className="actions ensure-actions">
        {view.state === "installing" ? (
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={() => void run(() => cancelEnsure(), true)}
          >
            {t("ensure.cancel")}
          </button>
        ) : canInstall(view) ? (
          <button
            type="button"
            className="btn btn-primary"
            disabled={busy}
            onClick={() => void run(() => startEnsure(), false)}
          >
            {t("fix.ensure")}
          </button>
        ) : null}
      </div>
      {view.state !== "complete" ? (
        <p className="fix-command">{t("fix.orType", { cmd: "aictl ensure" })}</p>
      ) : null}
      {busy ? (
        <p className="fix-result" role="status">
          {t("fix.working")}
        </p>
      ) : null}
      {stopped ? (
        <p className="fix-result" role="status">
          {t("ensure.stopped")}
        </p>
      ) : null}
      {failure ? (
        <div className="fix-result fix-failed" role="alert">
          <p>{t("fix.failed", { message: failure.message })}</p>
          {failure.fix === "" ? null : <p>{failure.fix}</p>}
          {failure.code === "" ? null : (
            <p className="code">{t("common.code", { code: failure.code })}</p>
          )}
        </div>
      ) : null}
      <Steps view={view} />
    </section>
  );
}

function Steps({ view }: { view: EnsureView }) {
  if (view.steps.length === 0) return null;
  const open = view.state !== "complete";
  const askFirst = new Set(stepsNeedingOk(view).map((s) => s.id));
  return (
    <details className="details ensure-steps" open={open}>
      <summary>{t("ensure.steps")}</summary>
      <ul className="ensure-list">
        {view.steps.map((s) => (
          <li key={s.id} className={`ensure-step ensure-step-${s.status}`}>
            <span className="block">
              <strong>{s.title}</strong>
              <span className="ensure-step-status">{` · ${t(`ensure.step.${s.status}` as Key)}`}</span>
            </span>
            {s.detail === "" ? null : <span className="block">{s.detail}</span>}
            {askFirst.has(s.id) ? (
              <span className="block">
                {WARN[s.id] ? `${t(WARN[s.id])} ` : ""}
                {t("ensure.needsOk", { cmd: CONFIRM_COMMAND })}
              </span>
            ) : null}
            {s.code ? <span className="block code">{s.code}</span> : null}
          </li>
        ))}
      </ul>
    </details>
  );
}

const WARN: Record<string, Key> = {
  "whisper.build": "ensure.warn.whisper.build",
  "restart.kiwix": "ensure.warn.restart.kiwix",
};
