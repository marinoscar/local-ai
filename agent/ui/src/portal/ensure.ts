import { useSyncExternalStore } from "react";
import { ApiError, apiFetch, apiJSON } from "./api";
import { getLang } from "./i18n/i18n";

// "Install what's missing" (ADR-0010, agent.md section 3.8.33, survival-portal.md section 4.1.8).
// The agent keeps the node complete; this file is the Portal's view of GET, POST and DELETE
// /api/v1/ensure. A Portal session may read, start and cancel; its POST body is ignored and replaced
// by the safe default (the automatic steps, no confirmation), so the button sends `{}` and the
// steps that need a person's OK (compiling, restarting a running service) are never started here.

export type EnsureState = "complete" | "installing" | "needs_you" | "off";
export type EnsureAuto = "on" | "ask" | "off";
export type EnsureStatus =
  | "ok"
  | "pending"
  | "waiting"
  | "needs_confirm"
  | "blocked"
  | "running"
  | "done"
  | "failed"
  | "cancelled";
export type EnsureStepId =
  | "apt.packages"
  | "tool.kiwix-tools"
  | "tool.pmtiles"
  | "tool.piper"
  | "whisper.restore"
  | "whisper.build"
  | "restart.kiwix";

export interface EnsureStep {
  id: EnsureStepId | string;
  status: EnsureStatus;
  needs_confirmation: boolean;
  /** The plain title and detail, in the language of the request. */
  title: string;
  detail: string;
  code?: string;
  reason?: string;
}

export interface EnsureView {
  state: EnsureState;
  auto: EnsureAuto;
  message: string;
  next_action: {
    kind: "none" | "wait" | "press_button" | "confirm" | "sync" | "fix";
    text: string;
    step?: string;
  };
  progress: { done: number; total: number; current?: string };
  steps: EnsureStep[];
  run: { id: string; result?: string } | null;
  dry_run: boolean;
  checked_at: string;
}

export const ENSURE_PATH = "/api/v1/ensure";
/** While a pass runs the card asks every second; idle, every five. */
export const POLL_BUSY_MS = 1000;
export const POLL_IDLE_MS = 5000;

/** The steps that only a person may start (compiling, restarting a running service). */
export function stepsNeedingOk(v: EnsureView): EnsureStep[] {
  return v.steps.filter((s) => s.status === "needs_confirm");
}

/** Whether the button "Install what's missing" is useful now: something is left and no pass is running. */
export function canInstall(v: EnsureView): boolean {
  if (v.state === "installing" || v.state === "complete") return false;
  return v.steps.some(
    (s) => s.status === "pending" || s.status === "waiting" || s.status === "failed",
  );
}

/** The mark that goes with a state, so colour is never the only signal. */
export const STATE_MARK: Record<EnsureState, string> = {
  complete: "✓",
  installing: "…",
  needs_you: "!",
  off: "!",
};

/** The language header: the agent writes the picture in the Accept-Language of the request. */
function langHeaders(): Record<string, string> {
  return { "Accept-Language": getLang() };
}

export function fetchEnsure(): Promise<EnsureView> {
  return apiJSON<EnsureView>(ENSURE_PATH, { headers: langHeaders() });
}

export type EnsureCall =
  | { ok: true; view: EnsureView; running: boolean }
  | { ok: false; status: number; code: string; message: string; fix: string };

async function call(
  method: "POST" | "DELETE",
  fetcher: typeof apiFetch,
): Promise<EnsureCall> {
  try {
    const res = await fetcher(ENSURE_PATH, {
      method,
      headers: { ...langHeaders(), "Content-Type": "application/json" },
      body: method === "POST" ? "{}" : undefined,
    });
    return {
      ok: true,
      view: (await res.json()) as EnsureView,
      running: res.status === 202,
    };
  } catch (e) {
    if (e instanceof ApiError)
      return { ok: false, status: e.status, code: e.code, message: e.message, fix: e.fix };
    return {
      ok: false,
      status: 0,
      code: "",
      message: e instanceof Error ? e.message : String(e),
      fix: "",
    };
  }
}

/** startEnsure is the button: POST /ensure with `{}` (a Portal session's body is replaced anyway). */
export function startEnsure(fetcher: typeof apiFetch = apiFetch): Promise<EnsureCall> {
  return call("POST", fetcher);
}

/** cancelEnsure stops the running pass; what was installed stays. */
export function cancelEnsure(fetcher: typeof apiFetch = apiFetch): Promise<EnsureCall> {
  return call("DELETE", fetcher);
}

export type EnsureStoreState =
  | { phase: "loading"; view: null }
  | { phase: "ok"; view: EnsureView }
  /** unavailable: the agent cannot answer (no session, sync stack not up, offline agent); view keeps the last good body. */
  | { phase: "unavailable"; view: EnsureView | null };

export interface EnsureDeps {
  fetchView: () => Promise<EnsureView>;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (h: unknown) => void;
}

/**
 * EnsureStore polls GET /ensure: every second while a pass runs (the progress bar moves), every five
 * seconds otherwise. An answer to the button or to cancel is put in with `accept`, so the card does
 * not wait for the next tick.
 */
export class EnsureStore {
  private state: EnsureStoreState = { phase: "loading", view: null };
  private listeners = new Set<() => void>();
  private timer: unknown = null;
  private stopped = true;

  constructor(private deps: EnsureDeps) {}

  start(): void {
    if (!this.stopped) return;
    this.stopped = false;
    void this.refetch();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer !== null) this.deps.clearTimeout(this.timer);
    this.timer = null;
  }

  subscribe = (l: () => void): (() => void) => {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  };

  getSnapshot = (): EnsureStoreState => this.state;

  /** accept shows a view the caller already has (the body of a 200 or 202) and schedules the next poll. */
  accept(view: EnsureView): void {
    if (this.stopped) return;
    this.state = { phase: "ok", view };
    this.emit();
    this.schedule(view);
  }

  async refetch(): Promise<void> {
    if (this.stopped) return;
    try {
      const view = await this.deps.fetchView();
      if (this.stopped) return;
      this.state = { phase: "ok", view };
      this.emit();
      this.schedule(view);
    } catch {
      if (this.stopped) return;
      this.state = { phase: "unavailable", view: this.state.view };
      this.emit();
      this.schedule(null);
    }
  }

  private schedule(view: EnsureView | null): void {
    if (this.timer !== null) this.deps.clearTimeout(this.timer);
    const ms = view?.state === "installing" ? POLL_BUSY_MS : POLL_IDLE_MS;
    this.timer = this.deps.setTimeout(() => void this.refetch(), ms);
  }

  private emit(): void {
    for (const l of this.listeners) l();
  }
}

let store: EnsureStore | null = null;

/** The page-wide store, created on first use. */
export function ensureStore(): EnsureStore {
  if (!store) {
    store = new EnsureStore({
      fetchView: fetchEnsure,
      setTimeout: (fn, ms) => window.setTimeout(fn, ms),
      clearTimeout: (h) => window.clearTimeout(h as number),
    });
  }
  return store;
}

export function useEnsureStore(): EnsureStoreState {
  const s = ensureStore();
  return useSyncExternalStore(s.subscribe, s.getSnapshot);
}
