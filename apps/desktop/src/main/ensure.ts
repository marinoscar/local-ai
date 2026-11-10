// "Install what's missing" (ADR-0010, docs/specs/desktop.md 4.12, agent.md 3.8.33): the desktop's calls to
// `GET`, `POST` and `DELETE /api/v1/ensure`. The page never talks to the agent: it names one of four actions over IPC and this
// module makes the request with the bearer, in the UI language (`Accept-Language`).
//
// What a person may start from here is the safe default: the automatic steps from the rebuild kit. Compiling the speech
// program and restarting a running service are never started by a button press alone: they need the page's confirmation
// dialog AND the administrator PIN (`confirm`), the same path as Rotate key. The request body is built here, never copied
// from the page, and a step name that is not one of the two confirmable ones is refused before any request is made.
import { type AgentClient } from './agent-client';
import { type Lang } from './i18n';
import type { EnsureActions } from './ipc';
import { NULL_LOGGER, type Logger } from './log';
import type { GrantRunner } from './clipboard';
import { PIN_GRANT_HEADER, codeForRequestError } from './tray';
import { ENSURE_CONFIRM_STEPS, type EnsureResult, type EnsureSnapshot, type EnsureState, type EnsureStepStatus, type EnsureStepView } from '../shared/types';

export const MODULE = 'ensure' as const;

export const ENSURE_PATH = '/api/v1/ensure';
const MAX_STEPS = 16;
const MAX_TEXT = 600;

const STATES: readonly string[] = ['complete', 'installing', 'needs_you', 'off'];
const AUTOS: readonly string[] = ['on', 'ask', 'off'];
const KINDS: readonly string[] = ['none', 'wait', 'press_button', 'confirm', 'sync', 'fix'];
const STATUSES: readonly string[] = ['ok', 'pending', 'waiting', 'needs_confirm', 'blocked', 'running', 'done', 'failed', 'cancelled'];

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const text = (value: unknown): string => (typeof value === 'string' ? value.slice(0, MAX_TEXT) : '');
const count = (value: unknown): number => (typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.min(Math.floor(value), 1000) : 0);

/** Null when the body is not the picture of agent.md 3.8.33; unknown keys are dropped, long texts cut. */
export function parseEnsureView(body: unknown): EnsureSnapshot | null {
  if (!isObject(body) || typeof body['state'] !== 'string' || !STATES.includes(body['state'])) return null;
  const next = isObject(body['next_action']) ? body['next_action'] : {};
  const progress = isObject(body['progress']) ? body['progress'] : {};
  const steps: EnsureStepView[] = [];
  for (const raw of Array.isArray(body['steps']) ? body['steps'].slice(0, MAX_STEPS) : []) {
    if (!isObject(raw) || typeof raw['id'] !== 'string' || typeof raw['status'] !== 'string' || !STATUSES.includes(raw['status'])) continue;
    steps.push({
      id: raw['id'].slice(0, 64),
      status: raw['status'] as EnsureStepStatus,
      title: text(raw['title']),
      detail: text(raw['detail']),
      needsConfirmation: raw['needs_confirmation'] === true,
      ...(typeof raw['code'] === 'string' && /^LAI-[A-Z]+-\d{3}$/.test(raw['code']) ? { code: raw['code'] } : {}),
    });
  }
  const kind = typeof next['kind'] === 'string' && KINDS.includes(next['kind']) ? (next['kind'] as EnsureSnapshot['nextAction']['kind']) : 'none';
  return {
    state: body['state'] as EnsureState,
    auto: typeof body['auto'] === 'string' && AUTOS.includes(body['auto']) ? (body['auto'] as EnsureSnapshot['auto']) : 'on',
    message: text(body['message']),
    nextAction: { kind, text: text(next['text']), ...(typeof next['step'] === 'string' ? { step: next['step'].slice(0, 64) } : {}) },
    progress: { done: count(progress['done']), total: count(progress['total']), ...(typeof progress['current'] === 'string' ? { current: progress['current'].slice(0, 64) } : {}) },
    steps,
  };
}

/** The failure an agent error envelope describes: its code, message and fix, in the language that was asked for. */
async function failureOf(response: Response): Promise<EnsureResult> {
  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
    // an empty or damaged body: the status alone decides
  }
  const envelope = isObject(body) && isObject(body['error']) ? body['error'] : {};
  return {
    ok: false,
    status: response.status,
    code: typeof envelope['code'] === 'string' ? envelope['code'] : response.status === 401 || response.status === 403 ? 'LAI-DSK-007' : 'LAI-AGT-099',
    ...(typeof envelope['message'] === 'string' ? { message: text(envelope['message']) } : {}),
    ...(typeof envelope['fix'] === 'string' ? { fix: text(envelope['fix']) } : {}),
  };
}

export interface EnsureApi {
  view(lang: Lang): Promise<EnsureResult>;
  /** `confirm` and `grant` go together: the steps a person approved, and the PIN grant that lets the agent accept them. */
  start(lang: Lang, approval?: { confirm: readonly string[]; grant: string }): Promise<EnsureResult>;
  cancel(lang: Lang): Promise<EnsureResult>;
}

export function createEnsureApi(client: Pick<AgentClient, 'request'>): EnsureApi {
  async function call(method: 'GET' | 'POST' | 'DELETE', lang: Lang, extra: { json?: unknown; headers?: Record<string, string> } = {}): Promise<EnsureResult> {
    let response: Response;
    try {
      response = await client.request(ENSURE_PATH, { method, headers: { 'Accept-Language': lang, ...(extra.headers ?? {}) }, ...(extra.json !== undefined ? { json: extra.json } : {}), timeoutMs: 15_000 });
    } catch (error) {
      return { ok: false, code: codeForRequestError(error) };
    }
    if (!response.ok) return failureOf(response);
    let body: unknown = null;
    try {
      body = await response.json();
    } catch {
      // handled below
    }
    const view = parseEnsureView(body);
    return view ? { ok: true, view, running: response.status === 202 } : { ok: false, code: 'LAI-AGT-099' };
  }
  return {
    view: (lang) => call('GET', lang),
    // The safe default is an empty object: the agent starts the automatic steps and nothing that needs a confirmation.
    start: (lang, approval) =>
      approval ? call('POST', lang, { json: { confirm: [...approval.confirm] }, headers: { [PIN_GRANT_HEADER]: approval.grant } }) : call('POST', lang, { json: {} }),
    cancel: (lang) => call('DELETE', lang),
  };
}

/** The step names a page may ask to confirm: only the two the agent holds back, each once. Null when anything else is in the list. */
export function parseConfirmSteps(raw: unknown): string[] | null {
  if (!Array.isArray(raw) || raw.length === 0 || raw.length > ENSURE_CONFIRM_STEPS.length) return null;
  const seen = new Set<string>();
  for (const id of raw) {
    if (typeof id !== 'string' || !(ENSURE_CONFIRM_STEPS as readonly string[]).includes(id) || seen.has(id)) return null;
    seen.add(id);
  }
  return [...seen];
}

/** The four calls behind `ensure.view`, `start`, `cancel` and `confirm`. */
export function createEnsureActions(deps: { api: EnsureApi; pin: GrantRunner; lang(): Lang; log?: Logger }): EnsureActions {
  const log = deps.log ?? NULL_LOGGER;
  return {
    view: () => deps.api.view(deps.lang()),
    async start() {
      const result = await deps.api.start(deps.lang());
      if (!result.ok) log.log('warn', 'app', 'install what is missing: the agent refused', { code: result.code });
      return result;
    },
    cancel: () => deps.api.cancel(deps.lang()),
    async confirm(raw) {
      const steps = parseConfirmSteps(raw);
      if (steps === null) return { ok: false, code: 'LAI-AGT-020' };
      const outcome = await deps.pin.withGrant((grant) => deps.api.start(deps.lang(), { confirm: steps, grant }), 'confirm-ensure');
      if (outcome.ok) {
        if (!outcome.value.ok) log.log('warn', 'app', 'install what is missing: the confirmation was refused', { code: outcome.value.code });
        return outcome.value;
      }
      return { ok: false, code: outcome.code ?? 'LAI-SEC-002', cancelled: true };
    },
  };
}
