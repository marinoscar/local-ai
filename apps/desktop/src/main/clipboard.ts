// The node's llama API key: `GET /api/v1/key`, the connection info the connection-info page shows, the copy to the
// clipboard with its timed clear, and `POST /api/v1/key/rotate` (docs/specs/desktop.md 4.6, agent.md 3.8.13 and 3.8.14).
//
// The key lives in this process only for the length of one call: it is never logged (the logger scrubs tokens, and no
// line here names the key), never written to disk by this app, and never sent to a renderer except by `revealKey`
// (an explicit Reveal; each one re-reads `GET /key`, so the agent audits it). The clipboard timer keeps only a sha256 of
// the key, to recognise it at clear time.
import { createHash } from 'node:crypto';
import { type AgentClient } from './agent-client';
import { CLOUD_HOST } from './egress-guard';
import { type Bilingual, type Lang, fill, pick } from './i18n';
import type { KeyActions } from './ipc';
import { NULL_LOGGER, type Logger } from './log';
import type { PinPurpose, WithGrantOutcome } from './pin';
import { PIN_GRANT_HEADER, type Timers, codeForRequestError, realTimers } from './tray';
import type { ConnectionInfo, ConnectionSnippets, CopyKeyResult, KeyFailure, RotateKeyResult } from '../shared/types';

export const MODULE = 'clipboard' as const;

/** "It will be cleared from the clipboard in 60 seconds." (desktop.md 4.6) */
export const CLIPBOARD_CLEAR_MS = 60_000;
/** The route may restart llama and reload the models: 15 minutes (agent.md 3.8). */
export const ROTATE_TIMEOUT_MS = 15 * 60 * 1000;
export const LOCAL_BASE_URL = 'http://127.0.0.1:8080/v1';
/** The lab gateway: it needs a `lai_` key created in the cloud, not this node's key. */
export const LAB_BASE_URL = `https://${CLOUD_HOST}/v1`;
/** The first line of the OpenCode tab's "write the key file" command. */
export const OPENCODE_KEY_FILE = '~/.config/opencode/llama-api-key';

const KEY_PATTERN = /^[0-9a-f]{64}$/;
const MAX_TEXT = 8000;
const MAX_URL = 300;
const MAX_MODELS = 200;

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/** `ab12…9f3c`: the first and last four characters, the form the agent and the audit log show too. */
export function maskKey(key: string): string {
  return key.length >= 8 ? `${key.slice(0, 4)}…${key.slice(-4)}` : '…';
}

const sha256 = (text: string): string => createHash('sha256').update(text).digest('hex');

// ---------------------------------------------------------------------------------------------------------------------
// The agent's /key answer
// ---------------------------------------------------------------------------------------------------------------------

/** `GET /key`, validated. `key` is the only secret in it. */
export interface KeyMaterial {
  key: string;
  baseUrls: { local: string; tailnet: string | null; public: string | null };
  models: string[];
  snippets: Partial<Record<'curl' | 'env' | 'opencode' | 'openai_python', string>>;
}

const url = (value: unknown): string | null => (typeof value === 'string' && value.length > 0 && value.length <= MAX_URL ? value : null);

/** Null when the body is not the shape of agent.md 3.8.13 (no key, or a key that is not 64 lowercase hex digits). */
export function parseKeyResponse(body: unknown): KeyMaterial | null {
  if (!isObject(body) || typeof body['key'] !== 'string' || !KEY_PATTERN.test(body['key'])) return null;
  const base = isObject(body['base_urls']) ? body['base_urls'] : {};
  const models = Array.isArray(body['models']) ? body['models'].filter((m): m is string => typeof m === 'string' && m.length <= MAX_URL).slice(0, MAX_MODELS) : [];
  const given = isObject(body['snippets']) ? body['snippets'] : {};
  const snippets: KeyMaterial['snippets'] = {};
  for (const name of ['curl', 'env', 'openai_python'] as const) {
    const value = given[name];
    if (typeof value === 'string' && value.length <= MAX_TEXT) snippets[name] = value;
  }
  const opencode = given['opencode'];
  if (typeof opencode === 'string' && opencode.length <= MAX_TEXT) snippets.opencode = opencode;
  else if (isObject(opencode)) {
    const text = JSON.stringify(opencode, null, 2);
    if (text.length <= MAX_TEXT) snippets.opencode = text;
  }
  return { key: body['key'], baseUrls: { local: url(base['local']) ?? LOCAL_BASE_URL, tailnet: url(base['tailnet']), public: url(base['public']) }, models, snippets };
}

/** What the agent would have sent, for an older agent that sends no snippets: placeholders only, local URL only. */
function defaultSnippets(localUrl: string, model: string): ConnectionSnippets {
  const opencode = {
    provider: {
      'local-llama': {
        npm: '@ai-sdk/openai-compatible',
        options: { baseURL: localUrl, apiKey: `{file:${OPENCODE_KEY_FILE}}` },
        models: { [model]: { limit: { context: 32768, output: 8192 } } },
      },
    },
  };
  return {
    curl: `curl -s ${localUrl}/chat/completions -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"model":"${model}","messages":[{"role":"user","content":"Hello"}]}'`,
    env: `OPENAI_BASE_URL=${localUrl}\nOPENAI_API_KEY=<key>`,
    opencode: JSON.stringify(opencode, null, 2),
    openaiPython: `from openai import OpenAI\nclient = OpenAI(base_url="${localUrl}", api_key=KEY)`,
  };
}

/**
 * The page's view of the key: everything except the key. A snippet that happens to contain the key (the agent promises
 * it never does) has it replaced by `<key>`, so the masked page cannot leak it.
 */
export function buildConnectionInfo(material: KeyMaterial): ConnectionInfo {
  const defaults = defaultSnippets(material.baseUrls.local, material.models[0] ?? 'model');
  const clean = (text: string | undefined, fallback: string): string => (text ?? fallback).split(material.key).join('<key>');
  return {
    ok: true,
    localUrl: material.baseUrls.local,
    tailnetUrl: material.baseUrls.tailnet,
    publicUrl: material.baseUrls.public,
    labUrl: LAB_BASE_URL,
    keyMasked: maskKey(material.key),
    models: material.models,
    snippets: {
      curl: clean(material.snippets.curl, defaults.curl),
      env: clean(material.snippets.env, defaults.env),
      opencode: clean(material.snippets.opencode, defaults.opencode),
      openaiPython: clean(material.snippets.openai_python, defaults.openaiPython),
    },
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// The key API
// ---------------------------------------------------------------------------------------------------------------------

export type ShowKeyResult = { ok: true; material: KeyMaterial } | KeyFailure;

export interface KeyApi {
  /** `GET /api/v1/key` with the bearer: the agent writes `key.show` to its audit log on every call. */
  show(): Promise<ShowKeyResult>;
  /** `POST /api/v1/key/rotate {restart: true}` with the PIN grant of the one call. */
  rotate(grant: string): Promise<RotateKeyResult>;
}

/** The failure an agent error envelope describes, with its code, message and fix (never a body that could hold the key). */
async function failureOf(response: Response): Promise<KeyFailure & { keyRotated?: boolean; fingerprint?: string }> {
  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
    // an empty or damaged body: the status alone decides
  }
  const envelope = isObject(body) && isObject(body['error']) ? body['error'] : {};
  const details = isObject(envelope['details']) ? envelope['details'] : {};
  return {
    ok: false,
    code: typeof envelope['code'] === 'string' ? envelope['code'] : response.status === 401 || response.status === 403 ? 'LAI-DSK-007' : 'LAI-AGT-099',
    ...(typeof envelope['message'] === 'string' ? { message: envelope['message'] } : {}),
    ...(typeof envelope['fix'] === 'string' ? { fix: envelope['fix'] } : {}),
    ...(details['key_rotated'] === true ? { keyRotated: true } : {}),
    ...(typeof details['fingerprint'] === 'string' ? { fingerprint: details['fingerprint'] } : {}),
  };
}

const strings = (value: unknown): string[] => (Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string').slice(0, 50) : []);

export function createKeyApi(client: Pick<AgentClient, 'request'>): KeyApi {
  return {
    async show() {
      let response: Response;
      try {
        response = await client.request('/api/v1/key');
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
      const material = parseKeyResponse(body);
      return material ? { ok: true, material } : { ok: false, code: 'LAI-AGT-099' };
    },
    async rotate(grant) {
      let response: Response;
      try {
        response = await client.request('/api/v1/key/rotate', {
          method: 'POST',
          json: { restart: true },
          headers: { [PIN_GRANT_HEADER]: grant },
          timeoutMs: ROTATE_TIMEOUT_MS,
        });
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
      if (!isObject(body) || typeof body['fingerprint'] !== 'string') return { ok: false, code: 'LAI-AGT-099' };
      return {
        ok: true,
        fingerprint: body['fingerprint'],
        rotatedAt: typeof body['rotated_at'] === 'string' ? body['rotated_at'] : '',
        cloudSynced: body['cloud_synced'] === true,
        restoredModels: strings(body['restored_models']),
        clientsToUpdate: strings(body['clients_to_update']),
      };
    },
  };
}

// ---------------------------------------------------------------------------------------------------------------------
// Copy API key
// ---------------------------------------------------------------------------------------------------------------------

/** The part of Electron's `clipboard` that is used (its methods answer promises since Electron 44; a plain value works too). */
export interface ClipboardPort {
  readText(): string | Promise<string>;
  writeText(text: string): void | Promise<void>;
  clear(): void | Promise<void>;
}

export interface KeyCopierDeps {
  keys: Pick<KeyApi, 'show'>;
  clipboard: ClipboardPort;
  timers?: Timers;
  /** Default 60 s. The e2e test copy shortens it (`LOCALAI_DESKTOP_CLIPBOARD_CLEAR_MS`, index.ts). */
  clearAfterMs?: number;
  log?: Logger;
}

/**
 * Copy API key: reads the key from the agent (audited there), puts it on the clipboard and, after the delay, clears the
 * clipboard if and only if it still holds exactly that key. Something else the user copied meanwhile is left alone.
 */
export class KeyCopier {
  readonly #d: KeyCopierDeps;
  readonly #timers: Timers;
  readonly #log: Logger;
  readonly #delay: number;
  #hash: string | null = null;
  #cancel: (() => void) | null = null;

  constructor(deps: KeyCopierDeps) {
    this.#d = deps;
    this.#timers = deps.timers ?? realTimers;
    this.#log = deps.log ?? NULL_LOGGER;
    this.#delay = deps.clearAfterMs ?? CLIPBOARD_CLEAR_MS;
  }

  /** Seconds the page is told (the real delay, rounded, at least 1). */
  get clearsInS(): number {
    return Math.max(1, Math.round(this.#delay / 1000));
  }

  async copy(): Promise<CopyKeyResult> {
    const shown = await this.#d.keys.show();
    if (!shown.ok) {
      this.#log.log('warn', 'app', 'copy API key failed', { code: shown.code });
      return shown;
    }
    try {
      await this.#d.clipboard.writeText(shown.material.key);
    } catch {
      this.#log.log('warn', 'app', 'the clipboard could not be written');
      return { ok: false, code: 'clipboard_failed' };
    }
    // A second copy restarts the countdown; the hash is all that is kept.
    this.#cancel?.();
    this.#hash = sha256(shown.material.key);
    this.#cancel = this.#timers.after(this.#delay, () => void this.clearIfHeld());
    this.#log.log('info', 'app', 'API key copied', { clearsInS: this.clearsInS });
    return { ok: true, clearsInS: this.clearsInS };
  }

  /** A copied key is waiting to be cleared. */
  get pending(): boolean {
    return this.#hash !== null;
  }

  /** Clears the clipboard when it still holds the copied key. True when it did. Also called when the app quits. */
  async clearIfHeld(): Promise<boolean> {
    const hash = this.#hash;
    this.#cancel?.();
    this.#cancel = null;
    this.#hash = null;
    if (hash === null) return false;
    try {
      if (sha256(await this.#d.clipboard.readText()) !== hash) return false;
      await this.#d.clipboard.clear();
      this.#log.log('info', 'app', 'the API key was cleared from the clipboard');
      return true;
    } catch {
      return false;
    }
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// The bridge actions and the tray's native messages
// ---------------------------------------------------------------------------------------------------------------------

/** Seen from here, the PIN service is `withGrant`: one grant, one request. */
export interface GrantRunner {
  withGrant<T>(action: (grant: string) => Promise<T>, purpose?: PinPurpose): Promise<WithGrantOutcome<T>>;
}

/**
 * The four calls behind `copyApiKey`, `connectionInfo`, `revealKey` and `rotateKey`. `info` and `reveal` each read
 * `GET /key` afresh, so the agent audits every Reveal; `rotate` asks for the PIN and sends the one request with its grant.
 */
export function createKeyActions(deps: { keys: KeyApi; copier: Pick<KeyCopier, 'copy'>; pin: GrantRunner }): KeyActions {
  return {
    copy: () => deps.copier.copy(),
    async info() {
      const shown = await deps.keys.show();
      return shown.ok ? buildConnectionInfo(shown.material) : shown;
    },
    async reveal() {
      const shown = await deps.keys.show();
      return shown.ok ? { ok: true, key: shown.material.key } : shown;
    },
    async rotate() {
      const outcome = await deps.pin.withGrant((grant) => deps.keys.rotate(grant), 'rotate-key');
      if (outcome.ok) return outcome.value;
      return { ok: false, code: outcome.code ?? 'LAI-SEC-002', cancelled: true };
    },
  };
}

const COPY_TEXT = {
  title: { en: 'API key', es: 'Clave de API' },
  copied: { en: 'API key copied. It will be cleared from the clipboard in {n} seconds.', es: 'Clave copiada. Se borrará del portapapeles en {n} segundos.' },
  failed: { en: 'The API key could not be copied.', es: 'No se pudo copiar la clave de API.' },
} as const satisfies Record<string, Bilingual>;

/** The native notification for a copy made from the tray (the top bar says the same in its own strip). */
export function copyNotification(result: CopyKeyResult, lang: Lang): { title: string; body: string } {
  const title = pick(COPY_TEXT.title, lang);
  if (result.ok) return { title, body: fill(pick(COPY_TEXT.copied, lang), { n: String(result.clearsInS) }) };
  return { title, body: result.code.startsWith('LAI-') ? `${pick(COPY_TEXT.failed, lang)} (${result.code})` : pick(COPY_TEXT.failed, lang) };
}
