// src/main/updates.ts: the gate table, release discovery, the controller (timers, egress, status, LAI-DSK-016), the
// electron-updater adapter, the lazy load, and "zero requests when off" over a simulated 24 hours through a recording
// undici dispatcher (docs/specs/desktop.md sections 4.10 and 9).
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Dispatcher, fetch as undiciFetch } from 'undici';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { decideMainEgress, getEgressState, patchEgressState, resetEgressGuardForTests, runAsUpdater, setEgressState } from '../../src/main/egress-guard';
import { UPDATE_TEXT } from '../../src/main/i18n';
import type { LogEntry, Logger } from '../../src/main/log';
import { buildMenuTemplate } from '../../src/main/menu';
import {
  CHECK_INTERVAL_MS,
  DOCTOR_CODE,
  FIRST_CHECK_DELAY_MS,
  RELEASES_FEED_URL,
  RELEASE_FOLDER_URL,
  type AppUpdaterLike,
  type Timers,
  type UpdateGate,
  type UpdaterPort,
  UpdateController,
  compareSemver,
  createElectronUpdaterPort,
  describeUpdateStatus,
  newestDesktopRelease,
  parseSemver,
  tagsFromFeed,
  updateGate,
} from '../../src/main/updates';
import type { NodeMode } from '../../src/shared/types';

const here = dirname(fileURLToPath(import.meta.url));
const srcDir = join(here, '..', '..', 'src');

// ---------------------------------------------------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------------------------------------------------

/** Timers the test advances by hand; real microtasks and real promises keep working (no fake-timer surprises). */
class ManualTimers implements Timers {
  now = 0;
  #next = 1;
  #pending = new Map<number, { at: number; fn: () => void }>();
  setTimeout(fn: () => void, ms: number): unknown {
    const id = this.#next++;
    this.#pending.set(id, { at: this.now + ms, fn });
    return id;
  }
  clearTimeout(handle: unknown): void {
    this.#pending.delete(handle as number);
  }
  get pending(): number {
    return this.#pending.size;
  }
  /** Moves the clock forward, running due timers in order and letting their promises settle in between. */
  async advance(ms: number): Promise<void> {
    const end = this.now + ms;
    for (;;) {
      const due = [...this.#pending.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0];
      if (!due) break;
      this.#pending.delete(due[0]);
      this.now = due[1].at;
      due[1].fn();
      await flush();
    }
    this.now = end;
    await flush();
  }
}

const flush = async (): Promise<void> => {
  for (let i = 0; i < 20; i++) await new Promise<void>((resolve) => setImmediate(resolve));
};

const feedWith = (...tags: string[]): string =>
  `<?xml version="1.0"?><feed>${tags.map((t) => `<entry><link rel="alternate" type="text/html" href="https://github.com/marinoscar/local-ai/releases/tag/${t}"/></entry>`).join('')}</feed>`;

function recordingLogger(): { log: Logger; entries: LogEntry[] } {
  const entries: LogEntry[] = [];
  return {
    entries,
    log: {
      write: (entry) => void entries.push(entry),
      log: (level, component, msg, fields) => void entries.push({ ts: '', level, component, msg, ...fields }),
    },
  };
}

class FakePort implements UpdaterPort {
  folders: string[] = [];
  installOnQuit: boolean[] = [];
  cancels = 0;
  downloads = 0;
  version: string;
  available = true;
  hold: Promise<void> | null = null;
  fail: Error | null = null;
  constructor(version: string) {
    this.version = version;
  }
  setReleaseFolder(url: string): void {
    this.folders.push(url);
  }
  setInstallOnQuit(on: boolean): void {
    this.installOnQuit.push(on);
  }
  cancel(): void {
    this.cancels += 1;
  }
  async checkAndDownload(): Promise<{ available: boolean; version: string }> {
    this.downloads += 1;
    if (this.hold) await this.hold;
    if (this.fail) throw this.fail;
    return { available: this.available, version: this.version };
  }
}

interface Rig {
  controller: UpdateController;
  timers: ManualTimers;
  port: FakePort;
  fetched: string[];
  loads: () => number;
  egress: boolean[];
  statuses: string[];
  logs: LogEntry[];
  feed: { text: string; fail: Error | null };
}

function rig(options: { packaged?: boolean; current?: string; feed?: string; portVersion?: string } = {}): Rig {
  const timers = new ManualTimers();
  const port = new FakePort(options.portVersion ?? '0.2.0');
  const fetched: string[] = [];
  const egress: boolean[] = [];
  const statuses: string[] = [];
  const feed = { text: options.feed ?? feedWith('node-v0.3.0', 'desktop-v0.2.0', 'catalog-2026.11.01'), fail: null as Error | null };
  let loads = 0;
  const { log, entries } = recordingLogger();
  const controller = new UpdateController({
    packaged: options.packaged ?? true,
    currentVersion: options.current ?? '0.1.0',
    loadUpdater: async () => {
      loads += 1;
      return port;
    },
    fetchText: async (url) => {
      fetched.push(url);
      if (feed.fail) throw feed.fail;
      return feed.text;
    },
    setEgress: (allowed) => void egress.push(allowed),
    log,
    clock: () => timers.now,
    timers,
    onStatus: (status) => void statuses.push(status.state),
  });
  return { controller, timers, port, fetched, loads: () => loads, egress, statuses, logs: entries, feed };
}

const ON: UpdateGate = { setting: true, mode: 'online', connected: false };

// ---------------------------------------------------------------------------------------------------------------------
// The gate table
// ---------------------------------------------------------------------------------------------------------------------

describe('updateGate: setting x mode x connected', () => {
  const modes: Array<NodeMode | null> = ['online', 'offline', 'survival', null];
  const cases = modes.flatMap((mode) => [true, false].flatMap((setting) => [true, false].map((connected) => ({ setting, mode, connected }))));

  it.each(cases)('setting=$setting mode=$mode connected=$connected', ({ setting, mode, connected }) => {
    const decision = updateGate({ setting, mode, connected });
    // Only "on" and "online" opens the gate. Connected is a part of online and never opens it alone.
    expect(decision.allowed).toBe(setting && mode === 'online');
  });

  it('names the reason', () => {
    expect(updateGate({ setting: false, mode: 'online', connected: true }).reason).toBe('setting-off');
    expect(updateGate({ setting: true, mode: 'offline', connected: false }).reason).toBe('offline');
    expect(updateGate({ setting: true, mode: 'survival', connected: false }).reason).toBe('survival');
    expect(updateGate({ setting: true, mode: null, connected: false }).reason).toBe('mode-unknown');
    expect(updateGate({ setting: true, mode: 'online', connected: false }).reason).toBe('allowed');
  });
});

// ---------------------------------------------------------------------------------------------------------------------
// Finding the release
// ---------------------------------------------------------------------------------------------------------------------

describe('semver and the release feed', () => {
  it('orders versions by SemVer 2.0', () => {
    const order = ['0.1.0-rc.1', '0.1.0-rc.2', '0.1.0-rc.10', '0.1.0', '0.1.1', '0.2.0', '1.0.0-alpha', '1.0.0-alpha.1', '1.0.0-beta', '1.0.0'];
    for (let i = 0; i < order.length - 1; i++) {
      expect(compareSemver(parseSemver(order[i]!)!, parseSemver(order[i + 1]!)!), `${order[i]} < ${order[i + 1]}`).toBe(-1);
      expect(compareSemver(parseSemver(order[i + 1]!)!, parseSemver(order[i]!)!)).toBe(1);
    }
    expect(compareSemver(parseSemver('1.2.3')!, parseSemver('1.2.3')!)).toBe(0);
    expect(parseSemver('v1.2.3')).toBeNull();
    expect(parseSemver('1.2')).toBeNull();
  });

  it('reads the tags of an Atom feed and decodes them', () => {
    expect(tagsFromFeed(feedWith('desktop-v0.2.0', 'node-v1.0.0'))).toEqual(['desktop-v0.2.0', 'node-v1.0.0']);
    expect(tagsFromFeed('<feed><link href="https://github.com/o/r/releases/tag/desktop-v1.0.0%2Brc"/></feed>')).toEqual(['desktop-v1.0.0+rc']);
    expect(tagsFromFeed('<html>not a feed</html>')).toEqual([]);
  });

  it('takes the newest desktop tag that is newer than the running version, and ignores other products', () => {
    const tags = ['node-v9.0.0', 'catalog-2026.11.01', 'desktop-v0.1.5', 'desktop-v0.3.0', 'desktop-v0.2.0'];
    expect(newestDesktopRelease(tags, '0.1.0')).toEqual({ tag: 'desktop-v0.3.0', version: '0.3.0' });
    expect(newestDesktopRelease(tags, '0.3.0')).toBeNull();
    expect(newestDesktopRelease(tags, '0.4.0')).toBeNull();
    expect(newestDesktopRelease(['node-v1.0.0'], '0.1.0')).toBeNull();
    expect(newestDesktopRelease(['desktop-vbanana', 'desktop-v1.0'], '0.1.0')).toBeNull();
  });

  it('moves to a pre-release only from a pre-release', () => {
    expect(newestDesktopRelease(['desktop-v0.2.0-rc.1'], '0.1.0')).toBeNull();
    expect(newestDesktopRelease(['desktop-v0.2.0-rc.1', 'desktop-v0.1.1'], '0.1.0')).toEqual({ tag: 'desktop-v0.1.1', version: '0.1.1' });
    expect(newestDesktopRelease(['desktop-v0.1.0-rc.2'], '0.1.0-rc.1')).toEqual({ tag: 'desktop-v0.1.0-rc.2', version: '0.1.0-rc.2' });
    expect(newestDesktopRelease(['desktop-v0.1.0'], '0.1.0-rc.1')).toEqual({ tag: 'desktop-v0.1.0', version: '0.1.0' });
    expect(newestDesktopRelease(['desktop-v0.1.0-rc.1'], '0.1.0')).toBeNull();
  });
});

// ---------------------------------------------------------------------------------------------------------------------
// The controller
// ---------------------------------------------------------------------------------------------------------------------

describe('UpdateController: off', () => {
  it('makes no request and loads nothing over 24 hours, whatever the mode', async () => {
    const r = rig();
    for (const mode of ['online', 'offline', 'survival', null] as const) r.controller.apply({ setting: false, mode, connected: false });
    await r.timers.advance(24 * 60 * 60 * 1000);
    expect(r.fetched).toEqual([]);
    expect(r.loads()).toBe(0);
    expect(r.egress).toEqual([]);
    expect(r.timers.pending).toBe(0);
    expect(r.controller.status).toEqual({ state: 'off' });
  });

  it.each(['offline', 'survival'] as const)('is paused in %s even when ticked: nothing is requested, loaded or opened', async (mode) => {
    const r = rig();
    r.controller.apply({ setting: true, mode, connected: false });
    await r.timers.advance(24 * 60 * 60 * 1000);
    expect(r.fetched).toEqual([]);
    expect(r.loads()).toBe(0);
    expect(r.egress).toEqual([]);
    expect(r.controller.status).toEqual({ state: 'paused', pausedBecause: mode });
  });

  it('waits while the mode is not known', async () => {
    const r = rig();
    r.controller.apply({ setting: true, mode: null, connected: false });
    await r.timers.advance(CHECK_INTERVAL_MS * 2);
    expect(r.fetched).toEqual([]);
    expect(r.controller.status).toEqual({ state: 'paused', pausedBecause: 'mode-unknown' });
  });

  it('an unpackaged run never checks, even ticked and online', async () => {
    const r = rig({ packaged: false });
    r.controller.apply(ON);
    await r.timers.advance(24 * 60 * 60 * 1000);
    expect(r.fetched).toEqual([]);
    expect(r.egress).toEqual([]);
    expect(r.controller.status.state).toBe('unavailable');
  });
});

describe('UpdateController: on', () => {
  it('ticking the box checks at once, then every 6 hours', async () => {
    const r = rig({ feed: feedWith('node-v1.0.0') });
    r.controller.apply({ ...ON, setting: false }); // the app started with the box off
    r.controller.apply(ON); // the person ticks it
    await r.timers.advance(0);
    expect(r.fetched).toEqual([RELEASES_FEED_URL]);
    await r.timers.advance(CHECK_INTERVAL_MS - 1);
    expect(r.fetched).toHaveLength(1);
    await r.timers.advance(1);
    expect(r.fetched).toHaveLength(2);
    await r.timers.advance(24 * 60 * 60 * 1000 - CHECK_INTERVAL_MS);
    expect(r.fetched).toHaveLength(5); // t = 0, 6, 12, 18, 24 h
    expect(r.loads()).toBe(0); // nothing newer in the feed: electron-updater was never even loaded
    expect(r.egress).toEqual([true]);
  });

  it('a start-up with the box already ticked waits 30 seconds for the first check', async () => {
    const r = rig();
    r.controller.apply(ON);
    await r.timers.advance(FIRST_CHECK_DELAY_MS - 1);
    expect(r.fetched).toEqual([]);
    await r.timers.advance(1);
    expect(r.fetched).toHaveLength(1);
  });

  it('opens the egress allowance before the first request and downloads from the release folder of the newest desktop tag', async () => {
    const r = rig();
    r.controller.apply(ON);
    expect(r.egress).toEqual([true]);
    await r.timers.advance(FIRST_CHECK_DELAY_MS);
    expect(r.loads()).toBe(1);
    expect(r.port.folders).toEqual([`${RELEASE_FOLDER_URL}/desktop-v0.2.0`]);
    expect(r.port.installOnQuit.at(-1)).toBe(true);
    expect(r.controller.status).toMatchObject({ state: 'ready', version: '0.2.0' });
    expect(r.statuses).toEqual(expect.arrayContaining(['idle', 'checking', 'downloading', 'ready']));
  });

  it('does not download again once the newest version is ready', async () => {
    const r = rig();
    r.controller.apply(ON);
    await r.timers.advance(FIRST_CHECK_DELAY_MS + CHECK_INTERVAL_MS * 3);
    expect(r.port.downloads).toBe(1);
    expect(r.fetched.length).toBeGreaterThanOrEqual(4);
    expect(r.controller.status).toMatchObject({ state: 'ready', version: '0.2.0' });
  });

  it('LAI-DSK-016 when the feed cannot be read, then recovers at the next check', async () => {
    const r = rig();
    r.feed.fail = new Error('HTTP 503 from the release feed');
    r.controller.apply(ON);
    await r.timers.advance(FIRST_CHECK_DELAY_MS);
    expect(r.controller.status).toMatchObject({ state: 'error', code: DOCTOR_CODE });
    expect(r.logs.find((e) => e.code === DOCTOR_CODE)).toMatchObject({ level: 'warn', component: 'updates' });
    r.feed.fail = null;
    await r.timers.advance(CHECK_INTERVAL_MS);
    expect(r.controller.status).toMatchObject({ state: 'ready', version: '0.2.0' });
  });

  it('LAI-DSK-016 when the download fails (a bad SHA-512, a dropped connection)', async () => {
    const r = rig();
    r.port.fail = new Error('sha512 checksum mismatch');
    r.controller.apply(ON);
    await r.timers.advance(FIRST_CHECK_DELAY_MS);
    expect(r.controller.status).toMatchObject({ state: 'error', code: DOCTOR_CODE });
  });

  it('LAI-DSK-016 when electron-updater cannot be loaded, and the load is tried again next time', async () => {
    const timers = new ManualTimers();
    let attempts = 0;
    const port = new FakePort('0.2.0');
    const controller = new UpdateController({
      packaged: true,
      currentVersion: '0.1.0',
      loadUpdater: async () => {
        attempts += 1;
        if (attempts === 1) throw new Error('cannot load');
        return port;
      },
      fetchText: async () => feedWith('desktop-v0.2.0'),
      setEgress: () => undefined,
      timers,
    });
    controller.apply(ON);
    await timers.advance(FIRST_CHECK_DELAY_MS);
    expect(controller.status.state).toBe('error');
    await timers.advance(CHECK_INTERVAL_MS);
    expect(attempts).toBe(2);
    expect(controller.status).toMatchObject({ state: 'ready' });
  });
});

describe('UpdateController: switching off', () => {
  it('unticking stops the timer, closes the egress allowance and stops installing on quit', async () => {
    const r = rig();
    r.controller.apply(ON);
    await r.timers.advance(FIRST_CHECK_DELAY_MS);
    const requests = r.fetched.length;
    r.controller.apply({ ...ON, setting: false });
    await flush();
    expect(r.egress).toEqual([true, false]);
    expect(r.timers.pending).toBe(0);
    expect(r.port.cancels).toBe(1);
    expect(r.port.installOnQuit.at(-1)).toBe(false);
    expect(r.controller.status).toEqual({ state: 'off' });
    await r.timers.advance(24 * 60 * 60 * 1000);
    expect(r.fetched).toHaveLength(requests);
  });

  it('a switch to Offline or Survival does the same, and Online again checks after the short delay', async () => {
    const r = rig({ feed: feedWith('node-v1.0.0') });
    r.controller.apply(ON);
    await r.timers.advance(FIRST_CHECK_DELAY_MS);
    expect(r.fetched).toHaveLength(1);
    for (const mode of ['offline', 'survival'] as const) {
      r.controller.apply({ setting: true, mode, connected: false });
      expect(r.egress.at(-1)).toBe(false);
      await r.timers.advance(CHECK_INTERVAL_MS * 2);
      expect(r.fetched).toHaveLength(1);
      expect(r.controller.status).toEqual({ state: 'paused', pausedBecause: mode });
    }
    r.controller.apply(ON);
    expect(r.egress.at(-1)).toBe(true);
    await r.timers.advance(FIRST_CHECK_DELAY_MS);
    expect(r.fetched).toHaveLength(2);
  });

  it('a download that finishes after the switch-off is ignored', async () => {
    const r = rig();
    let release!: () => void;
    r.port.hold = new Promise<void>((resolve) => (release = resolve));
    r.controller.apply(ON);
    await r.timers.advance(FIRST_CHECK_DELAY_MS);
    expect(r.controller.status.state).toBe('downloading');
    r.controller.apply({ setting: true, mode: 'offline', connected: false });
    release();
    await flush();
    expect(r.controller.status).toEqual({ state: 'paused', pausedBecause: 'offline' });
  });

  it('dispose stops everything and later calls do nothing', async () => {
    const r = rig();
    r.controller.apply(ON);
    r.controller.dispose();
    r.controller.apply(ON);
    await r.timers.advance(CHECK_INTERVAL_MS);
    expect(r.fetched).toEqual([]);
    expect(r.egress).toEqual([true, false]);
  });
});

describe('UpdateController and the egress guard', () => {
  afterEach(() => resetEgressGuardForTests());

  it('the GitHub hosts are open to the updater only while updates are on, and never to the rest of the app', () => {
    resetEgressGuardForTests();
    setEgressState({ connected: true, updates: false }); // connected mode (DSK-10) is on and must stay on
    const timers = new ManualTimers();
    const controller = new UpdateController({
      packaged: true,
      currentVersion: '0.1.0',
      loadUpdater: async () => new FakePort('0.2.0'),
      fetchText: async () => feedWith('node-v1.0.0'),
      // What index.ts does: change `updates` and leave `connected` (DSK-10) alone.
      setEgress: (allowed) => patchEgressState({ updates: allowed }),
      runAsUpdater,
      timers,
    });
    const github = { kind: 'tcp', host: 'github.com', port: 443 } as const;
    const asUpdater = () => decideMainEgress(github, { state: getEgressState(), caller: 'updater' }).allow;
    const asApp = () => decideMainEgress(github, { state: getEgressState(), caller: 'app' }).allow;
    expect([asUpdater(), asApp()]).toEqual([false, false]);
    controller.apply(ON);
    expect([asUpdater(), asApp()]).toEqual([true, false]);
    controller.apply({ setting: true, mode: 'survival', connected: false });
    expect([asUpdater(), asApp()]).toEqual([false, false]);
    controller.apply(ON);
    controller.apply({ ...ON, setting: false });
    expect([asUpdater(), asApp()]).toEqual([false, false]);
    expect(getEgressState()).toEqual({ connected: true, updates: false });
    controller.dispose();
  });
});

// ---------------------------------------------------------------------------------------------------------------------
// "Zero requests when off", through a recording dispatcher
// ---------------------------------------------------------------------------------------------------------------------

/** Answers every request itself with an empty release feed and records it: nothing leaves the process. */
class RecordingDispatcher extends Dispatcher {
  readonly requests: string[] = [];
  override dispatch(options: Dispatcher.DispatchOptions, handler: Dispatcher.DispatchHandler): boolean {
    this.requests.push(`${String(options.origin)}${options.path}`);
    const body = Buffer.from(feedWith('node-v1.0.0'));
    handler.onConnect?.(() => undefined);
    handler.onHeaders?.(200, [Buffer.from('content-type'), Buffer.from('application/atom+xml')], () => undefined, 'OK');
    handler.onData?.(body);
    handler.onComplete?.([]);
    return true;
  }
  override close(): Promise<void> {
    return Promise.resolve();
  }
  override destroy(): Promise<void> {
    return Promise.resolve();
  }
}

describe('zero requests when off, over a simulated 24 hours', () => {
  function controllerOver(dispatcher: RecordingDispatcher, timers: ManualTimers): UpdateController {
    return new UpdateController({
      packaged: true,
      currentVersion: '0.1.0',
      loadUpdater: async () => new FakePort('0.2.0'),
      fetchText: async (url) => {
        const response = await undiciFetch(url, { dispatcher });
        return response.text();
      },
      setEgress: () => undefined,
      timers,
    });
  }

  it('the recorder really records (so a zero means something)', async () => {
    const dispatcher = new RecordingDispatcher();
    const timers = new ManualTimers();
    const controller = controllerOver(dispatcher, timers);
    controller.apply({ ...ON, setting: false });
    controller.apply(ON);
    await timers.advance(24 * 60 * 60 * 1000);
    expect(dispatcher.requests).toHaveLength(5);
    expect(dispatcher.requests.every((r) => r === RELEASES_FEED_URL)).toBe(true);
  });

  it('off: the dispatcher sees nothing in 24 hours, in any mode', async () => {
    const dispatcher = new RecordingDispatcher();
    const timers = new ManualTimers();
    const controller = controllerOver(dispatcher, timers);
    for (const mode of ['online', 'offline', 'survival', null] as const) controller.apply({ setting: false, mode, connected: mode === 'online' });
    await timers.advance(24 * 60 * 60 * 1000);
    expect(dispatcher.requests).toEqual([]);
  });

  it.each(['offline', 'survival'] as const)('on in %s: the dispatcher sees nothing in 24 hours', async (mode) => {
    const dispatcher = new RecordingDispatcher();
    const timers = new ManualTimers();
    const controller = controllerOver(dispatcher, timers);
    controller.apply({ setting: true, mode, connected: false });
    await timers.advance(24 * 60 * 60 * 1000);
    expect(dispatcher.requests).toEqual([]);
  });

  it('on and online, then Survival half way: requests stop at the switch', async () => {
    const dispatcher = new RecordingDispatcher();
    const timers = new ManualTimers();
    const controller = controllerOver(dispatcher, timers);
    controller.apply(ON);
    await timers.advance(13 * 60 * 60 * 1000);
    const before = dispatcher.requests.length;
    expect(before).toBeGreaterThan(0);
    controller.apply({ setting: true, mode: 'survival', connected: false });
    await timers.advance(24 * 60 * 60 * 1000);
    expect(dispatcher.requests).toHaveLength(before);
  });
});

// ---------------------------------------------------------------------------------------------------------------------
// electron-updater is loaded lazily
// ---------------------------------------------------------------------------------------------------------------------

describe('electron-updater is not loaded until it is needed', () => {
  const sources = (dir: string): string[] =>
    readdirSync(dir).flatMap((name) => {
      const full = join(dir, name);
      return statSync(full).isDirectory() ? sources(full) : /\.(ts|tsx|mts)$/.test(name) ? [full] : [];
    });

  it('no source file imports it except as a type, and updates.ts alone loads it, with a dynamic import', () => {
    const offenders: string[] = [];
    for (const file of sources(srcDir)) {
      const text = readFileSync(file, 'utf8');
      const staticImport = /^\s*import\s+(?!type\b)[^;]*from\s+['"]electron-updater['"]/m.test(text);
      const requireCall = /require\(\s*['"]electron-updater['"]\s*\)/.test(text);
      const dynamic = /import\(\s*['"]electron-updater['"]\s*\)/.test(text);
      if (staticImport || requireCall) offenders.push(`${file}: static load`);
      if (dynamic && !file.endsWith(join('main', 'updates.ts'))) offenders.push(`${file}: dynamic load outside updates.ts`);
    }
    expect(offenders).toEqual([]);
    expect(readFileSync(join(srcDir, 'main', 'updates.ts'), 'utf8')).toMatch(/await import\('electron-updater'\)/);
  });

  it('is not in require.cache after start-up with updates off (a day of timers, every mode)', async () => {
    const cacheKeys = (): string[] => Object.keys(createRequire(import.meta.url).cache).filter((key) => key.includes('electron-updater'));
    expect(cacheKeys()).toEqual([]);
    const r = rig();
    for (const mode of ['online', 'offline', 'survival', null] as const) r.controller.apply({ setting: false, mode, connected: false });
    await r.timers.advance(24 * 60 * 60 * 1000);
    r.controller.dispose();
    expect(r.loads()).toBe(0);
    expect(cacheKeys()).toEqual([]);
  });
});

// ---------------------------------------------------------------------------------------------------------------------
// The adapter for electron-updater
// ---------------------------------------------------------------------------------------------------------------------

function fakeAutoUpdater(overrides: Record<string, unknown> = {}): AppUpdaterLike & { feeds: unknown[]; checks: number } {
  const state = {
    feeds: [] as unknown[],
    checks: 0,
    autoDownload: false,
    autoInstallOnAppQuit: false,
    autoRunAppAfterInstall: false,
    allowDowngrade: true,
    allowPrerelease: true,
    requestHeaders: null as Record<string, string> | null,
    logger: null as unknown,
    httpExecutor: { cachedSession: null as unknown },
    setFeedURL(options: unknown) {
      state.feeds.push(options);
    },
    async checkForUpdates() {
      state.checks += 1;
      return null as unknown;
    },
    ...overrides,
  };
  return state as unknown as AppUpdaterLike & { feeds: unknown[]; checks: number };
}

describe('createElectronUpdaterPort', () => {
  it('puts the library on the guarded session and sets the safe options', () => {
    const session = { name: 'updater session' };
    const updater = fakeAutoUpdater();
    createElectronUpdaterPort(updater, { session });
    expect(updater.httpExecutor?.cachedSession).toBe(session);
    expect(updater).toMatchObject({ autoDownload: true, autoInstallOnAppQuit: true, allowDowngrade: false, allowPrerelease: false });
    expect(updater.requestHeaders).toEqual({ 'Cache-Control': 'no-cache' });
  });

  it('refuses a library that has no HTTP executor to guard (it would use its own, unguarded session)', () => {
    expect(() => createElectronUpdaterPort(fakeAutoUpdater({ httpExecutor: undefined }), { session: {} })).toThrow(/HTTP executor/);
  });

  it('points the generic provider at a release folder', () => {
    const updater = fakeAutoUpdater();
    createElectronUpdaterPort(updater, { session: {} }).setReleaseFolder(`${RELEASE_FOLDER_URL}/desktop-v0.2.0`);
    expect(updater.feeds).toEqual([{ provider: 'generic', url: 'https://github.com/marinoscar/local-ai/releases/download/desktop-v0.2.0' }]);
  });

  it('checkAndDownload: inactive, not newer, and downloaded', async () => {
    const cancel = vi.fn();
    const results: unknown[] = [
      null,
      { isUpdateAvailable: false, updateInfo: { version: '0.1.0' } },
      { isUpdateAvailable: true, updateInfo: { version: '0.2.0' }, downloadPromise: Promise.resolve(['x.deb']), cancellationToken: { cancel } },
    ];
    const updater = fakeAutoUpdater({ checkForUpdates: async () => results.shift() });
    const port = createElectronUpdaterPort(updater, { session: {} });
    expect(await port.checkAndDownload()).toEqual({ available: false, version: '' });
    expect(await port.checkAndDownload()).toEqual({ available: false, version: '0.1.0' });
    expect(await port.checkAndDownload()).toEqual({ available: true, version: '0.2.0' });
  });

  it('cancel reaches the token of a download in progress', async () => {
    const cancel = vi.fn();
    let finish!: () => void;
    const downloadPromise = new Promise<string[]>((resolve) => (finish = () => resolve([])));
    const updater = fakeAutoUpdater({ checkForUpdates: async () => ({ isUpdateAvailable: true, updateInfo: { version: '0.2.0' }, downloadPromise, cancellationToken: { cancel } }) });
    const port = createElectronUpdaterPort(updater, { session: {} });
    const pending = port.checkAndDownload();
    await flush();
    port.cancel();
    expect(cancel).toHaveBeenCalledTimes(1);
    finish();
    await pending;
    port.cancel();
    expect(cancel).toHaveBeenCalledTimes(1); // nothing left to cancel
  });

  it('the library logs through our logger (scrubbed there), warnings and errors as warnings', () => {
    const { log, entries } = recordingLogger();
    const updater = fakeAutoUpdater();
    createElectronUpdaterPort(updater, { session: {}, log });
    const logger = updater.logger as { info(m: string): void; warn(m: string): void; error(m: string): void };
    logger.info('Checking for update');
    logger.error('Error: boom');
    expect(entries.map((e) => [e.level, e.component, e.msg])).toEqual([
      ['info', 'updates', 'Checking for update'],
      ['warn', 'updates', 'Error: boom'],
    ]);
  });
});

// ---------------------------------------------------------------------------------------------------------------------
// The menu status
// ---------------------------------------------------------------------------------------------------------------------

describe('menu status lines', () => {
  it('has a line for every state, in both languages, and LAI-DSK-016 carries the fix', () => {
    expect(describeUpdateStatus({ state: 'off' }, 'en')).toEqual([]);
    expect(describeUpdateStatus({ state: 'idle' }, 'en')).toEqual([]);
    expect(describeUpdateStatus({ state: 'idle', checkedAt: 1 }, 'es')).toEqual([UPDATE_TEXT.upToDate.es]);
    expect(describeUpdateStatus({ state: 'checking' }, 'en')).toEqual([UPDATE_TEXT.checking.en]);
    expect(describeUpdateStatus({ state: 'downloading', version: '0.2.0' }, 'en')).toEqual(['Downloading version 0.2.0...']);
    expect(describeUpdateStatus({ state: 'ready', version: '0.2.0' }, 'es')[0]).toContain('0.2.0');
    expect(describeUpdateStatus({ state: 'paused', pausedBecause: 'offline' }, 'en')).toEqual([UPDATE_TEXT.pausedOffline.en]);
    expect(describeUpdateStatus({ state: 'paused', pausedBecause: 'survival' }, 'es')).toEqual([UPDATE_TEXT.pausedSurvival.es]);
    expect(describeUpdateStatus({ state: 'paused', pausedBecause: 'mode-unknown' }, 'en')).toEqual([UPDATE_TEXT.pausedUnknown.en]);
    expect(describeUpdateStatus({ state: 'unavailable' }, 'en')).toEqual([UPDATE_TEXT.unavailable.en]);
    expect(describeUpdateStatus({ state: 'error', code: DOCTOR_CODE }, 'en')).toEqual([
      "LAI-DSK-016 · Couldn't check or download an update",
      'Try later, or install the update from the rebuild kit.',
    ]);
    expect(describeUpdateStatus({ state: 'error', code: DOCTOR_CODE }, 'es')[1]).toContain('kit de reconstrucción');
  });

  it('the native menu shows them greyed out under the ticked box, and not when the box is off', () => {
    const actions = { connectionInfo() {}, connect() {}, disconnect() {}, setAutostart() {}, setUpdates() {}, about() {}, quit() {} };
    const base = { lang: 'en' as const, linked: false, autostart: true, checkForUpdates: true, updateLines: ['LAI-DSK-016 · x', 'fix'] };
    const labels = buildMenuTemplate(base, actions).map((i) => `${i.label ?? '---'}${i.enabled === false ? ' (disabled)' : ''}`);
    expect(labels).toEqual(['Connection info', 'Connect to the lab', '---', 'Start at login', 'Check for updates', 'LAI-DSK-016 · x (disabled)', 'fix (disabled)', '---', 'About', 'Quit']);
    expect(buildMenuTemplate({ ...base, checkForUpdates: false }, actions).map((i) => i.label ?? '---')).not.toContain('fix');
  });
});
