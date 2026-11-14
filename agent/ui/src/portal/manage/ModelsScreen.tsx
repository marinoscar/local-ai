import { useCallback, useEffect, useRef, useState } from "react";
import "../styles/status-brain.css";
import { ApiError, apiSend, failureOf, type Failure } from "../api";
import { getLang, t, useLang, type Key } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ConfirmDialog } from "./ConfirmDialog";
import { FailureNote } from "./FailureNote";
import { ManageFrame } from "./ManageFrame";
import { usePin } from "./PinDialog";
import { useAction } from "./useAction";
import {
  CODE_LAST_COPY,
  CODE_LOAD_FAILED,
  CODE_LOAD_TIMEOUT,
  LOAD_TIMEOUT_MS,
  STATE_KEY,
  buttons,
  deletePath,
  downloadView,
  fits,
  formatSize,
  groups,
  isBrain,
  isHelper,
  rowState,
  type ButtonName,
  type ButtonRule,
  type ModelList,
  type ModelRow,
  type SyncRun,
} from "./models";

// The AI models screen (dashboard.md section 4.4, issue #498): a row per model with its state in plain
// words, and the buttons of section 3.2. The list is GET /models, polled (fast while something
// changes); a load is followed until the model is in use, a download through GET /ark/sync.

const FAST_MS = 1500;
const SLOW_MS = 10_000;

type Confirm =
  | { kind: "use" | "unload" | "download" | "delete" | "lastCopy"; m: ModelRow }
  | null;

interface Download {
  id: string;
  name: string;
  runId: string;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function fetchList(): Promise<ModelList> {
  const l = await apiSend<ModelList>("GET", "/models");
  if (!l) throw new Error("empty answer");
  return l;
}

async function fetchRun(): Promise<SyncRun | null> {
  try {
    return await apiSend<SyncRun>("GET", "/ark/sync");
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

const BUTTON_LABEL: Record<ButtonName, Key> = {
  use: "models.use",
  unload: "models.unload",
  makeDefault: "models.makeDefault",
  download: "models.download",
  delete: "models.delete",
};
const BUTTON_ORDER: ButtonName[] = ["use", "unload", "makeDefault", "download", "delete"];

/** The buttons of a row; a disabled one names its reason (aria-describedby and a line below). */
function RowButtons({
  m,
  rules,
  onPress,
}: {
  m: ModelRow;
  rules: Record<ButtonName, ButtonRule>;
  onPress: (b: ButtonName) => void;
}) {
  const shown = BUTTON_ORDER.filter((b) => rules[b].show);
  const reasons: Key[] = [];
  for (const b of shown) {
    const r = rules[b];
    if (r.show && r.why && !reasons.includes(r.why)) reasons.push(r.why);
  }
  const reasonId = (why: Key) => `why-${m.id}-${reasons.indexOf(why)}`;
  return (
    <>
      <div className="actions model-actions">
        {shown.map((b) => {
          const r = rules[b];
          const why = r.show ? r.why : null;
          const cls =
            b === "delete"
              ? "btn"
              : "btn";
          return (
            <button
              key={b}
              type="button"
              className={cls}
              disabled={why !== null}
              aria-describedby={why ? reasonId(why) : undefined}
              onClick={() => onPress(b)}
            >
              {t(BUTTON_LABEL[b])}
            </button>
          );
        })}
      </div>
      {reasons.map((why) => (
        <p key={why} id={reasonId(why)} className="model-why">
          {t(why)}
        </p>
      ))}
    </>
  );
}

/** ModelsScreen is the AI models screen. */
export function ModelsScreen({ status }: { status: PortalStatus }) {
  useLang();
  const lang = getLang();
  const [list, setList] = useState<ModelList | null>(null);
  const [listFailure, setListFailure] = useState<Failure | null>(null);
  const [confirm, setConfirm] = useState<Confirm>(null);
  const [actingId, setActingId] = useState<string | null>(null);
  const [workingText, setWorkingText] = useState("");
  const [download, setDownload] = useState<Download | null>(null);
  const [percent, setPercent] = useState<number | null>(null);
  const action = useAction();
  const pin = usePin();
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const refresh = useCallback(async (): Promise<ModelList | null> => {
    try {
      const l = await fetchList();
      if (alive.current) {
        setList(l);
        setListFailure(null);
      }
      return l;
    } catch (e) {
      if (alive.current) setListFailure(failureOf(e));
      return null;
    }
  }, []);

  const anyLoading = list?.models.some((m) => m.status === "loading") ?? false;
  const fast = action.busy || download !== null || anyLoading;
  useEffect(() => {
    void refresh();
  }, [refresh]);
  useEffect(() => {
    const h = window.setInterval(() => void refresh(), fast ? FAST_MS : SLOW_MS);
    return () => window.clearInterval(h);
  }, [fast, refresh]);

  // Follow a download: its run on GET /ark/sync, and the model turning present on GET /models.
  useEffect(() => {
    if (!download) return;
    let stopped = false;
    const finish = (failure: Failure | null) => {
      stopped = true;
      setDownload(null);
      setPercent(null);
      setActingId(download.id);
      void action.run(async () => {
        if (failure) throw new ApiError(0, failure.code, failure.message, failure.fix);
      }, t("models.done.download", { name: download.name }));
    };
    const failed = (code: string): Failure => ({
      code,
      message: t("models.downloadFailed", { name: download.name }),
      fix: t("models.downloadFailedFix"),
    });
    const tick = async () => {
      if (stopped) return;
      let run: SyncRun | null = null;
      try {
        run = await fetchRun();
      } catch {
        run = null;
      }
      const l = await refresh();
      if (stopped || !alive.current) return;
      const m = l?.models.find((x) => x.id === download.id);
      if (m?.present) return finish(null);
      const v = downloadView(run, download.runId, download.id);
      if (v.kind === "running") setPercent(v.percent);
      else if (v.kind === "failed") finish(failed(v.code));
      else finish(failed(""));
    };
    const h = window.setInterval(() => void tick(), FAST_MS);
    return () => {
      stopped = true;
      window.clearInterval(h);
    };
  }, [download, refresh]);

  /** waitInUse polls the list until the model is in use, or fails as the brain switch does. */
  async function waitInUse(m: ModelRow): Promise<void> {
    const started = Date.now();
    let seenLoading = false;
    for (;;) {
      await sleep(FAST_MS);
      if (!alive.current) return;
      const l = await refresh();
      const now = l?.models.find((x) => x.id === m.id);
      if (now?.status === "loaded" || now?.status === "sleeping") return;
      if (now?.status === "loading") seenLoading = true;
      const gaveUp =
        now?.status === "failed" ||
        (seenLoading && now?.status !== "loading") ||
        // The router never reported it loading: give the watcher 30 s to see it.
        (!seenLoading && Date.now() - started > 30_000);
      if (gaveUp)
        throw new ApiError(
          500,
          CODE_LOAD_FAILED,
          t("models.loadFailed", { name: m.display_name }),
          t("models.loadFailedFix"),
        );
      if (Date.now() - started > LOAD_TIMEOUT_MS)
        throw new ApiError(
          504,
          CODE_LOAD_TIMEOUT,
          t("models.loadTimeout", { name: m.display_name }),
          t("models.loadFailedFix"),
        );
    }
  }

  function start(m: ModelRow, text = "") {
    setActingId(m.id);
    setWorkingText(text);
  }

  async function doUse(m: ModelRow) {
    start(m, t("models.working.use", { name: m.display_name }));
    await action.run(async () => {
      const r = await apiSend<{ status?: string }>(
        "POST",
        `/models/${encodeURIComponent(m.id)}/load`,
        { wait: false, unload_others: true },
      );
      if (r?.status !== "loaded") await waitInUse(m);
    }, t("models.done.use", { name: m.display_name }));
    void refresh();
  }

  async function doUnload(m: ModelRow) {
    start(m);
    await action.run(
      () => apiSend("POST", `/models/${encodeURIComponent(m.id)}/unload`, {}),
      t("models.done.unload", { name: m.display_name }),
    );
    void refresh();
  }

  async function doDefault(m: ModelRow) {
    start(m);
    await action.run(
      () => apiSend("PUT", "/models/default", { id: m.id }),
      t("models.done.default", { name: m.display_name }),
    );
    void refresh();
  }

  async function doDownload(m: ModelRow) {
    start(m);
    let runId = "";
    const ok = await action.run(async () => {
      const r = await apiSend<{ sync_run_id?: string }>(
        "POST",
        `/models/${encodeURIComponent(m.id)}/download`,
        {},
      );
      runId = r?.sync_run_id ?? "";
    }, t("models.done.downloadStarted", { name: m.display_name }));
    if (ok) setDownload({ id: m.id, name: m.display_name, runId });
  }

  /** doDelete: the PIN, then DELETE from this computer's disk; the last copy asks once more. */
  async function doDelete(m: ModelRow, keepLast: boolean) {
    const grant = await pin.ask(t("models.pinWhy", { name: m.display_name }));
    if (!grant) return;
    start(m);
    let lastCopy = false;
    await action.run(async () => {
      let r: { deleted?: unknown[] } | null;
      try {
        r = await apiSend<{ deleted?: unknown[] }>(
          "DELETE",
          deletePath(m.id, keepLast),
          undefined,
          grant,
        );
      } catch (e) {
        if (keepLast && e instanceof ApiError && e.code === CODE_LAST_COPY) {
          lastCopy = true;
          return;
        }
        throw e;
      }
      if (!r?.deleted || r.deleted.length === 0)
        throw new Error(t("models.notOnDisk", { name: m.display_name }));
    }, t("models.done.delete", { name: m.display_name }));
    if (lastCopy) {
      action.reset();
      setConfirm({ kind: "lastCopy", m });
      return;
    }
    void refresh();
  }

  function press(m: ModelRow, b: ButtonName) {
    action.reset();
    setActingId(m.id);
    switch (b) {
      case "use":
        setConfirm({ kind: "use", m });
        return;
      case "unload":
        setConfirm({ kind: "unload", m });
        return;
      case "makeDefault":
        void doDefault(m);
        return;
      case "download":
        setConfirm({ kind: "download", m });
        return;
      case "delete":
        setConfirm({ kind: "delete", m });
        return;
    }
  }

  function onConfirm() {
    if (!confirm) return;
    const { kind, m } = confirm;
    setConfirm(null);
    if (kind === "use") void doUse(m);
    else if (kind === "unload") void doUnload(m);
    else if (kind === "download") void doDownload(m);
    else if (kind === "delete") void doDelete(m, true);
    else void doDelete(m, false);
  }

  const survival = new Set(
    (status.brain.choices ?? []).filter((c) => c.survival).map((c) => c.id),
  );
  const size = (m: ModelRow) => formatSize(m.size_bytes, lang);

  /** row is called as a function (not a component) so a poll never remounts it and focus stays. */
  function row(m: ModelRow) {
    const downloading = download?.id === m.id;
    const rules = buttons(m, {
      mode: status.mode,
      busy: action.busy,
      downloading,
      downloadRunning: download !== null,
    });
    const state = downloading
      ? percent === null
        ? t("models.state.downloading")
        : t("models.state.downloadingPct", { pct: percent })
      : t(STATE_KEY[rowState(m)]);
    const s = size(m);
    const note = actingId === m.id ? action.phase : null;
    return (
      <li key={m.id} className="card model">
        <div className="card-row">
          <h2 className="card-title">{m.display_name}</h2>
          {m.default ? <span className="tag model-tag">{t("models.tag.default")}</span> : null}
          {survival.has(m.id) ? (
            <span className="tag model-tag">{t("models.tag.survival")}</span>
          ) : null}
          {isHelper(m) ? (
            <span className="tag model-tag">{t("models.tag.helper")}</span>
          ) : isBrain(m) ? null : (
            <span className="tag model-tag">{t("models.tag.speech")}</span>
          )}
        </div>
        <p className="model-state">
          <strong>{state}</strong>
          {s === "" ? null : <span>{t("models.size", { size: s })}</span>}
          {isBrain(m) && fits(m) ? <span>{t("models.fits")}</span> : null}
        </p>
        {isBrain(m) && !fits(m) ? (
          <p className="warn-text">
            {t("models.tooBig", { reason: m.loadable_reason?.message ?? "" })}
          </p>
        ) : null}
        {m.summary ? <p className="muted">{m.summary}</p> : null}
        {downloading ? (
          <progress
            className="meter meter-ok"
            aria-label={state}
            max={100}
            value={percent ?? undefined}
          />
        ) : null}
        <RowButtons m={m} rules={rules} onPress={(b) => press(m, b)} />
        {note?.kind === "working" ? (
          <div role="status" className="model-note">
            <p>{workingText || t("manage.working")}</p>
            <progress className="meter meter-ok" aria-label={t("manage.working")} />
          </div>
        ) : note?.kind === "done" ? (
          <p role="status" className="model-note model-done">
            {note.text}
          </p>
        ) : note?.kind === "failed" ? (
          <FailureNote failure={note.failure} />
        ) : null}
      </li>
    );
  }

  function dialog() {
    if (!confirm) return null;
    const { kind, m } = confirm;
    const name = m.display_name;
    const common = { onConfirm, onCancel: () => setConfirm(null), busy: action.busy };
    switch (kind) {
      case "use":
        return (
          <ConfirmDialog
            {...common}
            title={t("models.confirmUse.title", { name })}
            lines={[t("models.confirmUse.line1"), t("models.confirmUse.line2")]}
            confirmLabel={t("models.use")}
          />
        );
      case "unload":
        return (
          <ConfirmDialog
            {...common}
            title={t("models.confirmUnload.title", { name })}
            lines={[t("models.confirmUnload.line1")]}
            confirmLabel={t("models.unload")}
          />
        );
      case "download":
        return (
          <ConfirmDialog
            {...common}
            title={t("models.confirmDownload.title", { name })}
            lines={[
              t("models.confirmDownload.line1", { size: size(m) }),
              t("models.confirmDownload.line2"),
            ]}
            confirmLabel={t("models.download")}
          />
        );
      case "delete":
        return (
          <ConfirmDialog
            {...common}
            danger
            title={t("models.confirmDelete.title", { name })}
            lines={[
              t("models.confirmDelete.line1"),
              t("models.confirmDelete.line2", { size: size(m) }),
            ]}
            confirmLabel={t("models.delete")}
          />
        );
      case "lastCopy":
        return (
          <ConfirmDialog
            {...common}
            danger
            title={t("models.confirmLast.title")}
            lines={[
              t("models.confirmLast.line1", { name }),
              t("models.confirmLast.line2"),
            ]}
            confirmLabel={t("models.confirmLast.ok")}
          />
        );
    }
  }

  const g = list ? groups(list.models) : null;
  const inUse = list?.models.filter((m) => rowState(m) === "in_use") ?? [];
  const def = list?.models.find((m) => m.default);

  return (
    <ManageFrame title={t("models.title")} lead={t("models.lead")}>
      {list === null && listFailure === null ? <p>{t("common.loading")}</p> : null}
      {listFailure ? (
        <div className="panel">
          <p className="lead">{t("models.listFailed")}</p>
          <FailureNote failure={listFailure} />
          <div className="actions">
            <button type="button" className="btn btn-primary" onClick={() => void refresh()}>
              {t("models.retry")}
            </button>
          </div>
        </div>
      ) : null}
      {list ? (
        <div className="panel model-summary" role="status">
          <p className="lead">
            {inUse.length > 0
              ? t("models.inUseNow", { name: inUse.map((m) => m.display_name).join(", ") })
              : t("models.noneInUse")}
          </p>
          <p>{def ? t("models.defaultIs", { name: def.display_name }) : t("models.noDefault")}</p>
        </div>
      ) : null}
      {g && g.brains.length + g.more.length + g.speech.length === 0 ? (
        <p>{t("models.empty")}</p>
      ) : null}
      {g ? (
        <ul className="model-list">
          {g.brains.map((m) => row(m))}
        </ul>
      ) : null}
      {g && g.more.length > 0 ? (
        <details className="expert model-more">
          <summary>{t("models.more", { n: g.more.length })}</summary>
          <ul className="model-list">
            {g.more.map((m) => row(m))}
          </ul>
        </details>
      ) : null}
      {g && g.speech.length > 0 ? (
        <>
          <h2 className="model-group">{t("models.speech")}</h2>
          <ul className="model-list">
            {g.speech.map((m) => row(m))}
          </ul>
        </>
      ) : null}
      {dialog()}
      {pin.dialog}
    </ManageFrame>
  );
}
