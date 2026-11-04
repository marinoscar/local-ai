import { useSyncExternalStore } from "react";
import { apiJSON, onSessionEnded } from "./api";
import { applyStatusLang } from "./i18n/i18n";

// The data the Portal shows: GET /api/v1/portal/status (survival-portal.md section 3.5), polled
// every 5 s and refetched on each event of the stream when a session exists.

export type CapState = "ready" | "degraded" | "unavailable";
export type CapName =
  "chat" | "photo" | "talk" | "library" | "maps" | "manuals";

export interface Capability {
  state: CapState;
  code?: string;
  reason?: string;
}

export interface Model {
  id: string;
  display_name: string;
  vision?: boolean;
}

export interface Alert {
  code: string;
  severity: string;
  title: string;
  fix: string;
}

export interface FirstAidEntry {
  key: string;
  title: string;
  url: string;
  available: boolean;
}

export interface PortalStatus {
  schema_version: number;
  mode: string;
  language: string;
  emergency_number: string;
  session: "none" | "portal" | "full";
  power: {
    on_ac: boolean;
    battery_pct: number | null;
    minutes_left: number | null;
    state: string;
    low: boolean;
    critical: boolean;
  };
  clock_ok: boolean;
  brain: { loaded: Model[]; loading: Model | null };
  capabilities: Partial<Record<CapName, Capability>>;
  first_aid: FirstAidEntry[];
  alerts: Alert[];
}

export type Level = "green" | "amber" | "red";

/**
 * statusLevel is the colour rule of the status line (survival-portal.md section 4.1.1): red when
 * chat or library is unavailable; else amber when any capability is not ready; else green.
 */
export function statusLevel(caps: PortalStatus["capabilities"]): Level {
  if (
    caps.chat?.state === "unavailable" ||
    caps.library?.state === "unavailable"
  )
    return "red";
  for (const c of Object.values(caps)) {
    if (c && c.state !== "ready") return "amber";
  }
  return "green";
}

/** Battery bands (survival-portal.md section 4.1.1): amber at 20 % or less, critical at 10 % or less, on battery. */
export type BatteryLevel = "ok" | "low" | "critical";

export function batteryLevel(p: PortalStatus["power"]): BatteryLevel {
  if (p.on_ac || p.battery_pct === null) return "ok";
  if (p.critical || p.battery_pct <= 10) return "critical";
  if (p.low || p.battery_pct <= 20) return "low";
  return "ok";
}

/** Capabilities that need a session to work (the lock icon): Chat, Photo, Talk and Switch brain. */
export const SESSION_CAPS = ["chat", "photo", "talk", "brain"] as const;

export type StatusState =
  | { phase: "loading"; status: null; lost: false }
  | { phase: "ok"; status: PortalStatus; lost: false }
  /** lost: the last fetch failed; status keeps the last good body, if any. */
  | { phase: "lost"; status: PortalStatus | null; lost: true };

const EVENT_TYPES = [
  "mode.changed",
  "mode.step",
  "model.loading",
  "model.loaded",
  "model.unloaded",
  "model.failed",
  "model.default_changed",
  "service.state",
  "drive.mounted",
  "drive.unmounted",
  "alert.raised",
  "alert.cleared",
];

export const POLL_MS = 5000;
export const STATUS_PATH = "/api/v1/portal/status";
export const EVENTS_PATH =
  "/api/v1/events?types=mode.*,model.*,service.state,drive.*,alert.*";

interface EventSourceLike {
  addEventListener(type: string, fn: () => void): void;
  close(): void;
}

export interface StoreDeps {
  fetchStatus: () => Promise<PortalStatus>;
  openEvents: (() => EventSourceLike) | null;
  setInterval: (fn: () => void, ms: number) => unknown;
  clearInterval: (h: unknown) => void;
  /** Called with the status language after each good fetch. */
  onLanguage?: (lang: string) => void;
  /** Called with the failure of a fetch (client error ring, section 5 of the issue). */
  onError?: (e: unknown) => void;
}

/**
 * StatusStore polls the status and keeps the last good body. A failed fetch flips `lost` (banner,
 * retry on the next tick); a 401 LAI-SVC-110 on any call (api.ts) triggers a refetch.
 */
export class StatusStore {
  private state: StatusState = { phase: "loading", status: null, lost: false };
  private listeners = new Set<() => void>();
  private timer: unknown = null;
  private events: EventSourceLike | null = null;
  private stopped = false;

  constructor(private deps: StoreDeps) {}

  start(): void {
    this.stopped = false;
    void this.refetch();
    this.timer = this.deps.setInterval(() => void this.refetch(), POLL_MS);
  }

  stop(): void {
    this.stopped = true;
    if (this.timer !== null) this.deps.clearInterval(this.timer);
    this.timer = null;
    this.events?.close();
    this.events = null;
  }

  subscribe = (l: () => void): (() => void) => {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  };

  getSnapshot = (): StatusState => this.state;

  async refetch(): Promise<void> {
    try {
      const status = await this.deps.fetchStatus();
      if (this.stopped) return;
      this.state = { phase: "ok", status, lost: false };
      this.deps.onLanguage?.(status.language);
      this.syncEvents(status.session);
    } catch (e) {
      if (this.stopped) return;
      this.deps.onError?.(e);
      this.state = { phase: "lost", status: this.state.status, lost: true };
    }
    this.emit();
  }

  private syncEvents(session: PortalStatus["session"]): void {
    if (session === "none") {
      this.events?.close();
      this.events = null;
      return;
    }
    if (this.events || !this.deps.openEvents) return;
    const es = this.deps.openEvents();
    for (const type of EVENT_TYPES)
      es.addEventListener(type, () => void this.refetch());
    this.events = es;
  }

  private emit(): void {
    for (const l of this.listeners) l();
  }
}

/** A small ring of client errors for the Status screen's "Details" (PORTAL-06). Never sent anywhere. */
const ring: string[] = [];
export function recordError(e: unknown): void {
  ring.push(
    `${new Date().toISOString()} ${e instanceof Error ? e.message : String(e)}`,
  );
  if (ring.length > 50) ring.shift();
}
export function clientErrors(): readonly string[] {
  return ring;
}

let store: StatusStore | null = null;

/** startStatus creates the page-wide store (once) and starts polling. */
export function startStatus(): StatusStore {
  if (store) return store;
  store = new StatusStore({
    fetchStatus: () => apiJSON<PortalStatus>(STATUS_PATH),
    openEvents:
      typeof EventSource === "undefined"
        ? null
        : () => new EventSource(EVENTS_PATH),
    setInterval: (fn, ms) => window.setInterval(fn, ms),
    clearInterval: (h) => window.clearInterval(h as number),
    onLanguage: applyStatusLang,
    onError: recordError,
  });
  onSessionEnded(() => void store?.refetch());
  store.start();
  return store;
}

export function useStatus(): StatusState {
  const s = startStatus();
  return useSyncExternalStore(s.subscribe, s.getSnapshot);
}

/** refreshStatus asks for a fresh body now (after Start survival mode). */
export function refreshStatus(): Promise<void> {
  return startStatus().refetch();
}
