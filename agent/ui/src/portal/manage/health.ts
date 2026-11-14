// The pure parts of the Health screen (dashboard.md section 4.5, issue #499): the doctor report in
// plain words, a journal line as a sentence, and a drill results document as a summary. The screen
// (HealthScreen.tsx) only calls the API and renders what these return.

/** A doctor code (doctor-codes.md section 1.1). */
export const CODE_RE = /LAI-[A-Z]{2,3}-\d{3}/;

// ---------------------------------------------------------------------------------------------------
// GET /doctor (agent.md section 3.8.27)

export type Severity = "info" | "warning" | "error" | "critical";

export interface DoctorResult {
  check: string;
  code: string;
  severity: Severity;
  title: string;
  fix: string;
  /** The button of the fix (doctor-codes.md section 1.7), when the code has one. */
  action?: string;
  details?: Record<string, unknown>;
}

export interface DoctorReport {
  ran_at: string;
  duration_ms: number;
  summary: { ok: number; info: number; warning: number; error: number; critical: number };
  results: DoctorResult[];
  passed: string[];
}

export interface DoctorView {
  /** How many checks passed. */
  passed: number;
  /** warning, error and critical results, worst first (the agent's order). */
  problems: DoctorResult[];
  /** info results: worth knowing, nothing to fix. */
  notes: DoctorResult[];
  /** Nothing is wrong (notes may still exist). */
  allOk: boolean;
}

/** doctorView splits a report into what the screen shows. */
export function doctorView(rep: DoctorReport): DoctorView {
  const results = Array.isArray(rep.results) ? rep.results : [];
  const problems = results.filter((r) => r.severity !== "info");
  const notes = results.filter((r) => r.severity === "info");
  const passed = Array.isArray(rep.passed) ? rep.passed.length : (rep.summary?.ok ?? 0);
  return { passed, problems, notes, allOk: problems.length === 0 };
}

// ---------------------------------------------------------------------------------------------------
// GET /logs (agent.md section 3.8.23)

/** The short unit names the route takes, in the order the screen offers them. */
export const LOG_UNITS = ["agent", "llama", "kiwix", "whisper", "sync", "drill"] as const;
export type LogUnit = (typeof LOG_UNITS)[number];

/** How many entries the screen asks for (dashboard.md section 4.5: the last 50). */
export const LOG_LINES = 50;

export interface LogLine {
  at: string;
  priority: number;
  message: string;
}

export interface LogsResponse {
  unit: string;
  lines: LogLine[];
  truncated: boolean;
}

export type Level = "error" | "warning" | "info" | "debug";

export interface EventSentence {
  /** The time of the entry; null when it does not parse. */
  at: Date | null;
  level: Level;
  /** What happened, in the words of the program, never a JSON object. */
  text: string;
  /** The doctor code of the entry, "" when it has none. */
  code: string;
}

/** levelOfPriority maps a syslog priority (0 emerg .. 7 debug) to the screen's four levels. */
export function levelOfPriority(p: number): Level {
  if (p <= 3) return "error";
  if (p === 4) return "warning";
  if (p === 7) return "debug";
  return "info";
}

function levelOfName(v: unknown): Level | null {
  if (typeof v !== "string") return null;
  const s = v.toUpperCase();
  if (s.startsWith("ERR") || s === "CRITICAL" || s === "FATAL") return "error";
  if (s.startsWith("WARN")) return "warning";
  if (s === "DEBUG") return "debug";
  if (s === "INFO") return "info";
  return null;
}

/**
 * eventSentence turns one journal entry into a sentence. The agent writes one slog JSON object per
 * line (`{"level":"WARN","msg":"…","code":"LAI-…",…}`): the screen shows its `msg`, its level and
 * its code. Any other program's line is shown as it is, with a code found in it.
 */
export function eventSentence(line: LogLine): EventSentence {
  const d = new Date(line.at);
  const at = Number.isNaN(d.getTime()) ? null : d;
  let level = levelOfPriority(line.priority);
  let text = (line.message ?? "").trim();
  let code = "";
  if (text.startsWith("{")) {
    try {
      const obj = JSON.parse(text) as Record<string, unknown>;
      if (obj && typeof obj === "object" && !Array.isArray(obj)) {
        level = levelOfName(obj.level) ?? level;
        const msg = typeof obj.msg === "string" ? obj.msg : typeof obj.message === "string" ? obj.message : "";
        text = msg.trim();
        if (typeof obj.code === "string" && CODE_RE.test(obj.code)) code = obj.code;
        if (text === "") {
          // A line with no message: say which fields it had, never the object itself.
          text = Object.keys(obj)
            .filter((k) => k !== "level" && k !== "time")
            .join(", ");
        }
      }
    } catch {
      // Not JSON after all: keep the text.
    }
  }
  if (code === "") code = CODE_RE.exec(text)?.[0] ?? "";
  return { at, level, text, code };
}

/** eventSentences: newest first, as the screen lists them. */
export function eventSentences(res: LogsResponse): EventSentence[] {
  const lines = Array.isArray(res.lines) ? res.lines : [];
  return lines.map(eventSentence).reverse();
}

// ---------------------------------------------------------------------------------------------------
// The drill (agent.md section 3.8.28, drill.md section 3.4)

export type CheckResultValue = "pass" | "warn" | "fail" | "skip";

export interface DrillCheck {
  id: string;
  area: string;
  result: CheckResultValue;
  code?: string;
  ms: number;
  details?: Record<string, unknown>;
}

export interface DrillDoc {
  run_id: string;
  trigger: string;
  state?: "running" | "skipped" | "aborted";
  quick: boolean;
  started_at: string;
  finished_at: string | null;
  overall: string;
  survival_ready: boolean;
  counts: { pass: number; warn: number; fail: number; skip: number };
  checks: DrillCheck[];
  code?: string;
  reason?: string;
}

/**
 * The plain name of a drill check: the key of its family (drill.md section 4.2), longest prefix
 * first, and what follows the prefix (a language, a model id) as a suffix.
 */
const CHECK_FAMILIES: [string, string][] = [
  ["drl.fence", "fence"],
  ["drl.config.offline_lint", "offlineLint"],
  ["drl.files.manifest", "manifest"],
  ["drl.files.hash", "hash"],
  ["drl.llm.answer", "answer"],
  ["drl.vlm.photo", "photo"],
  ["drl.llm.fit", "fit"],
  ["drl.kiwix.search", "library"],
  ["drl.portal.firstaid", "firstaid"],
  ["drl.stt", "stt"],
  ["drl.tts", "tts"],
  ["drl.speech.roundtrip", "roundtrip"],
  ["drl.maps.tile", "mapsTile"],
  ["drl.maps.search", "mapsSearch"],
  ["drl.portal", "portal"],
  ["drl.docs", "docs"],
  ["drl.kit.present", "kit"],
  ["drl.kit.rebuild_dry_run", "rebuild"],
  ["drl.services", "services"],
  ["drl.power", "power"],
  ["drl.clock", "clock"],
  ["drl.restore", "restore"],
];

export interface CheckName {
  /** The family key (health.check.<family>), or "" for a check this build does not know. */
  family: string;
  /** What follows the family in the id ("es", a model id), "" when nothing. */
  suffix: string;
}

export function checkName(id: string): CheckName {
  let best: [string, string] | null = null;
  for (const f of CHECK_FAMILIES) {
    if ((id === f[0] || id.startsWith(f[0] + ".")) && (!best || f[0].length > best[0].length)) best = f;
  }
  if (!best) return { family: "", suffix: "" };
  return { family: best[1], suffix: id === best[0] ? "" : id.slice(best[0].length + 1) };
}

export type DrillOutcome = "running" | "pass" | "warn" | "fail" | "aborted" | "skipped" | "manual_pass" | "manual_fail";

export interface DrillView {
  outcome: DrillOutcome;
  runId: string;
  quick: boolean;
  startedAt: Date | null;
  finishedAt: Date | null;
  survivalReady: boolean;
  counts: DrillDoc["counts"];
  /** The checks that did not pass (fail first, then warn), with their codes. */
  attention: DrillCheck[];
  /** The checks done so far (a running drill) or in all. */
  done: number;
  code: string;
  reason: string;
}

function date(s: string | null | undefined): Date | null {
  if (!s) return null;
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** drillView reads a results document (finished, running, aborted, skipped or a manual record). */
export function drillView(doc: DrillDoc): DrillView {
  const checks = Array.isArray(doc.checks) ? doc.checks : [];
  let outcome: DrillOutcome;
  if (doc.state === "running" || doc.state === "aborted" || doc.state === "skipped") outcome = doc.state;
  else if (doc.trigger === "manual_plug") outcome = doc.overall === "pass" ? "manual_pass" : "manual_fail";
  else if (doc.overall === "pass" || doc.overall === "warn" || doc.overall === "fail") outcome = doc.overall;
  else outcome = "fail";
  const rank = { fail: 0, warn: 1 } as Record<string, number>;
  const attention = checks
    .filter((c) => c.result === "fail" || c.result === "warn")
    .sort((a, b) => rank[a.result] - rank[b.result]);
  const counts = doc.counts ?? { pass: 0, warn: 0, fail: 0, skip: 0 };
  return {
    outcome,
    runId: doc.run_id,
    quick: !!doc.quick,
    startedAt: date(doc.started_at),
    finishedAt: date(doc.finished_at),
    survivalReady: !!doc.survival_ready,
    counts,
    attention,
    done: checks.length,
    code: doc.code ?? "",
    reason: doc.reason ?? "",
  };
}

/** How often the screen asks for a running drill (GET /drill?run_id=). */
export const DRILL_POLL_MS = 2000;
