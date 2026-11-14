import { useCallback, useEffect, useState } from "react";
import { ApiError, failureOf, type Failure } from "../api";
import { EnsureCard } from "../components/EnsureCard";
import { durationText } from "../format";
import { t, useLang, type Key, type Lang } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import "../styles/status-brain.css";
import { ConfirmDialog } from "./ConfirmDialog";
import { FailureNote } from "./FailureNote";
import { ManageFrame } from "./ManageFrame";
import { usePin } from "./PinDialog";
import { useAction, type ActionPhase } from "./useAction";
import {
  activeWords,
  blockedSet,
  blockersOf,
  copyBlocked,
  dateText,
  driveName,
  driveWords,
  ejectBlocked,
  ejectOutcome,
  groupItems,
  itemWords,
  lastSyncLine,
  rootLine,
  rootName,
  runActive,
  runEnded,
  runProgress,
  runTitle,
  sizeText,
  syncBlocked,
  type Drive,
  type EjectBlocker,
  type PruneResult,
  type ReadyReport,
  type SyncRun,
  type Tone,
} from "./content";
import { contentApi, loadContent, type ContentData } from "./contentApi";

/** How often the screen rereads the agent: fast while work runs, slow otherwise. */
const FAST_MS = 2000;
const SLOW_MS = 10000;
/** How long the screen waits for an accepted eject to finish before it says it did not. */
const EJECT_WAIT_MS = 120_000;

const MARK: Record<Tone, string> = { ok: "✓", warn: "!", bad: "✗" };

type Ask = (why?: string) => Promise<string | null>;

/** The data of the screen, reread every 2 s while a run is active or a drive is ejecting. */
function useContentData(lang: Lang) {
  const [data, setData] = useState<ContentData | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const reload = useCallback(async () => {
    try {
      setData(await loadContent());
      setFailure(null);
    } catch (e) {
      setFailure(failureOf(e));
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload, lang]);
  const fast =
    runActive(data?.run ?? null) || (data?.drives ?? []).some((d) => d.state === "ejecting");
  useEffect(() => {
    const h = window.setInterval(() => void reload(), fast ? FAST_MS : SLOW_MS);
    return () => window.clearInterval(h);
  }, [reload, fast]);
  return { data, failure, reload };
}

/** The Offline content and drives screen (dashboard.md section 4.3, issue #496). */
export function ContentScreen({ status }: { status: PortalStatus }) {
  const lang = useLang();
  const { data, failure, reload } = useContentData(lang);
  const pin = usePin();
  const running = runActive(data?.run ?? null);
  return (
    <ManageFrame title={t("content.title")} lead={leadOf(data, status.mode)}>
      {failure ? <FailureNote failure={failure} /> : null}
      {data ? (
        <>
          <ReadinessCard roots={data.roots} />
          <RunCard mode={status.mode} run={data.run} reload={reload} />
          <DrivesCard drives={data.drives} running={running} reload={reload} ask={pin.ask} />
          <ItemsCard roots={data.roots} run={data.run} />
          <EnsureCard />
          <PruneCard running={running} reload={reload} ask={pin.ask} />
        </>
      ) : failure ? null : (
        <p role="status">{t("common.loading")}</p>
      )}
      {pin.dialog}
    </ManageFrame>
  );
}

/** The state-first sentence: everything is here, or what is missing and the one next step. */
function leadOf(data: ContentData | null, mode: string): string | undefined {
  if (!data) return undefined;
  if (data.roots.length === 0) return t("content.lead.noRoot");
  const complete = data.roots.every((r) =>
    r.items.every((i) => i.status === "verified" || i.status === "stale" || i.status === "orphan"),
  );
  if (complete) return t("content.lead.ok");
  if (mode === "online") return t("content.lead.nextSync");
  if (data.drives.some((d) => copyBlocked(d, false) === null)) return t("content.lead.nextCopy");
  return t("content.lead.nextConnect");
}

function ReadinessCard({ roots }: { roots: ReadyReport[] }) {
  return (
    <section className="card" aria-labelledby="content-ready-title">
      <h2 id="content-ready-title" className="card-title">
        {t("content.ready.title")}
      </h2>
      <ul className="content-roots">
        {roots.map((r) => (
          <li key={r.root.id} className={`content-root content-${toneOf(r.overall)}`}>
            <span className="mark" aria-hidden="true">
              {MARK[toneOf(r.overall)]}
            </span>
            <span>
              <strong>{rootLine(r)}</strong>
              <span className="block muted">{lastSyncLine(r)}</span>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function toneOf(c: string): Tone {
  return c === "green" ? "ok" : c === "yellow" ? "warn" : "bad";
}

/** The line under a button: working, done, or the failure with its code and fix. */
function Phase({ phase }: { phase: ActionPhase }) {
  if (phase.kind === "working")
    return (
      <p className="fix-result" role="status">
        {t("manage.working")}
      </p>
    );
  if (phase.kind === "done")
    return (
      <p className="fix-result" role="status">
        {phase.text}
      </p>
    );
  if (phase.kind === "failed") return <FailureNote failure={phase.failure} />;
  return null;
}

/** "Sync now: Needs the internet" — the reason of a disabled button, always on screen. */
function Why({ id, button, why }: { id: string; button: Key; why: Key | null }) {
  if (why === null) return null;
  return (
    <p id={id} className="muted content-why">
      {t("content.whyLine", { button: t(button), why: t(why) })}
    </p>
  );
}

function RunCard({ mode, run, reload }: { mode: string; run: SyncRun | null; reload: () => Promise<void> }) {
  const act = useAction();
  const running = runActive(run);
  const syncWhy = syncBlocked(mode, running);
  const verifyWhy: Key | null = running ? "content.why.busy" : null;
  async function go(work: () => Promise<unknown>, done: string) {
    await act.run(work, done);
    await reload();
  }
  return (
    <section className="card" aria-labelledby="content-run-title">
      <h2 id="content-run-title" className="card-title">
        {t("content.run.title")}
      </h2>
      {run && running ? <RunProgress run={run} /> : null}
      {run && !running ? <LastRun run={run} /> : null}
      {!run ? <p className="muted">{t("content.run.none")}</p> : null}
      <div className="actions">
        {running ? (
          <button
            type="button"
            className="btn"
            disabled={act.busy || run?.state === "cancelling"}
            onClick={() => void go(() => contentApi.stop(), t("content.stop.done"))}
          >
            {t("content.stop")}
          </button>
        ) : (
          <button
            type="button"
            className="btn btn-primary"
            disabled={act.busy || syncWhy !== null}
            aria-describedby={syncWhy ? "content-sync-why" : undefined}
            onClick={() => void go(() => contentApi.sync(), t("content.sync.started"))}
          >
            {t("content.sync")}
          </button>
        )}
        <button
          type="button"
          className="btn"
          disabled={act.busy || verifyWhy !== null}
          aria-describedby={verifyWhy ? "content-verify-why" : undefined}
          onClick={() => void go(() => contentApi.verify(), t("content.verify.started"))}
        >
          {t("content.verify")}
        </button>
      </div>
      {running ? null : <Why id="content-sync-why" button="content.sync" why={syncWhy} />}
      <Why id="content-verify-why" button="content.verify" why={verifyWhy} />
      <p className="muted">{t("content.verify.what")}</p>
      <Phase phase={act.phase} />
    </section>
  );
}

function RunProgress({ run }: { run: SyncRun }) {
  const p = runProgress(run);
  const eta = run.totals?.eta_s ?? 0;
  return (
    <div className="content-progress">
      <p>
        <strong>{runTitle(run)}</strong>
      </p>
      {p ? (
        <div className="meter-row">
          <progress
            className="meter"
            max={100}
            value={p.pct}
            aria-label={t("content.progress", { done: p.done, total: p.total, pct: p.pct })}
          />
          <span className="meter-text">{t("content.progress", { done: p.done, total: p.total, pct: p.pct })}</span>
        </div>
      ) : null}
      {eta > 0 ? <p className="muted">{t("content.eta", { time: durationText(eta / 60) })}</p> : null}
      {(run.active ?? []).length > 0 ? (
        <ul className="content-active">
          {(run.active ?? []).map((a) => (
            <li key={`${a.root}/${a.item}`}>
              <strong>{a.item}</strong>
              {` · ${activeWords(a.state)}`}
              {a.bytes_total > 0
                ? ` · ${t("content.active.bytes", {
                    done: sizeText(a.bytes_done),
                    total: sizeText(a.bytes_total),
                  })}`
                : ""}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function LastRun({ run }: { run: SyncRun }) {
  const end = runEnded(run);
  const when = dateText(run.finished_at);
  return (
    <div className={`content-last content-${end.tone}`}>
      <p>
        <span className="mark" aria-hidden="true">
          {MARK[end.tone]}
        </span>
        <span>{end.text}</span>
      </p>
      {when === "" ? null : <p className="muted">{t("content.run.when", { when })}</p>}
      {run.code ? <p className="code">{t("common.code", { code: run.code })}</p> : null}
    </div>
  );
}

function ItemsCard({ roots, run }: { roots: ReadyReport[]; run: SyncRun | null }) {
  const blocked = blockedSet(run);
  return (
    <section className="card" aria-labelledby="content-items-title">
      <h2 id="content-items-title" className="card-title">
        {t("content.items.title")}
      </h2>
      {roots.map((r, n) => (
        <details key={r.root.id} className="details content-rootitems" open={n === 0}>
          <summary>{t("content.items.on", { name: rootName(r.root), n: r.items.length })}</summary>
          {r.items.length === 0 ? <p className="muted">{t("content.items.empty")}</p> : null}
          {groupItems(r.items).map((g) => (
            <div key={g.group} className="content-group">
              <h3 className="content-group-title">{t(`content.group.${g.group}` as Key)}</h3>
              <ul className="content-list">
                {g.items.map((i) => {
                  const w = itemWords(i, blocked.has(`${r.root.id}/${i.id}`));
                  const problem = r.problems.find((p) => p.item === i.id);
                  const size = sizeText(i.bytes);
                  return (
                    <li key={i.id} className={`content-item content-${w.tone}`}>
                      <span className="mark" aria-hidden="true">
                        {MARK[w.tone]}
                      </span>
                      <span className="content-item-body">
                        <strong>{i.id}</strong>
                        {` · ${t(w.key)}`}
                        {size === "" ? "" : ` · ${size}`}
                        {problem ? <span className="block">{problem.title}</span> : null}
                        {i.code ? <span className="block code">{t("common.code", { code: i.code })}</span> : null}
                      </span>
                    </li>
                  );
                })}
              </ul>
            </div>
          ))}
        </details>
      ))}
    </section>
  );
}

type EjectWatch = { name: string; phase: "waiting" | "safe" | "failed"; since: number };

function DrivesCard({
  drives,
  running,
  reload,
  ask,
}: {
  drives: Drive[];
  running: boolean;
  reload: () => Promise<void>;
  ask: Ask;
}) {
  const [watch, setWatch] = useState<Record<string, EjectWatch>>({});
  const waiting = Object.values(watch).some((w) => w.phase === "waiting");

  // Follow an accepted eject in the drive list until the drive is gone (safe to unplug) or back.
  useEffect(() => {
    setWatch((cur) => {
      let changed = false;
      const next = { ...cur };
      for (const [id, w] of Object.entries(cur)) {
        if (w.phase !== "waiting") continue;
        let out = ejectOutcome(drives, id);
        if (out === "waiting" && Date.now() - w.since > EJECT_WAIT_MS) out = "failed";
        if (out !== "waiting") {
          next[id] = { ...w, phase: out };
          changed = true;
        }
      }
      return changed ? next : cur;
    });
  }, [drives]);
  useEffect(() => {
    if (!waiting) return;
    const h = window.setInterval(() => void reload(), FAST_MS);
    return () => window.clearInterval(h);
  }, [waiting, reload]);

  const ejected = Object.entries(watch).filter(([id]) => !drives.some((d) => d.root_id === id && d.mount !== null));
  return (
    <section className="card" aria-labelledby="content-drives-title">
      <h2 id="content-drives-title" className="card-title">
        {t("content.drives.title")}
      </h2>
      {drives.length === 0 ? <p>{t("content.drives.none")}</p> : null}
      <ul className="content-drives">
        {drives.map((d, n) => (
          <DriveRow
            key={d.root_id ?? `${d.device ?? ""}-${n}`}
            drive={d}
            running={running}
            reload={reload}
            ask={ask}
            watch={d.root_id ? watch[d.root_id] : undefined}
            onEjecting={(id, name) =>
              setWatch((cur) => ({ ...cur, [id]: { name, phase: "waiting", since: Date.now() } }))
            }
          />
        ))}
      </ul>
      {ejected.map(([id, w]) =>
        w.phase === "safe" ? (
          <p key={id} className="fix-result content-safe" role="status">
            {t("content.eject.safe", { name: w.name })}
          </p>
        ) : null,
      )}
    </section>
  );
}

function DriveRow({
  drive: d,
  running,
  reload,
  ask,
  watch,
  onEjecting,
}: {
  drive: Drive;
  running: boolean;
  reload: () => Promise<void>;
  ask: Ask;
  watch?: EjectWatch;
  onEjecting: (rootId: string, name: string) => void;
}) {
  const act = useAction();
  const [blockers, setBlockers] = useState<EjectBlocker[]>([]);
  const [confirm, setConfirm] = useState<"init" | "force" | null>(null);
  const name = driveName(d);
  const words = driveWords(d);
  const rootId = d.root_id;
  const copyWhy = copyBlocked(d, running);
  const ejectWhy = ejectBlocked(d);
  const idBase = `drive-${rootId ?? d.device ?? "x"}`.replace(/[^A-Za-z0-9_-]/g, "-");

  async function eject(force: boolean) {
    if (!rootId) return;
    setBlockers([]);
    const ok = await act.run(async () => {
      try {
        await contentApi.eject(rootId, force);
      } catch (e) {
        if (e instanceof ApiError && e.code === "LAI-ARK-009") setBlockers(blockersOf(e.details));
        throw e;
      }
    }, t("content.eject.started"));
    if (ok) onEjecting(rootId, name);
    await reload();
  }

  async function init() {
    setConfirm(null);
    if (!rootId) return;
    const grant = await ask(t("content.init.pinWhy"));
    if (!grant) return;
    await act.run(() => contentApi.init(rootId, grant), t("content.init.done"));
    await reload();
  }

  async function simple(work: () => Promise<unknown>, done: string) {
    await act.run(work, done);
    await reload();
  }

  return (
    <li className={`card content-drive content-${words.tone}`}>
      <h3 className="content-drive-name">{name}</h3>
      <p>
        <span className="mark" aria-hidden="true">
          {MARK[words.tone]}
        </span>
        <span>{t(words.key)}</span>
      </p>
      {d.mount !== null && d.free_bytes !== undefined ? (
        <p className="muted">{t("content.drive.free", { free: sizeText(d.free_bytes) })}</p>
      ) : null}
      {(d.problems ?? []).map((p) => (
        <div key={p.code} className="problem">
          <p>{p.title}</p>
          {p.fix ? <p>{p.fix}</p> : null}
          <p className="code">{t("common.code", { code: p.code })}</p>
        </div>
      ))}
      <div className="actions">
        {rootId && copyWhy !== "content.why.notMounted" && copyWhy !== "content.why.empty" ? (
          <button
            type="button"
            className="btn btn-primary"
            disabled={act.busy || copyWhy !== null}
            aria-describedby={copyWhy ? `${idBase}-copy-why` : undefined}
            onClick={() => void simple(() => contentApi.importFrom(rootId), t("content.copy.started"))}
          >
            {t("content.copy")}
          </button>
        ) : null}
        {rootId && d.state === "not_mounted" ? (
          <button
            type="button"
            className="btn"
            disabled={act.busy}
            onClick={() => void simple(() => contentApi.mount(rootId), t("content.mount.done"))}
          >
            {t("content.mount")}
          </button>
        ) : null}
        {rootId && d.state === "uninitialized" ? (
          <button type="button" className="btn" disabled={act.busy} onClick={() => setConfirm("init")}>
            {t("content.init")}
          </button>
        ) : null}
        {d.mount !== null ? (
          <button
            type="button"
            className="btn"
            disabled={act.busy || ejectWhy !== null}
            aria-describedby={ejectWhy ? `${idBase}-eject-why` : undefined}
            onClick={() => void eject(false)}
          >
            {t("content.eject")}
          </button>
        ) : null}
      </div>
      {copyWhy !== "content.why.notMounted" && copyWhy !== "content.why.empty" ? (
        <Why id={`${idBase}-copy-why`} button="content.copy" why={copyWhy} />
      ) : null}
      {d.mount !== null ? <Why id={`${idBase}-eject-why`} button="content.eject" why={ejectWhy} /> : null}
      <Phase phase={act.phase} />
      {watch?.phase === "waiting" ? (
        <p className="fix-result" role="status">
          {t("content.eject.waiting")}
        </p>
      ) : null}
      {watch?.phase === "failed" && d.mount !== null ? (
        <div className="fix-result fix-failed" role="alert">
          <p>{t("content.eject.notDone")}</p>
          <p className="code">{t("common.code", { code: "LAI-ARK-009" })}</p>
        </div>
      ) : null}
      {blockers.length > 0 ? (
        <div className="content-blockers">
          <p>{t("content.eject.blockers", { names: blockers.map((b) => b.name).join(", ") })}</p>
          {blockers.some((b) => b.kind === "model") ? (
            <button type="button" className="btn btn-danger" disabled={act.busy} onClick={() => setConfirm("force")}>
              {t("content.eject.force")}
            </button>
          ) : null}
        </div>
      ) : null}
      {confirm === "init" ? (
        <ConfirmDialog
          title={t("content.init.confirmTitle", { name })}
          lines={[t("content.init.confirm1"), t("content.init.confirm2"), t("content.init.confirm3")]}
          confirmLabel={t("content.init.confirmOk")}
          onConfirm={() => void init()}
          onCancel={() => setConfirm(null)}
        />
      ) : null}
      {confirm === "force" ? (
        <ConfirmDialog
          title={t("content.eject.forceTitle")}
          lines={[t("content.eject.force1"), t("content.eject.force2")]}
          confirmLabel={t("content.eject.force")}
          danger
          onConfirm={() => {
            setConfirm(null);
            void eject(true);
          }}
          onCancel={() => setConfirm(null)}
        />
      ) : null}
    </li>
  );
}

function PruneCard({ running, reload, ask }: { running: boolean; reload: () => Promise<void>; ask: Ask }) {
  const act = useAction();
  const [plan, setPlan] = useState<PruneResult | null>(null);
  const why: Key | null = running ? "content.why.busy" : null;

  async function check() {
    setPlan(null);
    const grant = await ask(t("content.prune.pinWhy"));
    if (!grant) return;
    let found: PruneResult | null = null;
    const ok = await act.run(async () => {
      found = await contentApi.prune(true, grant);
    }, t("content.prune.checked"));
    const p = found as PruneResult | null;
    if (!ok || !p) return;
    if (p.deleted.length === 0) {
      await act.run(async () => undefined, t("content.prune.nothing"));
      return;
    }
    act.reset();
    setPlan(p);
  }

  async function remove() {
    setPlan(null);
    const grant = await ask(t("content.prune.pinWhy"));
    if (!grant) return;
    let freed = 0;
    const ok = await act.run(async () => {
      const r = await contentApi.prune(false, grant);
      freed = r?.freed_bytes ?? 0;
    });
    // The sentence needs the answer, so it is shown once the call is back.
    if (ok) await act.run(async () => undefined, t("content.prune.done", { size: sizeText(freed) }));
    await reload();
  }

  const total = plan ? plan.deleted.reduce((s, d) => s + d.bytes, 0) : 0;
  return (
    <section className="card" aria-labelledby="content-prune-title">
      <h2 id="content-prune-title" className="card-title">
        {t("content.prune.title")}
      </h2>
      <p>{t("content.prune.what")}</p>
      <div className="actions">
        <button
          type="button"
          className="btn"
          disabled={act.busy || why !== null}
          aria-describedby={why ? "content-prune-why" : undefined}
          onClick={() => void check()}
        >
          {t("content.prune")}
        </button>
      </div>
      <Why id="content-prune-why" button="content.prune" why={why} />
      <Phase phase={act.phase} />
      {plan ? (
        <ConfirmDialog
          title={t("content.prune.confirmTitle")}
          lines={[
            plan.deleted.length === 1
              ? t("content.prune.confirmOne", { size: sizeText(total) })
              : t("content.prune.confirm1", { n: plan.deleted.length, size: sizeText(total) }),
            t("content.prune.confirm2"),
          ]}
          confirmLabel={t("content.prune.confirmOk")}
          danger
          onConfirm={() => void remove()}
          onCancel={() => setPlan(null)}
        >
          <ul className="content-prune-list">
            {plan.deleted.map((d) => (
              <li key={`${d.root}/${d.item}/${d.version ?? ""}/${d.kind}`}>
                {`${d.item} · ${t(d.kind === "trash" ? "content.prune.kindTrash" : "content.prune.kindOrphan")} · ${sizeText(d.bytes)}`}
              </li>
            ))}
          </ul>
          {plan.refused.length > 0 ? (
            <p>{t("content.prune.kept", { names: plan.refused.map((r) => r.item).join(", ") })}</p>
          ) : null}
        </ConfirmDialog>
      ) : null}
    </section>
  );
}
