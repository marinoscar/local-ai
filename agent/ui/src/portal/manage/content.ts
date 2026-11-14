import { getLang, t, type Key, type Lang } from "../i18n/i18n";

// The data and the plain-word rules of the Offline content and drives screen (dashboard.md section
// 4.3, issue #496). The shapes are those the agent answers (ark-sync.md section 3.5, READY.json section
// 4.12.1); only the fields the screen reads are typed. Everything here is pure, so the rules are
// unit-tested without a browser (content.test.ts).

/** One item of a READY.json report (ark-sync.md section 4.12.1). */
export interface ReadyItem {
  id: string;
  kind: string;
  tier?: number;
  survival?: boolean;
  status: "verified" | "stale" | "missing" | "skipped" | "failed" | "quarantined" | "orphan" | string;
  version?: string;
  reason?: string;
  code?: string;
  bytes?: number;
}

export interface ReadyProblem {
  code: string;
  severity?: string;
  item?: string;
  title: string;
}

export type Colour = "green" | "yellow" | "red";

/** One root of GET /ark: the READY.json of a mounted content root. */
export interface ReadyReport {
  generated_at?: string;
  root: { id: string; kind: string; read_only?: boolean; fs_free_bytes?: number; fs_total_bytes?: number };
  overall: Colour;
  survival_capable?: boolean;
  clock_wrong?: boolean;
  last_sync: { run_id?: string; trigger?: string; finished_at?: string; state?: string; age_days?: number } | null;
  counts?: Partial<Record<string, number>>;
  items: ReadyItem[];
  problems: ReadyProblem[];
}

export interface DriveProblem {
  code: string;
  severity?: string;
  title: string;
  fix?: string;
}

export type DriveState =
  | "ready"
  | "read_only"
  | "unclean"
  | "not_mounted"
  | "unusable"
  | "ejecting"
  | "uninitialized";

/** One drive of GET /ark/drives (ark-sync.md section 3.5). */
export interface Drive {
  root_id: string | null;
  device?: string;
  mount: string | null;
  fstype?: string;
  label?: string;
  size_bytes?: number;
  free_bytes?: number;
  state: DriveState | string;
  read_only?: boolean;
  initialized?: boolean;
  profile?: string;
  ready?: { overall: Colour; generated_at?: string } | null;
  problems?: DriveProblem[];
}

export interface RunTotals {
  items: number;
  done: number;
  skipped: number;
  failed: number;
  bytes_total: number;
  bytes_done: number;
  rate_bps?: number;
  eta_s?: number;
}

export interface RunActive {
  root: string;
  item: string;
  state: string;
  version?: string;
  bytes_done: number;
  bytes_total: number;
}

export interface RunItem {
  root: string;
  item: string;
  result: string;
  version?: string;
  reason?: string;
  code?: string;
}

/** GET /ark/sync: the active or the last run (sync, verify or import share the run slot). */
export interface SyncRun {
  run_id: string;
  trigger: string;
  state: "running" | "cancelling" | "done" | "cancelled" | "failed" | "skipped" | string;
  reason?: string;
  code?: string;
  started_at?: string;
  finished_at?: string | null;
  roots?: string[];
  totals?: RunTotals;
  active?: RunActive[];
  result?: string;
  items?: RunItem[];
}

/** The answer of POST /ark/prune (agent.md section 3.8.20a). */
export interface PruneResult {
  deleted: { root: string; item: string; version?: string; bytes: number; kind: string }[];
  refused: { root: string; item: string; version?: string; reason: string; kind?: string }[];
  freed_bytes: number;
}

/** One blocker of a refused eject (409 LAI-ARK-009, details.blockers). */
export interface EjectBlocker {
  kind: "process" | "model" | "sync" | string;
  name: string;
  pid?: number;
}

// ---------------------------------------------------------------------------------------------------
// Groups

export type Group = "maps" | "library" | "models" | "speech" | "kit" | "docs" | "other";

/** The order of the groups on the screen. */
export const GROUPS: Group[] = ["maps", "library", "models", "speech", "kit", "docs", "other"];

/**
 * groupOf puts an item under its heading. READY kinds are model, kiwix, maps, kit and docs; speech
 * models (whisper, piper) are kind model, so their catalog id prefix tells them apart.
 */
export function groupOf(item: Pick<ReadyItem, "id" | "kind">): Group {
  switch (item.kind) {
    case "maps":
      return "maps";
    case "kiwix":
      return "library";
    case "model":
      return /^(whisper|piper)-/.test(item.id) ? "speech" : "models";
    case "kit":
      return "kit";
    case "docs":
      return "docs";
  }
  return "other";
}

/** groupItems keeps the screen's group order and drops empty groups. */
export function groupItems(items: ReadyItem[]): { group: Group; items: ReadyItem[] }[] {
  const out: { group: Group; items: ReadyItem[] }[] = [];
  for (const g of GROUPS) {
    const list = items.filter((i) => groupOf(i) === g);
    if (list.length > 0) out.push({ group: g, items: list });
  }
  return out;
}

// ---------------------------------------------------------------------------------------------------
// Item status in plain words

export type Tone = "ok" | "warn" | "bad";

export interface ItemWords {
  key: Key;
  tone: Tone;
}

/** blockedSet is the root/item pairs the last ended run skipped as blocked (quarantined twice). */
export function blockedSet(run: SyncRun | null): Set<string> {
  const s = new Set<string>();
  for (const i of run?.items ?? []) if (i.reason === "blocked") s.add(`${i.root}/${i.item}`);
  return s;
}

/** itemWords is the plain-word status of an item (dashboard.md section 4.3). */
export function itemWords(item: ReadyItem, blocked = false): ItemWords {
  if (blocked || item.reason === "blocked") return { key: "content.item.blocked", tone: "bad" };
  switch (item.status) {
    case "verified":
      return { key: "content.item.verified", tone: "ok" };
    case "stale":
      return { key: "content.item.stale", tone: "warn" };
    case "missing":
      return { key: "content.item.missing", tone: "bad" };
    case "quarantined":
      return { key: "content.item.quarantined", tone: "bad" };
    case "failed":
      return { key: "content.item.failed", tone: "bad" };
    case "orphan":
      return { key: "content.item.orphan", tone: "warn" };
    case "skipped":
      if (item.reason === "budget") return { key: "content.item.skippedBudget", tone: "warn" };
      if (item.reason === "disk") return { key: "content.item.skippedDisk", tone: "warn" };
      if (item.reason === "time_limit") return { key: "content.item.skippedTime", tone: "warn" };
      return { key: "content.item.skipped", tone: "warn" };
  }
  return { key: "content.item.unknown", tone: "warn" };
}

// ---------------------------------------------------------------------------------------------------
// Root readiness, one line each

/** rootName: "Internal disk" or "AI-ARK drive (ark-3f9c0a12)". */
export function rootName(root: { id: string; kind: string }): string {
  return root.kind === "internal" ? t("content.root.internal") : t("content.root.drive", { id: root.id });
}

/**
 * rootLine is the readiness of one root in one line: "Internal disk: ready", "… ready, with a
 * warning" or "98 % ready, 1 item missing". An item counts as ready when a working copy is present
 * (verified or stale); orphans are not in the plan and do not count. The percentage is never 100
 * while an item is missing.
 */
export function rootLine(r: ReadyReport): string {
  const name = rootName(r.root);
  const wanted = r.items.filter((i) => i.status !== "orphan");
  const here = wanted.filter((i) => i.status === "verified" || i.status === "stale").length;
  const missing = wanted.length - here;
  if (missing === 0)
    return r.overall === "green"
      ? t("content.root.ready", { name })
      : t("content.root.readyWarn", { name });
  const pct = Math.min(99, Math.floor((here * 100) / wanted.length));
  return missing === 1
    ? t("content.root.partOne", { name, pct })
    : t("content.root.partMany", { name, pct, n: missing });
}

/** dateText: a date and time in the UI language, "" when it cannot be read. */
export function dateText(iso: string | null | undefined, lang: Lang = getLang()): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return new Intl.DateTimeFormat(lang, { dateStyle: "medium", timeStyle: "short" }).format(d);
}

/** lastSyncLine: "Last sync: 2 Nov 2026, 04:02" or "No sync yet". */
export function lastSyncLine(r: ReadyReport, lang: Lang = getLang()): string {
  const when = dateText(r.last_sync?.finished_at, lang);
  return when === "" ? t("content.lastSync.never") : t("content.lastSync", { when });
}

/** sizeText: decimal units, "5.9 GB" / "5,9 GB". */
export function sizeText(bytes: number | undefined | null, lang: Lang = getLang()): string {
  if (bytes === undefined || bytes === null || !Number.isFinite(bytes) || bytes < 0) return "";
  const units: [number, string][] = [
    [1e12, "TB"],
    [1e9, "GB"],
    [1e6, "MB"],
    [1e3, "KB"],
  ];
  for (const [n, u] of units)
    if (bytes >= n) {
      const v = bytes / n;
      const f = new Intl.NumberFormat(lang, { maximumFractionDigits: v < 10 ? 1 : 0 });
      return `${f.format(v)} ${u}`;
    }
  return `${bytes} B`;
}

// ---------------------------------------------------------------------------------------------------
// The run (sync, verify or import)

export function runActive(run: SyncRun | null): boolean {
  return run !== null && (run.state === "running" || run.state === "cancelling");
}

/** runTitle: what the running work is, in words. */
export function runTitle(run: SyncRun): string {
  if (run.state === "cancelling") return t("content.run.stopping");
  if (run.trigger === "verify") return t("content.run.verifying");
  if (run.trigger === "import") return t("content.run.importing");
  return t("content.run.syncing");
}

/** runProgress: items and bytes of a running run; null when the run has no totals yet. */
export function runProgress(run: SyncRun): { done: number; total: number; pct: number } | null {
  const tt = run.totals;
  if (!tt || tt.items <= 0) return null;
  const done = Math.min(tt.items, tt.done + tt.skipped + tt.failed);
  const pct =
    tt.bytes_total > 0
      ? Math.min(100, Math.floor((tt.bytes_done * 100) / tt.bytes_total))
      : Math.floor((done * 100) / tt.items);
  return { done, total: tt.items, pct };
}

/** activeState: the state of one item in a running run, in words. */
export function activeWords(state: string): string {
  switch (state) {
    case "resolving":
      return t("content.active.resolving");
    case "downloading":
      return t("content.active.downloading");
    case "verifying":
      return t("content.active.verifying");
    case "staged":
    case "swapping":
    case "swapped":
      return t("content.active.installing");
  }
  return t("content.active.waiting");
}

/** runEnded: the sentence of a run that has ended, and whether it is a problem. */
export function runEnded(run: SyncRun): { text: string; tone: Tone } {
  const failed = run.totals?.failed ?? 0;
  if (run.state === "cancelled") return { text: t("content.run.cancelled"), tone: "warn" };
  if (run.state === "skipped") return { text: t("content.run.skipped"), tone: "warn" };
  if (run.state === "failed" || run.result === "failed") return { text: t("content.run.failed"), tone: "bad" };
  if (run.result === "partial") {
    if (failed === 1) return { text: t("content.run.partialOne"), tone: "warn" };
    return failed > 1
      ? { text: t("content.run.partial", { n: failed }), tone: "warn" }
      : { text: t("content.run.partialNoCount"), tone: "warn" };
  }
  return { text: t("content.run.ok"), tone: "ok" };
}

// ---------------------------------------------------------------------------------------------------
// Buttons: why a button is disabled (dashboard.md section 5: "Disabled with the reason")

/** syncBlocked: why Sync now cannot be pressed, or null. */
export function syncBlocked(mode: string, running: boolean): Key | null {
  if (mode !== "online") return "manage.needsInternet";
  if (running) return "content.why.busy";
  return null;
}

/** driveName: "AI-ARK, 2 TB" (label and size). */
export function driveName(d: Drive, lang: Lang = getLang()): string {
  const label = d.label && d.label !== "" ? d.label : t("content.drive.noLabel");
  const size = sizeText(d.size_bytes, lang);
  return size === "" ? label : t("content.drive.name", { label, size });
}

/** driveWords: the readiness of one drive in plain words. */
export function driveWords(d: Drive): ItemWords {
  switch (d.state) {
    case "ready":
      if (!d.ready) return { key: "content.drive.readyUnchecked", tone: "ok" };
      if (d.ready.overall === "green") return { key: "content.drive.ready", tone: "ok" };
      if (d.ready.overall === "yellow") return { key: "content.drive.readyWarn", tone: "warn" };
      return { key: "content.drive.incomplete", tone: "bad" };
    case "read_only":
      return { key: "content.drive.readOnly", tone: "warn" };
    case "unclean":
      return { key: "content.drive.unclean", tone: "bad" };
    case "not_mounted":
      return { key: "content.drive.notMounted", tone: "warn" };
    case "ejecting":
      return { key: "content.drive.ejecting", tone: "warn" };
    case "uninitialized":
      return { key: "content.drive.uninitialized", tone: "warn" };
  }
  return { key: "content.drive.unusable", tone: "bad" };
}

/** copyBlocked: why Copy from this drive cannot be pressed, or null. */
export function copyBlocked(d: Drive, running: boolean): Key | null {
  if (d.root_id === null) return "content.why.unusable";
  switch (d.state) {
    case "ready":
    case "read_only":
      return running ? "content.why.busy" : null;
    case "unclean":
      return "content.why.unclean";
    case "uninitialized":
      return "content.why.empty";
    case "not_mounted":
      return "content.why.notMounted";
    case "ejecting":
      return "content.why.ejecting";
  }
  return "content.why.unusable";
}

/** ejectBlocked: why Eject safely cannot be pressed, or null (a drive must be mounted). */
export function ejectBlocked(d: Drive): Key | null {
  if (d.root_id === null) return "content.why.unusable";
  if (d.state === "ejecting") return "content.why.ejecting";
  if (d.mount === null || d.state === "not_mounted") return "content.why.notMountedEject";
  return null;
}

/** EjectOutcome: what the drive list says after an accepted eject. */
export type EjectOutcome = "waiting" | "safe" | "failed";

/**
 * ejectOutcome reads the drive list after a 202: the drive left the list or shows not_mounted →
 * safe to unplug; still ejecting → waiting; back to a mounted state → the unmount failed (the agent
 * puts it back and raises LAI-ARK-009).
 */
export function ejectOutcome(drives: Drive[], rootId: string): EjectOutcome {
  const d = drives.find((x) => x.root_id === rootId);
  if (!d || d.state === "not_mounted" || d.mount === null) return "safe";
  if (d.state === "ejecting") return "waiting";
  return "failed";
}

/** blockersOf: the blockers of a refused eject (409 LAI-ARK-009 details). */
export function blockersOf(details: Record<string, unknown>): EjectBlocker[] {
  const b = details["blockers"];
  if (!Array.isArray(b)) return [];
  return b.filter(
    (x): x is EjectBlocker =>
      typeof x === "object" && x !== null && typeof (x as EjectBlocker).name === "string",
  );
}
