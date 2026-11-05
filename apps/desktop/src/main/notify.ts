// Native notifications from the main process (docs/specs/desktop.md 4.4): mode changes, a model that failed to load,
// sync results, drives plugged and unplugged, alerts of severity warning or higher, and the "no internet for 30
// minutes" suggestion. Text in the UI language, at most one per event kind per 5 minutes.
// No Electron import: the notification itself, the clock and the language are injected, so the whole module is unit-tested.
// Web notifications are never used (a renderer cannot be trusted to show them, and the agent UI has its own banners).
import type { AgentEvent } from './agent-client';
import { type Lang, type NoticeCode, MODE_WORDS, NOTICE_COPY, NOTIFY_TEXT, fill, pick } from './i18n';
import { type Logger, NULL_LOGGER } from './log';

export const MODULE = 'notify' as const;

/** "At most one notification per event kind per 5 minutes." */
export const NOTIFY_WINDOW_MS = 5 * 60 * 1000;

/** What a notification says; the kind is its rate-limit key. */
export interface NotifySpec {
  kind: string;
  title: string;
  body: string;
}

/** The one thing an Electron `Notification` does for us. The adapter is `createElectronNotificationPort` in tray.ts. */
export interface NotificationPort {
  /** False when the desktop cannot show notifications at all (no `Notification.isSupported()`). */
  supported(): boolean;
  show(notification: { title: string; body: string }, onClick: () => void): void;
}

/**
 * One notification per kind per window. A suppressed notification does not extend the window: a burst of ten alerts in
 * five minutes shows the first, and the next one is allowed five minutes after that first, not after the tenth.
 */
export class RateLimiter {
  readonly #windowMs: number;
  readonly #clock: () => number;
  readonly #last = new Map<string, number>();

  constructor(windowMs: number, clock: () => number) {
    this.#windowMs = windowMs;
    this.#clock = clock;
  }

  /** True (and the time is recorded) when `kind` has not been allowed within the window. */
  allow(kind: string): boolean {
    const now = this.#clock();
    const last = this.#last.get(kind);
    if (last !== undefined && now - last < this.#windowMs) return false;
    this.#last.set(kind, now);
    // The map holds a handful of kinds, but never let it grow: drop what is long expired.
    if (this.#last.size > 64) {
      for (const [key, at] of this.#last) if (now - at >= this.#windowMs) this.#last.delete(key);
    }
    return true;
  }
}

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const text = (value: unknown): string | undefined => (typeof value === 'string' && value.trim() !== '' ? value.trim() : undefined);

/** The severities that deserve a notification (desktop.md 4.4: warning or more). */
export function isNotifiableSeverity(severity: unknown): boolean {
  return severity === 'warning' || severity === 'error' || severity === 'critical';
}

/**
 * The notification an agent event asks for, in `lang`, or null when it asks for none (an alert below warning, a sync
 * that was cancelled or skipped, an event this module does not announce). Pure.
 */
export function describeEvent(event: Pick<AgentEvent, 'type' | 'data'>, lang: Lang): NotifySpec | null {
  const data = isObject(event.data) ? event.data : {};
  switch (event.type) {
    case 'mode.changed': {
      const to = text(data['to']);
      if (to !== 'online' && to !== 'offline' && to !== 'survival') return null;
      // One kind per target: switching to Survival right after a switch to Offline must not be swallowed.
      return {
        kind: `mode.changed.${to}`,
        title: pick(NOTIFY_TEXT.modeChangedTitle, lang),
        body: fill(pick(NOTIFY_TEXT.modeChangedBody, lang), { mode: pick(MODE_WORDS[to], lang) }),
      };
    }
    case 'model.failed': {
      const model = text(data['model']) ?? '?';
      const code = text(data['code']);
      const template = code === undefined ? NOTIFY_TEXT.modelFailedBodyNoCode : NOTIFY_TEXT.modelFailedBody;
      return { kind: 'model.failed', title: pick(NOTIFY_TEXT.modelFailedTitle, lang), body: fill(pick(template, lang), { model, code: code ?? '' }) };
    }
    case 'sync.done': {
      const state = text(data['state']);
      if (state === 'failed') return { kind: 'sync.failed', title: pick(NOTIFY_TEXT.syncFailedTitle, lang), body: pick(NOTIFY_TEXT.syncFailedBody, lang) };
      if (state !== 'done') return null;
      const totals = isObject(data['totals']) ? data['totals'] : {};
      const failed = typeof totals['failed'] === 'number' ? totals['failed'] : 0;
      return {
        kind: 'sync.done',
        title: pick(NOTIFY_TEXT.syncDoneTitle, lang),
        body: failed > 0 ? fill(pick(NOTIFY_TEXT.syncPartialBody, lang), { failed: String(failed) }) : pick(NOTIFY_TEXT.syncDoneBody, lang),
      };
    }
    case 'drive.mounted':
      return {
        kind: 'drive.mounted',
        title: pick(NOTIFY_TEXT.driveMountedTitle, lang),
        body: pick(data['read_only'] === true ? NOTIFY_TEXT.driveMountedReadOnlyBody : NOTIFY_TEXT.driveMountedBody, lang),
      };
    case 'drive.unmounted':
      return {
        kind: 'drive.unmounted',
        title: pick(NOTIFY_TEXT.driveUnmountedTitle, lang),
        // `safe_to_unplug: false` is a surprise removal: say so (ark-sync.md 3.6).
        body: pick(data['safe_to_unplug'] === false ? NOTIFY_TEXT.driveUnmountedUnsafeBody : NOTIFY_TEXT.driveUnmountedSafeBody, lang),
      };
    case 'alert.raised': {
      if (!isNotifiableSeverity(data['severity'])) return null;
      const title = text(data['title']);
      const fix = text(data['fix']);
      const code = text(data['code']);
      if (title === undefined && code === undefined) return null;
      // The agent writes the title and the fix in its own language; the code is always shown, so it can be looked up.
      return {
        kind: 'alert.raised',
        title: pick(NOTIFY_TEXT.alertTitle, lang),
        body: [title ?? code, fix, title !== undefined && code !== undefined ? `(${code})` : undefined].filter((part) => part !== undefined).join(' '),
      };
    }
    default:
      return null;
  }
}

/** The "no internet for 30 minutes" suggestion ([modes.md](../../../../docs/architecture/modes.md) section 4). Never changes the mode. */
export function describeSuggestion(lang: Lang): NotifySpec {
  return { kind: 'suggest.survival', title: pick(NOTIFY_TEXT.suggestTitle, lang), body: pick(NOTIFY_TEXT.suggestBody, lang) };
}

export interface NotifierDeps {
  port: NotificationPort;
  lang(): Lang;
  /** Milliseconds, any epoch; `Date.now` in the app, a fake in the tests. */
  clock(): number;
  /** A click on a notification brings the main window forward. */
  onActivate(): void;
  windowMs?: number;
  log?: Logger;
}

export class Notifier {
  readonly #deps: NotifierDeps;
  readonly #limiter: RateLimiter;
  readonly #log: Logger;

  constructor(deps: NotifierDeps) {
    this.#deps = deps;
    this.#limiter = new RateLimiter(deps.windowMs ?? NOTIFY_WINDOW_MS, deps.clock);
    this.#log = deps.log ?? NULL_LOGGER;
  }

  /** Shows the notification an event asks for, if any and if its kind is not rate limited. True when one was shown. */
  notifyEvent(event: Pick<AgentEvent, 'type' | 'data'>): boolean {
    const spec = describeEvent(event, this.#deps.lang());
    return spec !== null && this.send(spec);
  }

  /** The 30-minute suggestion. The caller decides when `/mode.suggest_survival` turned true. */
  suggestSurvival(): boolean {
    return this.send(describeSuggestion(this.#deps.lang()));
  }

  /** Rate limits by `spec.kind`, then shows. A desktop without notifications drops the message (it is in the log). */
  send(spec: NotifySpec): boolean {
    if (!this.#limiter.allow(spec.kind)) return false;
    if (!this.#deps.port.supported()) {
      this.#log.log('debug', 'app', 'notifications are not supported on this desktop', { kind: spec.kind });
      return false;
    }
    this.#deps.port.show({ title: spec.title, body: spec.body }, this.#deps.onActivate);
    return true;
  }

  /** A one-time notice with a doctor code (LAI-DSK-013, LAI-DSK-014); `{name}` placeholders of its text come from `values`. Not rate limited: the caller decides when it is shown. */
  notice(code: NoticeCode, values: Readonly<Record<string, string>> = {}): boolean {
    const lang = this.#deps.lang();
    const copy = NOTICE_COPY[code];
    this.#log.log('warn', 'tray', pick(copy.title, 'en'), { code });
    if (!this.#deps.port.supported()) return false;
    this.#deps.port.show({ title: `${pick(copy.title, lang)} (${code})`, body: fill(`${pick(copy.message, lang)} ${pick(copy.fix, lang)}`, values) }, this.#deps.onActivate);
    return true;
  }
}
