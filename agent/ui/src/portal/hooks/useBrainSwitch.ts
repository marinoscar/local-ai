import { useEffect, useMemo, useSyncExternalStore } from "react";
import { ApiError, apiFetch } from "../api";

// The brain switch (survival-portal.md section 4.1.9): POST the load, then follow the model.*
// events of the stream. A load that has not finished after 300 s counts as failed (LAI-SVC-107);
// the agent restarts the previous default by itself (llama-router.md section 4.4).

export const LOAD_TIMEOUT_MS = 300_000;
export const CODE_BRAIN_FAILED = "LAI-SVC-107";
export const BRAIN_EVENTS_PATH =
  "/api/v1/events?types=model.*,mode.*,service.state,alert.*";

export type FailKind = "failed" | "timeout" | "rejected";

export type SwitchState =
  | { phase: "idle" }
  | { phase: "confirming" }
  | { phase: "loading"; seconds: number }
  | { phase: "loaded" }
  | {
      phase: "failed";
      kind: FailKind;
      code: string;
      message: string;
      fix: string;
    };

export interface SwitchEvent {
  data: string;
}

export interface SwitchEventSource {
  addEventListener(type: string, fn: (e: SwitchEvent) => void): void;
  close(): void;
}

export interface SwitchDeps {
  /** Posts the load; rejects with ApiError on a non-2xx answer (for example 429). */
  post: (id: string) => Promise<void>;
  openEvents: () => SwitchEventSource | null;
  now: () => number;
  setInterval: (fn: () => void, ms: number) => unknown;
  clearInterval: (h: unknown) => void;
}

/** The event body: the stream sends the whole event, whose `data` holds the fields (agent.md section 3.10). */
function eventData(e: SwitchEvent): Record<string, unknown> {
  try {
    const body = JSON.parse(e.data) as Record<string, unknown>;
    const inner = body.data;
    return inner && typeof inner === "object"
      ? (inner as Record<string, unknown>)
      : body;
  } catch {
    return {};
  }
}

/**
 * BrainSwitchMachine is idle -> confirming -> loading -> loaded | failed (error, timeout or a
 * rejected request). It is plain TypeScript so a fake clock and a fake event source can drive it.
 */
export class BrainSwitchMachine {
  private state: SwitchState = { phase: "idle" };
  private listeners = new Set<() => void>();
  private es: SwitchEventSource | null = null;
  private timer: unknown = null;
  private startedAt = 0;

  constructor(
    readonly id: string,
    private deps: SwitchDeps,
  ) {}

  subscribe = (l: () => void): (() => void) => {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  };

  getSnapshot = (): SwitchState => this.state;

  /** ask opens the confirmation ("Switch to X? The current brain will stop."). */
  ask(): void {
    const p = this.state.phase;
    if (p === "idle" || p === "failed" || p === "loaded")
      this.set({ phase: "confirming" });
  }

  cancel(): void {
    if (this.state.phase === "confirming") this.set({ phase: "idle" });
  }

  /** reset leaves any phase (also stops listening). */
  reset(): void {
    this.cleanup();
    this.set({ phase: "idle" });
  }

  /** dispose stops the timer and the stream (the screen was left). It keeps the listeners: React may re-run the effect. */
  dispose(): void {
    this.cleanup();
  }

  /** confirm posts the load and follows the events. */
  async confirm(): Promise<void> {
    if (this.state.phase !== "confirming") return;
    this.cleanup();
    this.startedAt = this.deps.now();
    this.set({ phase: "loading", seconds: 0 });
    // Listen first, so an event that is faster than the answer to the POST is not lost.
    this.es = this.deps.openEvents();
    this.es?.addEventListener("model.loaded", (e) => {
      if (eventData(e).model === this.id) this.succeed();
    });
    this.es?.addEventListener("model.failed", (e) => {
      const d = eventData(e);
      if (d.model !== this.id) return;
      this.fail(
        "failed",
        CODE_BRAIN_FAILED,
        typeof d.message === "string" ? d.message : "",
        "",
      );
    });
    this.timer = this.deps.setInterval(() => this.tick(), 1000);
    try {
      await this.deps.post(this.id);
    } catch (e) {
      if (this.getSnapshot().phase !== "loading") return;
      if (e instanceof ApiError)
        this.fail("rejected", e.code || CODE_BRAIN_FAILED, e.message, e.fix);
      else this.fail("rejected", CODE_BRAIN_FAILED, "", "");
    }
  }

  /** notifyLoaded lets the screen finish when a status poll shows the model loaded (events missed). */
  notifyLoaded(): void {
    if (this.state.phase === "loading") this.succeed();
  }

  private tick(): void {
    if (this.state.phase !== "loading") return;
    const ms = this.deps.now() - this.startedAt;
    if (ms >= LOAD_TIMEOUT_MS) {
      this.fail("timeout", CODE_BRAIN_FAILED, "", "");
      return;
    }
    const seconds = Math.floor(ms / 1000);
    if (seconds !== this.state.seconds) this.set({ phase: "loading", seconds });
  }

  private succeed(): void {
    this.cleanup();
    this.set({ phase: "loaded" });
  }

  private fail(
    kind: FailKind,
    code: string,
    message: string,
    fix: string,
  ): void {
    this.cleanup();
    this.set({ phase: "failed", kind, code, message, fix });
  }

  private cleanup(): void {
    if (this.timer !== null) this.deps.clearInterval(this.timer);
    this.timer = null;
    this.es?.close();
    this.es = null;
  }

  private set(s: SwitchState): void {
    this.state = s;
    for (const l of this.listeners) l();
  }
}

/** loadBody: a Portal session's body is forced by the agent; a full session also unloads the others. */
export function loadBody(session: string): {
  wait: false;
  unload_others?: true;
} {
  return session === "full"
    ? { wait: false, unload_others: true }
    : { wait: false };
}

export function realDeps(session: string): SwitchDeps {
  return {
    post: async (id) => {
      await apiFetch(`/api/v1/models/${encodeURIComponent(id)}/load`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(loadBody(session)),
      });
    },
    openEvents: () =>
      typeof EventSource === "undefined"
        ? null
        : new EventSource(BRAIN_EVENTS_PATH),
    now: () => Date.now(),
    setInterval: (fn, ms) => window.setInterval(fn, ms),
    clearInterval: (h) => window.clearInterval(h as number),
  };
}

/** useBrainSwitch drives the switch to model `id` (also used by the photo screen, PORTAL-08). */
export function useBrainSwitch(id: string, session: string) {
  const machine = useMemo(
    () => new BrainSwitchMachine(id, realDeps(session)),
    [id, session],
  );
  useEffect(() => () => machine.dispose(), [machine]);
  const state = useSyncExternalStore(machine.subscribe, machine.getSnapshot);
  return {
    state,
    ask: () => machine.ask(),
    cancel: () => machine.cancel(),
    confirm: () => void machine.confirm(),
    reset: () => machine.reset(),
    notifyLoaded: () => machine.notifyLoaded(),
  };
}
