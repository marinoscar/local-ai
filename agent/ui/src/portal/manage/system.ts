import { ApiError } from "../api";
import { t, type Key } from "../i18n/i18n";

// The data and the words of the System screen (dashboard.md section 4.6, issue #500). Pure helpers, so
// the rules are unit-tested without a browser: what GET /status and GET /catalog say (agent.md
// sections 3.8.4 and 3.8.21; only the fields this screen reads), and what a privileged button of the
// desktop bridge came to (desktop.md section 3.5, runSystemAction).

/** One row of GET /status `services` (agent.md 3.8.4, 3.8.22). piper and maps have `unit: null`. */
export interface ServiceRow {
  name: string;
  unit: string | null;
  active?: string;
  healthy?: boolean;
  /** companion-services.md 4.6: ok, starting, degraded, down, missing_content, disabled. */
  state?: string;
}

/** The part of GET /status this screen reads. */
export interface AgentStatus {
  node: { agent_version: string; catalog_version: string | null };
  services: ServiceRow[];
}

/** One row of GET /catalog `content` (agent.md 3.8.21). */
export interface ContentRow {
  id: string;
  kind: string;
  verified: boolean;
  in_profile: boolean;
}

export interface CatalogBody {
  version: string;
  content: ContentRow[];
}

/** POST /catalog/refresh (agent.md 3.8.21). */
export interface CatalogRefresh {
  version: string;
  previous: string | null;
  changed: boolean;
}

/** The services a person can restart here: the three with a unit (POST /services/{name}/restart). */
export const RESTARTABLE = ["llama", "kiwix", "whisper"] as const;
export type Restartable = (typeof RESTARTABLE)[number];

export function isRestartable(name: string): name is Restartable {
  return (RESTARTABLE as readonly string[]).includes(name);
}

const SERVICE_NAME: Record<string, Key> = {
  llama: "system.svc.llama",
  kiwix: "system.svc.kiwix",
  whisper: "system.svc.whisper",
  piper: "system.svc.piper",
  maps: "system.svc.maps",
};

/** serviceName is the plain name of a service ("Library"); an unknown one keeps its own name. */
export function serviceName(name: string): string {
  const k = SERVICE_NAME[name];
  return k ? t(k) : name;
}

const STATE: Record<string, Key> = {
  ok: "system.state.ok",
  starting: "system.state.starting",
  degraded: "system.state.degraded",
  down: "system.state.down",
  missing_content: "system.state.missing_content",
  disabled: "system.state.disabled",
};

/** stateKey is the plain-words state of a row: its health `state`, else what systemd says. */
export function stateKey(row: ServiceRow): Key {
  if (row.state && STATE[row.state]) return STATE[row.state];
  if (row.active === "active") return row.healthy === false ? "system.state.degraded" : "system.state.ok";
  if (row.active === "activating") return "system.state.starting";
  if (row.active === "inactive" || row.active === "failed") return "system.state.down";
  return "system.state.unknown";
}

/** The traffic-light of a state, for the dot next to it. */
export function stateLevel(key: Key): "green" | "amber" | "red" {
  if (key === "system.state.ok") return "green";
  if (key === "system.state.down") return "red";
  return "amber";
}

/** What a restart stops for a moment, said in the confirmation. */
export const RESTART_WHY: Record<Restartable, Key> = {
  llama: "system.restartWhy.llama",
  kiwix: "system.restartWhy.kiwix",
  whisper: "system.restartWhy.whisper",
};

/** The rebuild kit's readiness from the catalog: the kit items of the plan and how many are verified. */
export interface KitSummary {
  total: number;
  ready: number;
}

export function kitSummary(content: ContentRow[]): KitSummary {
  const kit = content.filter((c) => c.kind === "kit" && c.in_profile);
  return { total: kit.length, ready: kit.filter((c) => c.verified).length };
}

// ---------------------------------------------------------------------------------------------------
// The desktop bridge (desktop.md section 3.5). It is another program's API: feature-detect, never trust
// the shape of an answer.
// ---------------------------------------------------------------------------------------------------

/** The System screen's privileged buttons, as the desktop names them. */
export type SystemActionName =
  | "update-agent"
  | "kit-verify"
  | "backup-config"
  | "restore-config";

/** The command an expert types for each button (aictl.md 4.27, 4.23c, 4.25, 4.19). */
export const EXPERT_COMMAND: Record<SystemActionName | "pin-set", string> = {
  "update-agent": "sudo aictl update",
  "kit-verify": "aictl kit verify",
  "backup-config": "sudo aictl backup-config --to-ark",
  "restore-config": "sudo aictl backup-config --restore latest",
  "pin-set": "sudo aictl pin set",
};

/** The answer of runSystemAction, checked field by field. */
export type SystemActionResult =
  | {
      ok: true;
      status?: string;
      from?: string;
      to?: string;
      overall?: "green" | "yellow" | "red";
      code?: string;
      files?: number;
      restored?: number;
      restarted?: boolean;
    }
  | { ok: false; reason: string; code?: string; message?: string; fix?: string };

interface HostLike {
  isDesktop?: unknown;
  version?: unknown;
  runSystemAction?: unknown;
}

export interface DesktopHost {
  /** The app's version, or "" when it did not say. */
  version: string;
  /** Null for a desktop older than issue #500 (no runSystemAction). */
  run: ((name: SystemActionName) => Promise<unknown>) | null;
}

/** desktopHost is the ai-desktop bridge of this page, or null in a plain web browser. */
export function desktopHost(w: unknown = typeof window === "undefined" ? undefined : window): DesktopHost | null {
  const host = (w as { localaiDesktopHost?: HostLike } | undefined)?.localaiDesktopHost;
  if (!host || host.isDesktop !== true) return null;
  const fn = host.runSystemAction;
  return {
    version: typeof host.version === "string" ? host.version : "",
    run: typeof fn === "function" ? (name) => (fn as (n: string) => Promise<unknown>).call(host, name) : null,
  };
}

const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);
const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);

/** parseResult reads an answer of runSystemAction; anything that is not one is a plain failure. */
export function parseResult(v: unknown): SystemActionResult {
  if (typeof v !== "object" || v === null) return { ok: false, reason: "failed" };
  const o = v as Record<string, unknown>;
  if (o.ok === true) {
    const overall = o.overall === "green" || o.overall === "yellow" || o.overall === "red" ? o.overall : undefined;
    return {
      ok: true,
      status: str(o.status),
      from: str(o.from),
      to: str(o.to),
      overall,
      code: str(o.code),
      files: num(o.files),
      restored: num(o.restored),
      restarted: typeof o.restarted === "boolean" ? o.restarted : undefined,
    };
  }
  return {
    ok: false,
    reason: str(o.reason) ?? "failed",
    code: str(o.code),
    message: str(o.message),
    fix: str(o.fix),
  };
}

const REASON: Record<string, Key> = {
  cancelled: "system.cancelled",
  busy: "system.busy",
  unavailable: "system.unavailable",
  not_implemented: "system.unavailable",
  timeout: "system.timeout",
};

/**
 * failureError turns a failed answer into the error the screen shows (code, message, fix): the
 * command's own envelope when it printed one, else the sentence of the reason.
 */
export function failureError(r: Extract<SystemActionResult, { ok: false }>): ApiError {
  const key = REASON[r.reason];
  const message = r.message ?? t(key ?? "system.failedPlain");
  return new ApiError(0, r.code ?? "", message, r.fix ?? "");
}

/** doneText is the sentence of a success, from the fields of the answer. */
export function doneText(name: SystemActionName, r: Extract<SystemActionResult, { ok: true }>): string {
  switch (name) {
    case "update-agent":
      if (r.status === "already_current")
        return t("system.upToDate", { version: r.to ?? r.from ?? "" });
      return r.to ? t("system.updated", { to: r.to }) : t("manage.done");
    case "kit-verify": {
      const line =
        r.overall === "red"
          ? t("system.kitRed")
          : r.overall === "yellow"
            ? t("system.kitYellow")
            : t("system.kitGreen");
      return r.code && r.overall !== "green" ? `${line} ${t("common.code", { code: r.code })}` : line;
    }
    case "backup-config":
      return r.files === undefined ? t("system.backedUpPlain") : t("system.backedUp", { n: r.files });
    case "restore-config":
      if (r.restored === 0) return t("system.restoredNothing");
      return r.restored === undefined ? t("system.restoredPlain") : t("system.restored", { n: r.restored });
  }
}

/** The catalog refresh's sentence. */
export function refreshText(r: CatalogRefresh | null): string {
  if (!r) return t("manage.done");
  return r.changed
    ? t("system.catalogChanged", { version: r.version })
    : t("system.catalogSame", { version: r.version });
}
