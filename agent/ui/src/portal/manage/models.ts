import type { Key } from "../i18n/i18n";

// The AI models screen's rules (dashboard.md section 4.4), kept apart from React so Vitest can check
// them: the row of GET /api/v1/models (agent.md section 3.8.7), what each row says in plain words,
// which buttons it shows and why a button is disabled.

/** The doctor codes of loadable_reason (agent.md section 3.8.7). */
export const CODE_DOES_NOT_FIT = "LAI-CAT-005";
export const CODE_FILES_MISSING = "LAI-LLM-008";
export const CODE_ROUTER_DOWN = "LAI-LLM-004";
/** A load that ended failed, or did not end in LOAD_TIMEOUT_MS (agent.md section 3.8.8). */
export const CODE_LOAD_FAILED = "LAI-LLM-009";
export const CODE_LOAD_TIMEOUT = "LAI-LLM-010";
/** DELETE with keep_last=true refused: the copy is the last verified one (agent.md section 3.8.12). */
export const CODE_LAST_COPY = "LAI-SYN-044";

/** The same 300 s as the Portal's brain switch (survival-portal.md section 4.1.9). */
export const LOAD_TIMEOUT_MS = 300_000;

/** The content root of this computer's own disk (ark-sync.md section 3.5 root ids). */
export const INTERNAL_ROOT = "internal";

/** One row of GET /models: only the fields this screen reads. */
export interface ModelRow {
  id: string;
  display_name: string;
  summary?: string;
  engine: string;
  survival_tier?: number;
  in_profile: boolean;
  size_bytes: number;
  present: boolean;
  verified: boolean;
  locations: { root: string; complete: boolean }[];
  loadable: boolean;
  loadable_reason: { code: string; message: string } | null;
  status: string;
  loaded: boolean;
  default: boolean;
}

export interface ModelList {
  models_max: number;
  default: string | null;
  models: ModelRow[];
}

/** What a row says, before any action of this screen is added. */
export type RowState =
  | "in_use"
  | "loading"
  | "on_computer"
  | "failed"
  | "absent";

export function rowState(m: ModelRow): RowState {
  switch (m.status) {
    case "loaded":
    case "sleeping":
      return "in_use";
    case "loading":
      return "loading";
    case "failed":
      return "failed";
    case "absent":
      return "absent";
    case "stored":
    case "unloaded":
      return "on_computer";
    default:
      // n/a (speech models) and anything new: what is on disk.
      return m.present ? "on_computer" : "absent";
  }
}

export const STATE_KEY: Record<RowState, Key> = {
  in_use: "models.state.inUse",
  loading: "models.state.loading",
  on_computer: "models.state.onComputer",
  failed: "models.state.failed",
  absent: "models.state.absent",
};

/** isBrain: a llama model, the only kind that can be used, unloaded or made the default. */
export function isBrain(m: ModelRow): boolean {
  return m.engine === "llama";
}

/** fits is false only when the agent said the model does not fit this computer. */
export function fits(m: ModelRow): boolean {
  return m.loadable_reason?.code !== CODE_DOES_NOT_FIT;
}

/** The facts a button needs besides its row. */
export interface ButtonContext {
  /** The node's mode (status.mode): a download needs online. */
  mode: string;
  /** An action of this screen is running (one at a time: the agent serialises model work). */
  busy: boolean;
  /** This row is downloading now. */
  downloading: boolean;
  /** A download of this screen is running (the sync run slot is taken). */
  downloadRunning: boolean;
}

/** One button of a row: hidden, enabled, or disabled with the sentence that says why. */
export type ButtonRule =
  | { show: false }
  | { show: true; why: Key | null };

export type ButtonName = "use" | "unload" | "makeDefault" | "download" | "delete";

const HIDDEN: ButtonRule = { show: false };
const on = (why: Key | null): ButtonRule => ({ show: true, why });

/** Why a brain on disk cannot be loaded or made the default, or null when it can. */
function notLoadableWhy(m: ModelRow): Key | null {
  if (m.loadable) return null;
  switch (m.loadable_reason?.code) {
    case CODE_DOES_NOT_FIT:
      return "models.why.tooBig";
    case CODE_ROUTER_DOWN:
      return "models.why.serviceDown";
    case CODE_FILES_MISSING:
      return "models.why.notChecked";
    default:
      return "models.why.cannotStart";
  }
}

/** buttons is the rule of every button of a row (dashboard.md section 3.2, section 5). */
export function buttons(
  m: ModelRow,
  ctx: ButtonContext,
): Record<ButtonName, ButtonRule> {
  const state = rowState(m);
  const busyWhy: Key | null = ctx.busy ? "models.why.busy" : null;
  const inMemory = state === "in_use" || state === "loading";
  const out: Record<ButtonName, ButtonRule> = {
    use: HIDDEN,
    unload: HIDDEN,
    makeDefault: HIDDEN,
    download: HIDDEN,
    delete: HIDDEN,
  };
  if (isBrain(m)) {
    if (inMemory) out.unload = on(busyWhy);
    else if (m.present) out.use = on(notLoadableWhy(m) ?? busyWhy);
    if (m.default) out.makeDefault = on("models.why.isDefault");
    else if (!m.present) out.makeDefault = on("models.why.downloadFirst");
    else out.makeDefault = on(notLoadableWhy(m) ?? busyWhy);
  }
  if (!m.present) {
    if (ctx.downloading) out.download = HIDDEN;
    else if (ctx.mode !== "online") out.download = on("manage.needsInternet");
    else if (isBrain(m) && !fits(m)) out.download = on("models.why.tooBig");
    else if (ctx.downloadRunning) out.download = on("models.why.downloadRunning");
    else out.download = on(busyWhy);
  } else {
    out.delete = on(inMemory ? "models.why.unloadFirst" : busyWhy);
  }
  return out;
}

/** The order of the list: in use, then on this computer, then the rest; the catalog order inside. */
const ORDER: Record<RowState, number> = {
  in_use: 0,
  loading: 0,
  failed: 1,
  on_computer: 1,
  absent: 2,
};

/**
 * groups splits the list: brains to show (on this computer or in this computer's plan), more brains
 * (the rest of the catalog, behind a "More models" fold) and the speech models.
 */
export function groups(list: ModelRow[]): {
  brains: ModelRow[];
  more: ModelRow[];
  speech: ModelRow[];
} {
  const brains: ModelRow[] = [];
  const more: ModelRow[] = [];
  const speech: ModelRow[] = [];
  for (const m of list) {
    if (!isBrain(m)) speech.push(m);
    else if (m.present || m.in_profile || m.loaded) brains.push(m);
    else more.push(m);
  }
  const byState = (a: ModelRow, b: ModelRow) =>
    ORDER[rowState(a)] - ORDER[rowState(b)];
  return {
    brains: [...brains].sort(byState),
    more,
    speech: [...speech].sort(byState),
  };
}

/** formatSize: "5.9 GB" / "5,9 GB" (decimal units, like aictl), "750 MB" under 1 GB. */
export function formatSize(bytes: number, lang: string): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "";
  const nf = new Intl.NumberFormat(lang, { maximumFractionDigits: 1, minimumFractionDigits: 1 });
  if (bytes >= 1e9) return `${nf.format(bytes / 1e9)} GB`;
  const whole = new Intl.NumberFormat(lang, { maximumFractionDigits: 0 });
  return `${whole.format(Math.max(1, Math.round(bytes / 1e6)))} MB`;
}

/** The path of a delete from this computer's own disk (agent.md section 3.8.12). */
export function deletePath(id: string, keepLast: boolean): string {
  return `/models/${encodeURIComponent(id)}?root=${INTERNAL_ROOT}&keep_last=${keepLast}`;
}

/** The part of GET /ark/sync (ark-sync.md section 3.5) a download follows. */
export interface SyncRun {
  run_id: string;
  state: string;
  result?: string;
  code?: string;
  active?: { item: string; bytes_done: number; bytes_total: number }[];
  items?: { item: string; result: string; code?: string; reason?: string }[];
}

/** What a model download looks like in its sync run. */
export type DownloadView =
  | { kind: "running"; percent: number | null }
  | { kind: "ok" }
  | { kind: "failed"; code: string };

/**
 * downloadView reads the run a download started: the percent of the model's transfer while it runs
 * (null before its bytes are known), then how it ended. A run that is not this download's (another
 * run took the slot, or the run is not known yet) counts as still running.
 */
export function downloadView(run: SyncRun | null, runId: string, id: string): DownloadView {
  if (!run || run.run_id !== runId) return { kind: "running", percent: null };
  if (run.state === "running" || run.state === "cancelling") {
    const a = run.active?.find((x) => x.item === id);
    if (!a || !(a.bytes_total > 0)) return { kind: "running", percent: null };
    return {
      kind: "running",
      percent: Math.min(100, Math.floor((a.bytes_done / a.bytes_total) * 100)),
    };
  }
  // An item that failed, or was skipped for a reason with a code (budget, disk), did not arrive.
  const mine = run.items?.find(
    (x) => x.item === id && (x.result === "failed" || (x.result === "skipped" && !!x.code)),
  );
  if (run.state === "done" && !mine) return { kind: "ok" };
  return { kind: "failed", code: mine?.code ?? run.code ?? "" };
}
