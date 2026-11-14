import { useCallback, useEffect, useRef, useState } from "react";
import "../styles/status-brain.css";
import { ApiError, apiSend, failureOf, type Failure } from "../api";
import { isFixAction } from "../actions";
import { FixButton } from "../components/FixButton";
import { announce } from "../components/LiveRegion";
import { clockText } from "../format";
import { t, useLang, type Key } from "../i18n/i18n";
import type { PortalStatus } from "../status";
import { ConfirmDialog } from "./ConfirmDialog";
import { FailureNote } from "./FailureNote";
import { ManageFrame } from "./ManageFrame";
import { useAction } from "./useAction";
import {
  DRILL_POLL_MS,
  LOG_LINES,
  LOG_UNITS,
  checkName,
  doctorView,
  drillView,
  eventSentences,
  type DoctorReport,
  type DoctorResult,
  type DrillCheck,
  type DrillDoc,
  type EventSentence,
  type LogUnit,
  type LogsResponse,
} from "./health";

// The Health screen (dashboard.md section 4.5, issue #499): a full check (GET /doctor), the recent
// events (GET /logs) and the practice drill (POST/GET/DELETE /drill).

const LEVEL_MARK = { error: "✗", warning: "!", info: "i", debug: "·" } as const;
const SEV_MARK = { critical: "✗", error: "✗", warning: "!", info: "i" } as const;

/** A full check opened again within this time shows the last report instead of running again. */
const REPORT_FRESH_MS = 5 * 60 * 1000;
let lastReport: { rep: DoctorReport; at: number } | null = null;

function useSeconds(on: boolean): number {
  const [s, setS] = useState(0);
  useEffect(() => {
    if (!on) return;
    setS(0);
    const start = Date.now();
    const id = window.setInterval(() => setS(Math.floor((Date.now() - start) / 1000)), 1000);
    return () => window.clearInterval(id);
  }, [on]);
  return s;
}

// ---------------------------------------------------------------------------------------------------

function Problem({ r }: { r: DoctorResult }) {
  return (
    <li className={`problem problem-${r.severity}`}>
      <span className="mark" aria-hidden="true">
        {SEV_MARK[r.severity] ?? "!"}
      </span>
      <span className="sr-only">{t(`health.sev.${r.severity}` as Key)}</span>
      <span className="code">{r.code}</span>
      <span className="block health-problem-title">{r.title}</span>
      <span className="block">{r.fix}</span>
      {isFixAction(r.action) ? <FixButton action={r.action} /> : null}
    </li>
  );
}

/** Run a full check: the doctor's report in plain words, each problem with its fix button. */
function CheckCard() {
  const [rep, setRep] = useState<{ rep: DoctorReport; at: number } | null>(lastReport);
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState<Failure | null>(null);
  const seconds = useSeconds(working);
  const running = useRef(false);

  const run = useCallback(async () => {
    if (running.current) return;
    running.current = true;
    setWorking(true);
    setFailure(null);
    announce(t("health.check.working"));
    try {
      const body = await apiSend<DoctorReport>("GET", "/doctor");
      if (body) {
        lastReport = { rep: body, at: Date.now() };
        setRep(lastReport);
        const v = doctorView(body);
        announce(v.allOk ? t("health.check.allOk") : t("health.check.problems", { n: v.problems.length }));
      }
    } catch (e) {
      const f = failureOf(e);
      setFailure(f);
      announce(t("manage.failed", { message: f.message }), true);
    } finally {
      running.current = false;
      setWorking(false);
    }
  }, []);

  // State first (ADR-0009): opening the screen runs the check, unless one ran a moment ago.
  useEffect(() => {
    if (!lastReport || Date.now() - lastReport.at > REPORT_FRESH_MS) void run();
  }, [run]);

  const view = rep ? doctorView(rep.rep) : null;
  return (
    <section className="card" aria-labelledby="health-check-title">
      <h2 id="health-check-title" className="card-title">
        {t("health.check.title")}
      </h2>
      {working ? (
        <div role="status" className="health-working">
          <p>{t("health.check.working")}</p>
          <progress className="health-progress" aria-label={t("health.check.progress")} />
          <p className="muted">{t("health.check.elapsed", { s: seconds })}</p>
        </div>
      ) : null}
      {failure ? <FailureNote failure={failure} /> : null}
      {view && rep && !working ? (
        <>
          <p className="muted">{t("health.check.when", { time: clockText(rep.rep.ran_at) })}</p>
          {view.allOk ? (
            <p className="health-ok">{t("health.check.allOk")}</p>
          ) : (
            <p className="health-bad">{t("health.check.problems", { n: view.problems.length })}</p>
          )}
          <p>{t("health.check.passed", { n: view.passed })}</p>
          {view.problems.length > 0 ? (
            <ul className="problems">
              {view.problems.map((r) => (
                <Problem key={`${r.check}-${r.code}`} r={r} />
              ))}
            </ul>
          ) : null}
          {view.notes.length > 0 ? (
            <>
              <h3 className="health-h3">{t("health.check.notes")}</h3>
              <ul className="problems">
                {view.notes.map((r) => (
                  <Problem key={`${r.check}-${r.code}`} r={r} />
                ))}
              </ul>
            </>
          ) : null}
        </>
      ) : null}
      <div className="card-row">
        <button type="button" className="btn btn-primary" disabled={working} onClick={() => void run()}>
          {rep ? t("health.check.again") : t("health.check.run")}
        </button>
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------------------------------

function Event({ e }: { e: EventSentence }) {
  return (
    <li className={`health-event health-event-${e.level}`}>
      <span className="mark" aria-hidden="true">
        {LEVEL_MARK[e.level]}
      </span>
      <span className="sr-only">{t(`health.level.${e.level}` as Key)}</span>
      <span className="health-event-time">
        {e.at ? clockText(e.at.toISOString()) : t("health.events.noTime")}
      </span>
      <span className="health-event-text">{e.text}</span>
      {e.code === "" ? null : <span className="code">{t("common.code", { code: e.code })}</span>}
    </li>
  );
}

/** Show recent events: the last 50 journal entries of one part of Local AI, as sentences. */
function EventsCard() {
  const [unit, setUnit] = useState<LogUnit>("agent");
  const [events, setEvents] = useState<{ list: EventSentence[]; truncated: boolean } | null>(null);
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState<Failure | null>(null);

  async function show() {
    setWorking(true);
    setFailure(null);
    announce(t("health.events.working"));
    try {
      const body = await apiSend<LogsResponse>("GET", `/logs?unit=${unit}&lines=${LOG_LINES}`);
      const list = body ? eventSentences(body) : [];
      setEvents({ list, truncated: !!body?.truncated });
      announce(list.length === 0 ? t("health.events.none") : t("health.events.count", { n: list.length }));
    } catch (e) {
      const f = failureOf(e);
      setFailure(f);
      announce(t("manage.failed", { message: f.message }), true);
    } finally {
      setWorking(false);
    }
  }

  return (
    <section className="card" aria-labelledby="health-events-title">
      <h2 id="health-events-title" className="card-title">
        {t("health.events.title")}
      </h2>
      <div className="card-row">
        <label className="health-unit">
          <span className="field-label">{t("health.events.of")}</span>
          <select
            className="health-select"
            value={unit}
            onChange={(e) => {
              setUnit(e.target.value as LogUnit);
              setEvents(null);
            }}
          >
            {LOG_UNITS.map((u) => (
              <option key={u} value={u}>
                {t(`health.unit.${u}` as Key)}
              </option>
            ))}
          </select>
        </label>
        <button type="button" className="btn btn-primary" disabled={working} onClick={() => void show()}>
          {t("health.events.show")}
        </button>
      </div>
      {working ? (
        <p role="status" className="fix-result">
          {t("health.events.working")}
        </p>
      ) : null}
      {failure ? <FailureNote failure={failure} /> : null}
      {events && !working ? (
        events.list.length === 0 ? (
          <p>{t("health.events.none")}</p>
        ) : (
          <>
            <p className="muted">{t("health.events.count", { n: events.list.length })}</p>
            {events.truncated ? <p className="muted">{t("health.events.cut")}</p> : null}
            <ul className="health-events">
              {events.list.map((e, i) => (
                <Event key={i} e={e} />
              ))}
            </ul>
          </>
        )
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------------------------------

function checkLabel(c: DrillCheck): string {
  const n = checkName(c.id);
  if (n.family === "") return c.id;
  const name = t(`health.drl.${n.family}` as Key);
  if (n.suffix === "") return name;
  const suffix = n.suffix === "en" || n.suffix === "es" ? t(`health.lang.${n.suffix}` as Key) : n.suffix;
  return `${name} (${suffix})`;
}

function DrillResult({ doc }: { doc: DrillDoc }) {
  const v = drillView(doc);
  const when = v.finishedAt ?? v.startedAt;
  return (
    <div className={`health-drill health-drill-${v.outcome}`}>
      {when ? <p className="muted">{t("health.drill.last", { time: clockText(when.toISOString()) })}</p> : null}
      <p className="health-drill-outcome">{t(`health.out.${v.outcome}` as Key)}</p>
      {v.reason === "" ? null : <p>{t("health.drill.reason", { reason: v.reason })}</p>}
      {v.code === "" ? null : <p className="code">{t("common.code", { code: v.code })}</p>}
      {v.outcome === "pass" || v.outcome === "warn" || v.outcome === "fail" ? (
        <>
          <p>{t("health.drill.counts", { ...v.counts })}</p>
          <p>{v.survivalReady ? t("health.drill.ready") : t("health.drill.notReady")}</p>
          {v.quick ? <p className="muted">{t("health.drill.short")}</p> : null}
        </>
      ) : null}
      {v.attention.length > 0 && v.outcome !== "running" ? (
        <>
          <h3 className="health-h3">{t("health.drill.attention")}</h3>
          <ul className="problems">
            {v.attention.map((c) => (
              <li key={c.id} className={`problem problem-${c.result === "fail" ? "error" : "warning"}`}>
                <span className="mark" aria-hidden="true">
                  {c.result === "fail" ? "✗" : "!"}
                </span>
                <span className="sr-only">{t(`health.res.${c.result}` as Key)}</span>
                <span className="block health-problem-title">{checkLabel(c)}</span>
                {c.code ? <span className="code">{t("common.code", { code: c.code })}</span> : null}
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </div>
  );
}

type DrillState =
  | { kind: "loading" }
  | { kind: "none" }
  | { kind: "doc"; doc: DrillDoc }
  | { kind: "failed"; failure: Failure };

/** Run a practice drill (drill.md): the short drill, progress while it runs, Stop, the last result. */
function DrillCard({ survival }: { survival: boolean }) {
  const [state, setState] = useState<DrillState>({ kind: "loading" });
  const [runId, setRunId] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  const [stopping, setStopping] = useState(false);
  const start = useAction();
  const stop = useAction();

  // The last result, and a drill already running (the scheduled one, or one started elsewhere).
  useEffect(() => {
    let alive = true;
    apiSend<DrillDoc>("GET", "/drill")
      .then((doc) => {
        if (!alive) return;
        if (!doc) return setState({ kind: "none" });
        setState({ kind: "doc", doc });
        if (doc.state === "running") setRunId(doc.run_id);
      })
      .catch((e: unknown) => {
        if (!alive) return;
        if (e instanceof ApiError && e.status === 404) setState({ kind: "none" });
        else setState({ kind: "failed", failure: failureOf(e) });
      });
    return () => {
      alive = false;
    };
  }, []);

  // While a run is active, ask for its document every 2 s until it is no longer running.
  useEffect(() => {
    if (!runId) return;
    let alive = true;
    let timer = 0;
    const tick = async () => {
      try {
        const doc = await apiSend<DrillDoc>("GET", `/drill?run_id=${encodeURIComponent(runId)}`);
        if (!alive || !doc) return;
        setState({ kind: "doc", doc });
        if (doc.state !== "running") {
          setRunId(null);
          setStopping(false);
          announce(t(`health.out.${drillView(doc).outcome}` as Key));
          return;
        }
      } catch {
        // A missed answer: try again on the next tick.
      }
      if (alive) timer = window.setTimeout(() => void tick(), DRILL_POLL_MS);
    };
    void tick();
    return () => {
      alive = false;
      window.clearTimeout(timer);
    };
  }, [runId]);

  async function confirmStart() {
    setAsking(false);
    await start.run(async () => {
      const r = await apiSend<{ run_id: string }>("POST", "/drill", { quick: true });
      if (r?.run_id) setRunId(r.run_id);
    }, t("health.drill.started"));
  }

  async function doStop() {
    setStopping(true);
    const ok = await stop.run(() => apiSend("DELETE", "/drill"), t("health.drill.stopping"));
    if (!ok) setStopping(false);
  }

  const running = runId !== null || (state.kind === "doc" && state.doc.state === "running");
  const live = state.kind === "doc" && state.doc.state === "running" ? drillView(state.doc) : null;

  return (
    <section className="card" aria-labelledby="health-drill-title">
      <h2 id="health-drill-title" className="card-title">
        {t("health.drill.title")}
      </h2>
      <p>{t("health.drill.what")}</p>
      {running ? (
        <div role="status" className="health-working">
          <p>{stopping ? t("health.drill.stopping") : t("health.drill.running", { n: live?.done ?? 0 })}</p>
          <progress className="health-progress" aria-label={t("health.drill.progress")} />
          {live?.startedAt ? (
            <p className="muted">{t("health.drill.startedAt", { time: clockText(live.startedAt.toISOString()) })}</p>
          ) : null}
        </div>
      ) : null}
      {!running && state.kind === "none" ? <p>{t("health.drill.none")}</p> : null}
      {!running && state.kind === "doc" ? <DrillResult doc={state.doc} /> : null}
      {state.kind === "failed" ? <FailureNote failure={state.failure} /> : null}
      <div className="card-row">
        {running ? (
          <button type="button" className="btn btn-danger" disabled={stopping || stop.busy} onClick={() => void doStop()}>
            {t("health.drill.stop")}
          </button>
        ) : (
          <button
            type="button"
            className="btn btn-primary"
            disabled={survival || start.busy}
            aria-describedby={survival ? "health-drill-why" : undefined}
            onClick={() => setAsking(true)}
          >
            {t("health.drill.run")}
          </button>
        )}
      </div>
      {survival && !running ? (
        <p id="health-drill-why" className="muted">
          {t("health.drill.survival")}
        </p>
      ) : null}
      {start.phase.kind === "failed" ? <FailureNote failure={start.phase.failure} /> : null}
      {stop.phase.kind === "failed" ? <FailureNote failure={stop.phase.failure} /> : null}
      {asking ? (
        <ConfirmDialog
          title={t("health.drill.confirmTitle")}
          lines={[t("health.drill.confirm1"), t("health.drill.confirm2"), t("health.drill.confirm3")]}
          confirmLabel={t("health.drill.start")}
          onConfirm={() => void confirmStart()}
          onCancel={() => setAsking(false)}
        />
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------------------------------

/** The Health screen (dashboard.md section 4.5, issue #499). */
export function HealthScreen({ status }: { status: PortalStatus }) {
  useLang();
  return (
    <ManageFrame title={t("health.title")} lead={t("health.lead")}>
      <CheckCard />
      <EventsCard />
      <DrillCard survival={status.mode === "survival"} />
    </ManageFrame>
  );
}
