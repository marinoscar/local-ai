// Music on this computer (issue #515, epic E11): whether the top bar's Music button works, and how a song is saved.
//
//   musicAvailabilityFrom(status, body)  pure: the answer of `GET /api/v1/music/status` -> installed / not-installed / unknown.
//   MusicProbe                           asks the agent again when the status is re-read; one request at a time.
//   decideDownload(view, url)            pure: which downloads the Music window may start (a song of the agent, nothing else).
//   installDownloadHandler(session)      applies it: a song asks where to save it (a native dialog); anything else is cancelled.
//
// The desktop decides nothing about music itself (ADR-0004): the agent's `/music/` page does the work; this module only
// reads one flag and lets a file reach the disk where the person chooses.
import { basename } from 'node:path';
import type { Session, WebContents } from 'electron';
import { AGENT_HOST, AGENT_PORT } from './egress-guard';
import { NULL_LOGGER, type Logger } from './log';
import type { View } from './navigation';
import type { Lang, MusicAvailability } from '../shared/types';

export const MODULE = 'music' as const;

export const MUSIC_STATUS_PATH = '/api/v1/music/status';
const AGENT_ORIGIN = `http://${AGENT_HOST}:${AGENT_PORT}`;

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/**
 * The agent's answer -> what the button shows. 404 is an agent without the music routes (a node from before E11): "not
 * installed". Any other failure (401, 5xx, a body that is not the documented shape) is `unknown`, never a guess.
 */
export function musicAvailabilityFrom(status: number, body: unknown): MusicAvailability {
  if (status === 404) return 'not-installed';
  if (status < 200 || status >= 300 || !isObject(body)) return 'unknown';
  // The agent API answers the object itself; an `{ data }` envelope is accepted too.
  const object = isObject(body['data']) ? body['data'] : body;
  const installed = object['installed'];
  if (installed === true) return 'installed';
  if (installed === false) return 'not-installed';
  return 'unknown';
}

export interface MusicProbeDeps {
  /** `AgentClient.request` (bearer token, `/api/v1/...` only). */
  request(path: string, options?: { timeoutMs?: number }): Promise<{ status: number; json(): Promise<unknown> }>;
  log?: Logger;
}

/** Holds the last answer; `refresh()` asks again (a second call while one is running waits for that one). */
export class MusicProbe {
  readonly #d: MusicProbeDeps;
  readonly #log: Logger;
  readonly #listeners = new Set<() => void>();
  #value: MusicAvailability = 'unknown';
  #running: Promise<void> | null = null;

  constructor(deps: MusicProbeDeps) {
    this.#d = deps;
    this.#log = deps.log ?? NULL_LOGGER;
  }

  get value(): MusicAvailability {
    return this.#value;
  }

  onChange(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  refresh(): Promise<void> {
    if (this.#running) return this.#running;
    this.#running = this.#read().finally(() => {
      this.#running = null;
    });
    return this.#running;
  }

  async #read(): Promise<void> {
    let next: MusicAvailability;
    try {
      const response = await this.#d.request(MUSIC_STATUS_PATH, { timeoutMs: 3000 });
      let body: unknown = null;
      if (response.status >= 200 && response.status < 300) body = await response.json().catch(() => null);
      next = musicAvailabilityFrom(response.status, body);
    } catch {
      // The agent is not reachable (or the token is unreadable): the start-up sequence and the pill say so.
      next = 'unknown';
    }
    if (next === this.#value) return;
    this.#log.log('info', 'agent', 'music availability changed', { music: next });
    this.#value = next;
    for (const listener of this.#listeners) {
      try {
        listener();
      } catch {
        // one listener must not stop the others
      }
    }
  }
}

// ---------------------------------------------------------------------------------------------------------------------
// Downloads
// ---------------------------------------------------------------------------------------------------------------------

/** A song's audio on the agent (agent API: `GET /api/v1/music/songs/{id}/audio`). */
const SONG_AUDIO = /^\/api\/v1\/music\/songs\/[A-Za-z0-9_-]{1,128}\/audio$/;

export type DownloadDecision = 'ask' | 'cancel';

/**
 * Which downloads may start. Only the Music window downloads, and only a song: the agent's song audio route, or a `blob:`
 * the Music page made from it (same agent origin). Every other view keeps Electron's behaviour except that this handler
 * also cancels a download from the Music window that is not a song.
 */
export function decideDownload(view: View, raw: string): DownloadDecision | 'default' {
  if (view !== 'music') return 'default';
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return 'cancel';
  }
  if (url.protocol === 'blob:') {
    // `blob:http://127.0.0.1:7070/<uuid>`: the inner URL's origin is the page that made it.
    return url.origin === AGENT_ORIGIN ? 'ask' : 'cancel';
  }
  if (url.origin !== AGENT_ORIGIN || url.username !== '' || url.password !== '') return 'cancel';
  return SONG_AUDIO.test(url.pathname) ? 'ask' : 'cancel';
}

const AUDIO_EXTENSIONS = ['wav', 'mp3', 'flac', 'ogg'] as const;

/**
 * The name the save dialog proposes: the server's file name when it is a plain audio file name, else `song.<ext>`
 * from the MIME type. Never a path: separators, dots at the start and control characters are dropped.
 */
export function suggestedSongName(filename: string, mime: string): string {
  const plain = basename(filename.replace(/\\/g, '/'))
    .replace(/[\u0000-\u001f\u007f/]/g, '')
    .replace(/^\.+/, '')
    .slice(0, 120);
  const extension = plain.includes('.') ? plain.slice(plain.lastIndexOf('.') + 1).toLowerCase() : '';
  if (plain !== '' && (AUDIO_EXTENSIONS as readonly string[]).includes(extension)) return plain;
  const fromMime: Record<string, string> = { 'audio/wav': 'wav', 'audio/x-wav': 'wav', 'audio/wave': 'wav', 'audio/mpeg': 'mp3', 'audio/flac': 'flac', 'audio/ogg': 'ogg' };
  return `song.${fromMime[mime.toLowerCase().split(';')[0]!.trim()] ?? 'wav'}`;
}

export const SAVE_SONG_TITLE = { en: 'Save the song', es: 'Guardar la canción' } as const;
const AUDIO_FILTER = { en: 'Audio', es: 'Audio' } as const;

export interface DownloadHandlerOptions {
  viewOf(contents: WebContents): View;
  lang(): Lang;
  /** `app.getPath('downloads')`. */
  downloadsDir(): string;
  log?: Logger;
}

/**
 * `will-download` on the agent session. A song: Electron shows the native "Save as" dialog (no `setSavePath`), starting in
 * the Downloads folder with a safe name. Not a song from the Music window: cancelled. Other views: untouched.
 */
export function installDownloadHandler(ses: Pick<Session, 'on'>, options: DownloadHandlerOptions): void {
  const log = options.log ?? NULL_LOGGER;
  ses.on('will-download', (event, item, contents) => {
    const decision = decideDownload(contents ? options.viewOf(contents) : 'shell', item.getURL());
    if (decision === 'default') return;
    if (decision === 'cancel') {
      event.preventDefault();
      log.log('warn', 'agent', 'a download from the Music window was refused: it is not a song');
      return;
    }
    const lang = options.lang();
    const name = suggestedSongName(item.getFilename(), item.getMimeType());
    item.setSaveDialogOptions({
      title: SAVE_SONG_TITLE[lang],
      defaultPath: `${options.downloadsDir()}/${name}`,
      filters: [{ name: AUDIO_FILTER[lang], extensions: [...AUDIO_EXTENSIONS] }],
    });
    // Sizes only, never names or content (contract §3: logs carry sizes and ids).
    log.log('info', 'agent', 'saving a song', { bytes: item.getTotalBytes() });
  });
}
