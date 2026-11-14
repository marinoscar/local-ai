import { useCallback, useEffect, useMemo, useState } from "react";
import { apiSend, failureOf, type Failure } from "../api";
import { announce } from "../components/LiveRegion";
import { t, useLang, type Key } from "../i18n/i18n";
import { recordError, refreshStatus, type PortalStatus } from "../status";
import { ConfirmDialog } from "./ConfirmDialog";
import { FailureNote } from "./FailureNote";
import { ManageFrame } from "./ManageFrame";
import { usePin } from "./PinDialog";
import {
  EXPERT_COMMAND,
  RESTART_WHY,
  desktopHost,
  doneText,
  failureError,
  isRestartable,
  kitSummary,
  parseResult,
  refreshText,
  serviceName,
  stateKey,
  stateLevel,
  type AgentStatus,
  type CatalogBody,
  type CatalogRefresh,
  type DesktopHost,
  type Restartable,
  type ServiceRow,
  type SystemActionName,
} from "./system";

// The System screen (dashboard.md section 4.6, issue #500): versions, services and Restart, the
// catalog and the agent update, the rebuild kit, the settings backup and the admin PIN. The
// privileged buttons run through the desktop bridge (desktop.md section 3.5, runSystemAction); in a
// plain browser they show the command for an expert.

type Phase =
  | { kind: "idle" }
  | { kind: "working" }
  | { kind: "done"; text: string }
  | { kind: "failed"; failure: Failure };

/** useWork is useAction with the done sentence computed from the answer. */
function useWork() {
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  const run = useCallback(async (work: () => Promise<string>): Promise<boolean> => {
    setPhase({ kind: "working" });
    announce(t("manage.working"));
    try {
      const text = await work();
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
  }, []);
  return { phase, run, busy: phase.kind === "working" };
}

function PhaseNote({ phase }: { phase: Phase }) {
  switch (phase.kind) {
    case "idle":
      return null;
    case "working":
      return <p className="fix-result">{t("manage.working")}</p>;
    case "done":
      return <p className="fix-result fix-done">{phase.text}</p>;
    case "failed":
      return <FailureNote failure={phase.failure} />;
  }
}

interface Loaded {
  agent: AgentStatus | null;
  catalog: CatalogBody | null;
  failure: Failure | null;
}

/** useSystemData reads GET /status and GET /catalog; a failed reload keeps what was read before. */
function useSystemData() {
  const [data, setData] = useState<Loaded | null>(null);
  const load = useCallback(async () => {
    const [agent, catalog] = await Promise.allSettled([
      apiSend<AgentStatus>("GET", "/status"),
      apiSend<CatalogBody>("GET", "/catalog"),
    ]);
    setData((prev) => ({
      agent: agent.status === "fulfilled" ? agent.value : (prev?.agent ?? null),
      catalog: catalog.status === "fulfilled" ? catalog.value : (prev?.catalog ?? null),
      failure: agent.status === "rejected" ? failureOf(agent.reason) : null,
    }));
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  return { data, load };
}

export function SystemScreen({ status }: { status: PortalStatus }) {
  useLang();
  const host = useMemo(() => desktopHost(), []);
  const { data, load } = useSystemData();
  const online = status.mode === "online";
  return (
    <ManageFrame title={t("system.title")} lead={t("system.lead")}>
      {data === null ? <p>{t("system.loading")}</p> : null}
      {data?.failure ? <FailureNote failure={data.failure} /> : null}
      <Versions agent={data?.agent ?? null} host={host} />
      {data?.agent ? <Services rows={data.agent.services} reload={load} /> : null}
      <section className="card" aria-labelledby="sys-updates">
        <h2 id="sys-updates" className="card-title">
          {t("system.updates")}
        </h2>
        <CatalogRefreshRow online={online} reload={load} />
        <Privileged
          name="update-agent"
          label="system.update"
          why="system.updateWhy"
          title="system.updateTitle"
          host={host}
          disabledReason={online ? null : t("manage.needsInternet")}
          reload={load}
        />
      </section>
      <section className="card" aria-labelledby="sys-kit">
        <h2 id="sys-kit" className="card-title">
          {t("system.kit")}
        </h2>
        <p>{t("system.kitWhy")}</p>
        {data?.catalog ? <KitLine catalog={data.catalog} /> : null}
        <Privileged
          name="kit-verify"
          label="system.kitCheck"
          why="system.kitCheckWhy"
          title="system.kitTitle"
          host={host}
          reload={load}
        />
      </section>
      <section className="card" aria-labelledby="sys-settings">
        <h2 id="sys-settings" className="card-title">
          {t("system.settings")}
        </h2>
        <Privileged
          name="backup-config"
          label="system.backup"
          why="system.backupWhy"
          title="system.backupTitle"
          host={host}
          reload={load}
        />
        <Privileged
          name="restore-config"
          label="system.restore"
          why="system.restoreWhy"
          title="system.restoreTitle"
          host={host}
          pin
          reload={load}
        />
        <div className="sys-item">
          <h3 className="sys-item-title">{t("system.pin")}</h3>
          <p>{t("system.pinWhy")}</p>
          <Expert command={EXPERT_COMMAND["pin-set"]} />
        </div>
      </section>
    </ManageFrame>
  );
}

function Versions({ agent, host }: { agent: AgentStatus | null; host: DesktopHost | null }) {
  const rows: [Key, string][] = [];
  if (host && host.version !== "") rows.push(["system.version.app", host.version]);
  if (agent) {
    rows.push(["system.version.agent", agent.node.agent_version]);
    rows.push(["system.version.catalog", agent.node.catalog_version ?? t("system.version.none")]);
  }
  if (rows.length === 0) return null;
  return (
    <section className="card" aria-labelledby="sys-versions">
      <h2 id="sys-versions" className="card-title">
        {t("system.versions")}
      </h2>
      <dl className="sys-versions">
        {rows.map(([k, v]) => (
          <div key={k} className="sys-version">
            <dt>{t(k)}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

function Services({ rows, reload }: { rows: ServiceRow[]; reload: () => Promise<void> }) {
  if (rows.length === 0) return null;
  return (
    <section className="card" aria-labelledby="sys-services">
      <h2 id="sys-services" className="card-title">
        {t("system.services")}
      </h2>
      <p>{t("system.servicesWhy")}</p>
      <ul className="sys-services">
        {rows.map((r) => (
          <ServiceItem key={r.name} row={r} reload={reload} />
        ))}
      </ul>
    </section>
  );
}

function ServiceItem({ row, reload }: { row: ServiceRow; reload: () => Promise<void> }) {
  const { phase, run, busy } = useWork();
  const [asking, setAsking] = useState(false);
  const name = serviceName(row.name);
  const key = stateKey(row);
  const level = stateLevel(key);
  async function restart(svc: Restartable) {
    setAsking(false);
    const ok = await run(async () => {
      await apiSend("POST", `/services/${svc}/restart`, {});
      return t("system.restarted", { name });
    });
    if (ok) await reload();
  }
  return (
    <li className="sys-service">
      <div className="card-row">
        <span className={`dot dot-${level}`} aria-hidden="true" />
        <span className="sys-service-name">{name}</span>
        <span>{t(key)}</span>
        {isRestartable(row.name) ? (
          <button
            type="button"
            className="btn"
            disabled={busy}
            aria-label={t("system.restartNamed", { name })}
            onClick={() => setAsking(true)}
          >
            {t("system.restart")}
          </button>
        ) : null}
      </div>
      <PhaseNote phase={phase} />
      {asking && isRestartable(row.name) ? (
        <ConfirmDialog
          title={t("system.restartTitle", { name })}
          lines={[t(RESTART_WHY[row.name as Restartable])]}
          confirmLabel={t("system.restart")}
          onConfirm={() => void restart(row.name as Restartable)}
          onCancel={() => setAsking(false)}
        />
      ) : null}
    </li>
  );
}

function CatalogRefreshRow({ online, reload }: { online: boolean; reload: () => Promise<void> }) {
  const { phase, run, busy } = useWork();
  async function refresh() {
    const ok = await run(async () =>
      refreshText(await apiSend<CatalogRefresh>("POST", "/catalog/refresh", {})),
    );
    if (ok) await reload();
  }
  return (
    <div className="sys-item">
      <div className="card-row">
        <button
          type="button"
          className="btn"
          disabled={!online || busy}
          aria-describedby={online ? undefined : "sys-catalog-offline"}
          onClick={() => void refresh()}
        >
          {t("system.catalogRefresh")}
        </button>
        {online ? null : (
          <span id="sys-catalog-offline" className="muted">
            {t("manage.needsInternet")}
          </span>
        )}
      </div>
      <p>{t("system.catalogWhy")}</p>
      <PhaseNote phase={phase} />
    </div>
  );
}

function KitLine({ catalog }: { catalog: CatalogBody }) {
  const k = kitSummary(catalog.content);
  if (k.total === 0) return <p>{t("system.kitNone")}</p>;
  const ready = k.ready === k.total;
  return (
    <p className="card-row">
      <span className={`dot ${ready ? "dot-green" : "dot-amber"}`} aria-hidden="true" />
      <span>
        {ready
          ? t("system.kitReady", { n: k.total })
          : t("system.kitPartial", { ready: k.ready, n: k.total })}
      </span>
    </p>
  );
}

/** The command for an expert, folded (ADR-0009: a command never appears outside this box). */
function Expert({
  command,
  open,
  onToggle,
}: {
  command: string;
  open?: boolean;
  onToggle?: (open: boolean) => void;
}) {
  return (
    <details
      className="expert"
      open={open}
      onToggle={(e) => onToggle?.((e.currentTarget as HTMLDetailsElement).open)}
    >
      <summary>{t("manage.expert")}</summary>
      <code>{command}</code>
    </details>
  );
}

/**
 * A privileged button (dashboard.md section 2): it says what it does and that the computer will ask
 * for the login password. In ai-desktop it confirms, asks the PIN when `pin`, and calls the bridge; in
 * a plain browser (or a desktop without runSystemAction) it opens the command for an expert.
 */
function Privileged({
  name,
  label,
  why,
  title,
  host,
  pin,
  disabledReason,
  reload,
}: {
  name: SystemActionName;
  label: Key;
  why: Key;
  title: Key;
  host: DesktopHost | null;
  pin?: boolean;
  disabledReason?: string | null;
  reload: () => Promise<void>;
}) {
  const { phase, run, busy } = useWork();
  const { ask, dialog } = usePin();
  const [asking, setAsking] = useState(false);
  const [expertOpen, setExpertOpen] = useState(false);
  const [browserNote, setBrowserNote] = useState(false);
  const runner = host?.run ?? null;
  const reasonId = `sys-${name}-reason`;

  function press() {
    if (!runner) {
      setBrowserNote(true);
      setExpertOpen(true);
      return;
    }
    setAsking(true);
  }

  async function go() {
    setAsking(false);
    if (!runner) return;
    if (pin) {
      const grant = await ask(t("system.restorePin"));
      if (grant === null) return;
    }
    const ok = await run(async () => {
      const r = parseResult(await runner(name));
      if (!r.ok) throw failureError(r);
      return doneText(name, r);
    });
    if (ok) await reload();
  }

  return (
    <div className="sys-item">
      <div className="card-row">
        <button
          type="button"
          className={name === "update-agent" ? "btn btn-primary" : "btn"}
          disabled={busy || !!disabledReason}
          aria-describedby={disabledReason ? reasonId : undefined}
          onClick={press}
        >
          {t(label)}
        </button>
        {disabledReason ? (
          <span id={reasonId} className="muted">
            {disabledReason}
          </span>
        ) : null}
      </div>
      <p>{t(why)}</p>
      <p className="muted">{t("system.password")}</p>
      {browserNote ? <p className="fix-result">{t("system.desktopOnly")}</p> : null}
      <PhaseNote phase={phase} />
      <Expert command={EXPERT_COMMAND[name]} open={expertOpen} onToggle={setExpertOpen} />
      {asking ? (
        <ConfirmDialog
          title={t(title)}
          lines={[t(why), t("system.password")]}
          onConfirm={() => void go()}
          onCancel={() => setAsking(false)}
        />
      ) : null}
      {dialog}
    </div>
  );
}
