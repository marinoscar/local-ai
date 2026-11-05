// The native PIN prompt (docs/specs/desktop.md 4.6): a modal window owned by the main process, `POST /api/v1/pin/verify`,
// and the one-shot grant. The desktop shows it only for actions it starts itself (leaving survival from the tray,
// Rotate key); the agent UI has its own PIN dialogs.
//
// The PIN crosses IPC once, from the PIN window to `submit()`, and goes straight into the request body: it is not stored,
// not logged and not returned. The grant is handed to exactly one action (`withGrant`) and then dropped, although the
// agent would accept it again for five minutes.
import { type AgentClient } from './agent-client';
import type { Lang } from './i18n';
import { NULL_LOGGER, type Logger } from './log';
import { type PinPrompt, codeForRequestError } from './tray';
import type { PinResult } from '../shared/types';

export const MODULE = 'pin' as const;

/** What the window says it is for. `leave-survival` is the tray's (and DSK-09's), `rotate-key` the connection-info page's. */
export type PinPurpose = 'leave-survival' | 'rotate-key';
export const PIN_PURPOSES: readonly PinPurpose[] = ['leave-survival', 'rotate-key'];

/** `pg_` and 43 base64url characters (agent.md 3.8.3). */
export const GRANT_PATTERN = /^pg_[A-Za-z0-9_-]{43}$/;
/** 4 to 8 digits, nothing else (agent.md 3.8.3). */
export const PIN_PATTERN = /^\d{4,8}$/;

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

// ---------------------------------------------------------------------------------------------------------------------
// The agent
// ---------------------------------------------------------------------------------------------------------------------

export type VerifyOutcome =
  | { ok: true; grant: string }
  | { ok: false; code: string; fix?: string; remainingAttempts?: number; retryAfterS?: number };

export interface PinApi {
  /** `POST /api/v1/pin/verify {pin}` with the bearer. */
  verify(pin: string): Promise<VerifyOutcome>;
}

export function createPinApi(client: Pick<AgentClient, 'request'>): PinApi {
  return {
    async verify(pin) {
      let response: Response;
      try {
        response = await client.request('/api/v1/pin/verify', { method: 'POST', json: { pin } });
      } catch (error) {
        return { ok: false, code: codeForRequestError(error) };
      }
      let body: unknown = null;
      try {
        body = await response.json();
      } catch {
        // an empty or damaged body: the status alone decides
      }
      if (response.status === 200) {
        const grant = isObject(body) ? body['grant'] : undefined;
        return typeof grant === 'string' && GRANT_PATTERN.test(grant) ? { ok: true, grant } : { ok: false, code: 'LAI-AGT-099' };
      }
      const envelope = isObject(body) && isObject(body['error']) ? body['error'] : {};
      const details = isObject(envelope['details']) ? envelope['details'] : {};
      const retryHeader = Number(response.headers.get('retry-after'));
      const retry = typeof details['retry_after_s'] === 'number' ? details['retry_after_s'] : Number.isFinite(retryHeader) && retryHeader > 0 ? retryHeader : undefined;
      return {
        ok: false,
        code: typeof envelope['code'] === 'string' ? envelope['code'] : 'LAI-AGT-099',
        ...(typeof envelope['fix'] === 'string' ? { fix: envelope['fix'] } : {}),
        ...(typeof details['remaining_attempts'] === 'number' ? { remainingAttempts: details['remaining_attempts'] } : {}),
        ...(retry !== undefined ? { retryAfterS: retry } : {}),
      };
    },
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// The service
// ---------------------------------------------------------------------------------------------------------------------

/** The modal window, as the service sees it. The real one is `WindowManager.openModal('pin', ...)` (index.ts). */
export interface PinWindowHandle {
  /** The webContents id: `pinSubmit` and `pinCancel` are accepted from this one only. */
  readonly id: number;
  focus(): void;
  close(): void;
  isDestroyed(): boolean;
  onClosed(listener: () => void): void;
}

export interface PinServiceDeps {
  api: PinApi;
  open(purpose: PinPurpose, lang: Lang): PinWindowHandle;
  lang(): Lang;
  log?: Logger;
}

export type GrantOutcome = { ok: true; grant: string } | { ok: false; code?: string; cancelled?: boolean };

export type WithGrantOutcome<T> = { ok: true; value: T } | { ok: false; code?: string; cancelled?: boolean };

interface Session {
  window: PinWindowHandle;
  busy: boolean;
  /** The agent's last refusal, shown in the window; reported when the user then closes it. */
  failure: string | undefined;
  settle(outcome: GrantOutcome): void;
}

export class PinService implements PinPrompt {
  readonly #d: PinServiceDeps;
  readonly #log: Logger;
  #session: Session | null = null;

  constructor(deps: PinServiceDeps) {
    this.#d = deps;
    this.#log = deps.log ?? NULL_LOGGER;
  }

  /** The webContents id of the open PIN window, or null: what `pinWindowId` of the IPC table reads. */
  windowId(): number | null {
    const session = this.#session;
    return session && !session.window.isDestroyed() ? session.window.id : null;
  }

  /**
   * Opens the PIN window and resolves with the grant once the agent accepted the PIN, or without one when the user
   * closed the window (`cancelled`). A refusal the window already showed (wrong PIN, locked, no PIN set) comes back as
   * `cancelled` with its code: the user has seen it, so the caller shows nothing more. Only one prompt at a time: a
   * second request focuses the first and answers `cancelled`.
   */
  requestGrant(purpose: PinPurpose): Promise<GrantOutcome> {
    const existing = this.#session;
    if (existing) {
      if (!existing.window.isDestroyed()) existing.window.focus();
      return Promise.resolve({ ok: false, cancelled: true });
    }
    return new Promise<GrantOutcome>((resolve) => {
      const window = this.#d.open(purpose, this.#d.lang());
      let done = false;
      const session: Session = {
        window,
        busy: false,
        failure: undefined,
        settle: (outcome) => {
          if (done) return;
          done = true;
          if (this.#session === session) this.#session = null;
          if (!window.isDestroyed()) window.close();
          resolve(outcome);
        },
      };
      this.#session = session;
      window.onClosed(() => session.settle({ ok: false, cancelled: true, ...(session.failure !== undefined ? { code: session.failure } : {}) }));
    });
  }

  /**
   * Runs `action` once with a fresh grant. The grant is not kept anywhere: it is an argument of this one call.
   * (`leave-survival` is what the tray and the survival UX ask for.)
   */
  async withGrant<T>(action: (grant: string) => Promise<T>, purpose: PinPurpose = 'leave-survival'): Promise<WithGrantOutcome<T>> {
    const asked = await this.requestGrant(purpose);
    if (!asked.ok) return asked;
    return { ok: true, value: await action(asked.grant) };
  }

  /** The PIN window's `pinSubmit`. `senderId` is the calling webContents. */
  async submit(senderId: number, pin: unknown): Promise<PinResult> {
    const session = this.#session;
    if (!session || session.window.id !== senderId) return { ok: false, code: 'no_prompt' };
    if (session.busy) return { ok: false, code: 'busy' };
    // The window only lets 4 to 8 digits through; anything else is not worth a counted try at the agent.
    if (typeof pin !== 'string' || !PIN_PATTERN.test(pin)) return { ok: false, code: 'invalid_format' };
    session.busy = true;
    let outcome: VerifyOutcome;
    try {
      outcome = await this.#d.api.verify(pin);
    } finally {
      session.busy = false;
    }
    // The window was closed while the agent was thinking: the grant is dropped unused.
    if (this.#session !== session) return { ok: false, code: 'no_prompt' };
    if (outcome.ok) {
      this.#log.log('info', 'app', 'PIN accepted');
      session.settle({ ok: true, grant: outcome.grant });
      return { ok: true };
    }
    session.failure = outcome.code;
    this.#log.log('warn', 'app', 'PIN refused', { code: outcome.code });
    return {
      ok: false,
      code: outcome.code,
      ...(outcome.fix !== undefined ? { fix: outcome.fix } : {}),
      ...(outcome.remainingAttempts !== undefined ? { remainingAttempts: outcome.remainingAttempts } : {}),
      ...(outcome.retryAfterS !== undefined ? { retryAfterS: outcome.retryAfterS } : {}),
    };
  }

  /** The PIN window's Cancel button. */
  cancel(senderId: number): void {
    const session = this.#session;
    if (session && session.window.id === senderId) session.settle({ ok: false, cancelled: true, ...(session.failure !== undefined ? { code: session.failure } : {}) });
  }
}
