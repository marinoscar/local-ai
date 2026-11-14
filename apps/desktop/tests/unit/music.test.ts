// Music on this computer (issue #515): the availability probe behind the top bar's Music button, the button's states,
// and the download handler that saves songs from the Music window.
import { describe, expect, it, vi } from 'vitest';
import { buildDesktopStatus } from '../../src/main/ipc';
import { MUSIC_STATUS_PATH, MusicProbe, SAVE_SONG_TITLE, decideDownload, installDownloadHandler, musicAvailabilityFrom, suggestedSongName } from '../../src/main/music';
import { registerView, viewOf, type View } from '../../src/main/navigation';
import { musicButtonFor } from '../../src/renderer/topbar/model';
import { STRINGS } from '../../src/renderer/topbar/strings';
import type { DesktopStatus } from '../../src/shared/types';

describe('musicAvailabilityFrom', () => {
  const status = { installed: true, loaded_model: null, engine_state: 'stopped', queue: { running: 0, queued: 0, max: 4 } };
  it.each([
    ['installed', 200, status, 'installed'],
    ['installed in an envelope', 200, { data: status }, 'installed'],
    ['not installed', 200, { ...status, installed: false }, 'not-installed'],
    ['an agent without music routes (404)', 404, null, 'not-installed'],
    ['404 with an error body', 404, { error: { code: 'not_found' } }, 'not-installed'],
    ['installed as a string is not true', 200, { installed: 'true' }, 'unknown'],
    ['no installed field', 200, { engine_state: 'ready' }, 'unknown'],
    ['not JSON', 200, null, 'unknown'],
    ['an array', 200, [true], 'unknown'],
    ['unauthorised', 401, null, 'unknown'],
    ['server error', 500, status, 'unknown'],
    ['agent busy', 503, status, 'unknown'],
  ] as const)('%s', (_name, code, body, expected) => {
    expect(musicAvailabilityFrom(code, body)).toBe(expected);
  });
});

describe('MusicProbe', () => {
  const answer = (status: number, body: unknown) => ({ status, json: async () => body });

  it('asks GET /api/v1/music/status and tells listeners only on a change', async () => {
    const request = vi.fn(async () => answer(200, { installed: true }));
    const probe = new MusicProbe({ request });
    const changed = vi.fn();
    probe.onChange(changed);
    expect(probe.value).toBe('unknown');
    await probe.refresh();
    expect(request).toHaveBeenCalledWith(MUSIC_STATUS_PATH, { timeoutMs: 3000 });
    expect(probe.value).toBe('installed');
    await probe.refresh();
    expect(changed).toHaveBeenCalledTimes(1);
  });

  it('404 is "not installed"; a thrown request (agent down) is "unknown"', async () => {
    let next: () => Promise<{ status: number; json(): Promise<unknown> }> = async () => answer(404, null);
    const probe = new MusicProbe({ request: () => next() });
    await probe.refresh();
    expect(probe.value).toBe('not-installed');
    next = async () => {
      throw new TypeError('fetch failed');
    };
    await probe.refresh();
    expect(probe.value).toBe('unknown');
  });

  it('a body that is not JSON is "unknown", not a crash', async () => {
    const probe = new MusicProbe({ request: async () => ({ status: 200, json: async () => Promise.reject(new SyntaxError('bad')) }) });
    await probe.refresh();
    expect(probe.value).toBe('unknown');
  });

  it('one request at a time: calls during a request share it', async () => {
    let release: (() => void) | undefined;
    const request = vi.fn(
      () =>
        new Promise<{ status: number; json(): Promise<unknown> }>((resolve) => {
          release = () => resolve(answer(200, { installed: false }));
        }),
    );
    const probe = new MusicProbe({ request });
    const a = probe.refresh();
    const b = probe.refresh();
    expect(request).toHaveBeenCalledTimes(1);
    release!();
    await Promise.all([a, b]);
    expect(probe.value).toBe('not-installed');
    // Once settled, the next call asks again.
    const c = probe.refresh();
    expect(request).toHaveBeenCalledTimes(2);
    release!();
    await c;
  });

  it('a listener that throws does not stop the others', async () => {
    const probe = new MusicProbe({ request: async () => answer(200, { installed: true }) });
    const second = vi.fn();
    probe.onChange(() => {
      throw new Error('boom');
    });
    probe.onChange(second);
    await probe.refresh();
    expect(second).toHaveBeenCalledTimes(1);
  });
});

describe('the status snapshot carries music only while the agent is up', () => {
  const base = { version: '1', phase: 'ready' as const, connection: 'connected' as const, agentStatus: null, cloud: { linked: false, tokenHeld: false, url: 'https://ai.marin.cr' }, tab: 'local' as const };
  it.each([
    ['up, installed', base, 'installed', 'installed'],
    ['up, not installed', base, 'not-installed', 'not-installed'],
    ['up, not read yet', base, undefined, 'unknown'],
    ['agent down keeps no stale answer', { ...base, connection: 'reconnecting' as const }, 'installed', 'unknown'],
    ['start-up error page', { ...base, phase: 'error' as const }, 'installed', 'unknown'],
    ['still starting', { ...base, phase: 'starting' as const }, 'installed', 'unknown'],
  ] as const)('%s', (_name, input, music, expected) => {
    expect(buildDesktopStatus({ ...input, ...(music !== undefined ? { music } : {}) }).music).toBe(expected);
  });
});

describe('the Music button of the top bar', () => {
  const status = (over: Partial<DesktopStatus> = {}): DesktopStatus => ({
    version: '0.1.0',
    agent: 'up',
    mode: 'offline',
    connectivity: 'none',
    alerts: 0,
    cloud: { linked: false, tokenHeld: false, url: 'https://ai.marin.cr' },
    connected: false,
    tab: 'local',
    ...over,
  });

  it('installed: enabled, no hint, works offline', () => {
    expect(musicButtonFor(status({ music: 'installed' }), 'en')).toEqual({ state: 'installed', enabled: true, hint: null });
  });

  it('not installed (or the route answered 404): disabled with the hint, in both languages', () => {
    expect(musicButtonFor(status({ music: 'not-installed' }), 'en')).toEqual({ state: 'not-installed', enabled: false, hint: 'Music is not installed on this computer' });
    expect(musicButtonFor(status({ music: 'not-installed' }), 'es')).toEqual({ state: 'not-installed', enabled: false, hint: 'La música no está instalada en esta computadora' });
  });

  it('unknown, absent, agent down or no status yet: disabled, with a "not available right now" hint', () => {
    for (const input of [status({ music: 'unknown' }), status(), status({ agent: 'down', music: 'installed' }), status({ agent: 'starting', music: 'installed' }), null]) {
      const button = musicButtonFor(input, 'en');
      expect(button.enabled).toBe(false);
      expect(button.state).toBe('unknown');
      expect(button.hint).toBe(STRINGS.musicUnknown.en);
    }
    expect(musicButtonFor(null, 'es').hint).toBe(STRINGS.musicUnknown.es);
  });

  it('the label is in both languages', () => {
    expect(STRINGS.music).toEqual({ en: 'Music', es: 'Música' });
  });
});

describe('decideDownload', () => {
  it.each([
    ['music', 'http://127.0.0.1:7070/api/v1/music/songs/sng_01HX/audio', 'ask'],
    ['music', 'http://127.0.0.1:7070/api/v1/music/songs/3f6c1d2e-1111-4a4a-9b9b-000000000001/audio', 'ask'],
    ['music', 'blob:http://127.0.0.1:7070/6f1c2a77-0000-4000-8000-000000000000', 'ask'],
    ['music', 'blob:https://example.com/6f1c', 'cancel'],
    ['music', 'http://127.0.0.1:7070/api/v1/music/songs/x/audio/extra', 'cancel'],
    ['music', 'http://127.0.0.1:7070/api/v1/music/songs/../../key/audio', 'cancel'],
    ['music', 'http://127.0.0.1:7070/api/v1/key', 'cancel'],
    ['music', 'http://127.0.0.1:7070/api/v1/music/songs', 'cancel'],
    ['music', 'http://localhost:7070/api/v1/music/songs/x/audio', 'cancel'],
    ['music', 'http://127.0.0.1:8082/v1/tasks/x', 'cancel'],
    ['music', 'https://example.com/song.wav', 'cancel'],
    ['music', 'data:audio/wav;base64,AAAA', 'cancel'],
    ['music', 'not a url', 'cancel'],
    ['chat', 'https://example.com/file.bin', 'default'],
    ['content', 'http://127.0.0.1:7070/api/v1/music/songs/x/audio', 'default'],
    ['shell', 'http://127.0.0.1:7070/api/v1/music/songs/x/audio', 'default'],
  ] as const)('%s %s -> %s', (view, url, expected) => {
    expect(decideDownload(view as View, url)).toBe(expected);
  });
});

describe('suggestedSongName', () => {
  it.each([
    ['song-01.wav', 'audio/wav', 'song-01.wav'],
    ['My song.MP3', 'audio/mpeg', 'My song.MP3'],
    ['../../etc/passwd', 'audio/wav', 'song.wav'],
    ['..\\..\\evil.wav', 'audio/wav', 'evil.wav'],
    ['.hidden.wav', 'audio/wav', 'hidden.wav'],
    ['audio', 'audio/mpeg', 'song.mp3'],
    ['', 'audio/flac', 'song.flac'],
    ['', 'audio/ogg; codecs=opus', 'song.ogg'],
    ['x.exe', 'application/octet-stream', 'song.wav'],
  ])('%s (%s) -> %s', (filename, mime, expected) => {
    expect(suggestedSongName(filename, mime)).toBe(expected);
  });
});

describe('installDownloadHandler', () => {
  function setup(lang: 'en' | 'es' = 'en') {
    let handler: ((event: { preventDefault(): void }, item: unknown, contents: unknown) => void) | undefined;
    const log = { log: vi.fn(), write: vi.fn() };
    installDownloadHandler({ on: ((name: string, h: typeof handler) => void (name === 'will-download' && (handler = h))) as never }, { viewOf, lang: () => lang, downloadsDir: () => '/home/person/Downloads', log });
    const item = (url: string, filename = 'song.wav') => ({
      getURL: () => url,
      getFilename: () => filename,
      getMimeType: () => 'audio/wav',
      getTotalBytes: () => 1234,
      setSaveDialogOptions: vi.fn(),
      setSavePath: vi.fn(),
    });
    const fire = (contents: unknown, it: ReturnType<typeof item>) => {
      const event = { preventDefault: vi.fn() };
      handler!(event, it, contents);
      return event;
    };
    return { item, fire, log };
  }

  it('a song from the Music window asks where to save it (no silent save path)', () => {
    const music = {};
    registerView(music as never, 'music');
    const { item, fire, log } = setup('es');
    const song = item('http://127.0.0.1:7070/api/v1/music/songs/sng_1/audio', 'Mi canción.wav');
    const event = fire(music, song);
    expect(event.preventDefault).not.toHaveBeenCalled();
    expect(song.setSavePath).not.toHaveBeenCalled();
    expect(song.setSaveDialogOptions).toHaveBeenCalledWith({ title: SAVE_SONG_TITLE.es, defaultPath: '/home/person/Downloads/Mi canción.wav', filters: [{ name: 'Audio', extensions: ['wav', 'mp3', 'flac', 'ogg'] }] });
    // sizes only: never the file name
    expect(JSON.stringify(log.log.mock.calls)).not.toContain('canción');
  });

  it('anything else from the Music window is cancelled', () => {
    const music = {};
    registerView(music as never, 'music');
    const { item, fire } = setup();
    const other = item('http://127.0.0.1:7070/api/v1/key', 'key.json');
    expect(fire(music, other).preventDefault).toHaveBeenCalledTimes(1);
    expect(other.setSaveDialogOptions).not.toHaveBeenCalled();
  });

  it('other windows keep the default behaviour', () => {
    const chat = {};
    registerView(chat as never, 'chat');
    const { item, fire } = setup();
    const file = item('http://127.0.0.1:7070/api/v1/music/songs/sng_1/audio');
    expect(fire(chat, file).preventDefault).not.toHaveBeenCalled();
    expect(file.setSaveDialogOptions).not.toHaveBeenCalled();
  });
});
