// The survival UX state machine against fakes (docs/specs/desktop.md 4.7): no Electron, no real window.
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AgentEvent, AgentStatus, FeedMessage } from '../../src/main/agent-client';
import {
  DEGRADED_POLL_MS,
  EVENT_PRECEDENCE_MS,
  PORTAL_LANG_KEY,
  STRIP_HEIGHT,
  SurvivalController,
  type SurvivalDeps,
  type SurvivalWindows,
  bringForward,
  enter,
  exit,
  install,
  setPortalLanguage,
} from '../../src/main/survival';
import { TOP_BAR_HEIGHT, type LocalContent } from '../../src/main/windows';
import type { Lang } from '../../src/main/i18n';
import type { Tab } from '../../src/shared/types';

const here = dirname(fileURLToPath(import.meta.url));

class FakeWindow {
  fullScreen = false;
  destroyed = false;
  calls: string[] = [];
  listeners = new Map<string, Array<() => void>>();
  /** Spies: they must never be touched (rule 3 of desktop.md 4.7). */
  setKiosk = vi.fn();
  setSimpleFullScreen = vi.fn();
  isFullScreen = () => this.fullScreen;
  isDestroyed = () => this.destroyed;
  setFullScreen = (flag: boolean) => {
    this.calls.push(`fullscreen:${flag}`);
    this.fullScreen = flag;
    for (const l of this.listeners.get(flag ? 'enter-full-screen' : 'leave-full-screen') ?? []) l();
  };
  onEvent = (event: string, listener: () => void) => {
    this.listeners.set(event, [...(this.listeners.get(event) ?? []), listener]);
  };
}

class FakeContents {
  url = '';
  destroyed = false;
  stored = new Map<string, string>();
  reloads = 0;
  scripts: string[] = [];
  getURL = () => this.url;
  isDestroyed = () => this.destroyed;
  reloadIgnoringCache = () => void (this.reloads += 1);
  // Runs the one script survival.ts sends, against a map standing in for localStorage.
  executeJavaScript = async (code: string): Promise<unknown> => {
    this.scripts.push(code);
    const key = /const k = "([^"]+)"/.exec(code)?.[1] ?? '';
    const value = /const v = "([^"]+)"/.exec(code)?.[1] ?? '';
    if (this.stored.get(key) === value) return false;
    this.stored.set(key, value);
    return true;
  };
}

class FakeWindows implements SurvivalWindows {
  win = new FakeWindow();
  contents = new FakeContents();
  log: string[] = [];
  visible = false;
  tab: Tab = 'local';
  local: LocalContent = 'none';
  path: string | null = null;
  barHeight = TOP_BAR_HEIGHT;
  showMain = () => {
    this.log.push('showMain');
    this.visible = true;
  };
  mainWindow = () => this.win;
  setTopBarHeight = (n: number) => {
    this.barHeight = n;
    this.log.push(`bar:${n}`);
  };
  showTab = (tab: Tab) => {
    this.tab = tab;
    this.log.push(`tab:${tab}`);
  };
  showAgentUi = async (path: string, options?: { reveal?: boolean }) => {
    this.log.push(`ui:${path}${options?.reveal === false ? ':noreveal' : ''}`);
    this.local = 'agent-ui';
    this.path = path;
    this.contents.url = `http://127.0.0.1:7070${path}`;
    if (options?.reveal !== false) this.visible = true;
  };
  current = () => this.local;
  agentPath = () => this.path;
  agentContents = () => this.contents;
}

interface Rig {
  windows: FakeWindows;
  survival: SurvivalController;
  mode: { value: string | undefined };
  lang: { value: Lang | null };
  probe: ReturnType<typeof vi.fn>;
  restart: ReturnType<typeof vi.fn>;
  changes: () => number;
  clock: { now: number };
}

function rig(over: Partial<SurvivalDeps> = {}): Rig {
  const windows = new FakeWindows();
  const mode = { value: undefined as string | undefined };
  const lang = { value: null as Lang | null };
  const probe = vi.fn(async () => 'survival' as string | null);
  const restart = vi.fn();
  const clock = { now: 1_000_000 };
  const survival = new SurvivalController({ windows, currentMode: () => mode.value, chosenLang: () => lang.value, probeMode: probe, restart, clock: () => clock.now, ...over });
  let changes = 0;
  survival.onChange(() => void (changes += 1));
  return { windows, survival, mode, lang, probe, restart, changes: () => changes, clock };
}

const modeChanged = (to: string, from = 'online'): FeedMessage => ({ kind: 'event', event: { id: 1, type: 'mode.changed', data: { from, to, op_id: 'op_x', warnings: [] } } as AgentEvent });
const status = (mode: string): FeedMessage => ({ kind: 'status', status: { mode: { value: mode } } as AgentStatus });
const flush = async (): Promise<void> => {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
};

describe('entering survival', () => {
  it('shows and focuses the window, goes full screen, collapses the bar to 32 px, loads /portal, hides the Lab', async () => {
    const r = rig();
    r.windows.tab = 'lab';
    await r.survival.enter({ reason: 'mode' });
    expect(r.windows.log).toEqual(['tab:local', `bar:${STRIP_HEIGHT}`, 'showMain', 'ui:/portal']);
    expect(STRIP_HEIGHT).toBe(32);
    expect(r.windows.win.calls).toEqual(['fullscreen:true']);
    expect(r.survival.active).toBe(true);
    expect(r.survival.snapshot()).toMatchObject({ active: true, fullScreen: true, reason: 'mode', banner: null });
    expect(r.changes()).toBeGreaterThan(0);
  });

  it('the start-up sequence loads /portal itself (load: false)', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode', load: false });
    expect(r.windows.log).not.toContain('ui:/portal');
    expect(r.survival.active).toBe(true);
  });

  it('does not reload a Portal that is already showing', async () => {
    const r = rig();
    r.windows.local = 'agent-ui';
    r.windows.path = '/portal/status';
    await r.survival.enter({ reason: 'mode' });
    expect(r.windows.log.filter((l) => l.startsWith('ui:'))).toEqual([]);
  });

  it('is idempotent for events: a second survival event does not push a window the person took out of full screen back in', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    r.survival.setFullScreen(false);
    r.windows.log.length = 0;
    r.windows.win.calls.length = 0;
    await r.survival.enter({ reason: 'mode' });
    r.survival.handle(modeChanged('survival'));
    await flush();
    expect(r.windows.log).toEqual([]);
    expect(r.windows.win.calls).toEqual([]);
  });

  it('a tray click or a second launch (bringForward) puts the Portal back in full screen, hidden or not', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    r.survival.setFullScreen(false);
    r.windows.visible = false; // closed to the tray
    r.windows.win.calls.length = 0;
    r.survival.bringForward();
    expect(r.windows.visible).toBe(true);
    expect(r.windows.win.calls).toEqual(['fullscreen:true']);
  });

  it('outside survival bringForward only shows the window', () => {
    const r = rig();
    r.survival.bringForward();
    expect(r.windows.log).toEqual(['showMain']);
    expect(r.windows.win.calls).toEqual([]);
  });

  it('the reason is "mode" when the node is known to be in survival mode, whatever was asked', async () => {
    const r = rig();
    r.mode.value = 'survival';
    await r.survival.enter({ reason: 'portal' });
    expect(r.survival.snapshot().reason).toBe('mode');
  });

  it('a destroyed window is left alone', async () => {
    const r = rig();
    r.windows.win.destroyed = true;
    await r.survival.enter({ reason: 'mode' });
    expect(r.windows.win.calls).toEqual([]);
    expect(r.survival.snapshot().fullScreen).toBe(false);
  });
});

describe('the strip follows the window', () => {
  it('Exit full screen only leaves full screen; the mode is not touched and the strip stays', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    const before = r.changes();
    r.survival.setFullScreen(false);
    expect(r.windows.win.fullScreen).toBe(false);
    expect(r.survival.active).toBe(true);
    expect(r.windows.barHeight).toBe(STRIP_HEIGHT);
    expect(r.survival.snapshot().fullScreen).toBe(false);
    expect(r.changes()).toBeGreaterThan(before);
    r.survival.setFullScreen(true);
    expect(r.survival.snapshot().fullScreen).toBe(true);
  });

  it('F11 or the window manager leaving full screen reaches the strip through the window event', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    const before = r.changes();
    r.windows.win.fullScreen = false;
    for (const l of r.windows.win.listeners.get('leave-full-screen') ?? []) l();
    expect(r.changes()).toBe(before + 1);
    expect(r.survival.snapshot().fullScreen).toBe(false);
  });

  it('the strip button does nothing outside the survival form', () => {
    const r = rig();
    r.survival.setFullScreen(true);
    expect(r.windows.win.calls).toEqual([]);
  });

  it('state() adds the banner text in the display language', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode', banner: 'LAI-DSK-001' });
    expect(r.survival.state((code) => `text of ${code}`)).toEqual({ active: true, fullScreen: true, portalOnly: false, banner: { code: 'LAI-DSK-001', text: 'text of LAI-DSK-001' } });
    await r.survival.exit();
    expect(r.survival.state(() => 'x')).toEqual({ active: false, fullScreen: false, portalOnly: false, banner: null });
  });
});

describe('leaving survival', () => {
  it('on mode.changed to another mode: leaves full screen, the 48 px bar, the dashboard, without showing a hidden window', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    r.windows.visible = false;
    r.windows.log.length = 0;
    r.survival.handle(modeChanged('online', 'survival'));
    await flush();
    expect(r.survival.active).toBe(false);
    expect(r.windows.win.fullScreen).toBe(false);
    expect(r.windows.barHeight).toBe(TOP_BAR_HEIGHT);
    expect(r.windows.log).toEqual([`bar:${TOP_BAR_HEIGHT}`, 'ui:/:noreveal']);
    expect(r.windows.visible).toBe(false);
  });

  it.each(['online', 'offline'])('to %s', async (to) => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    r.survival.handle(modeChanged(to, 'survival'));
    await flush();
    expect(r.survival.active).toBe(false);
  });

  it('exit when not active does nothing', async () => {
    const r = rig();
    await r.survival.exit();
    expect(r.windows.log).toEqual([]);
  });

  it('a window already out of full screen is not asked to leave it again', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    r.survival.setFullScreen(false);
    r.windows.win.calls.length = 0;
    await r.survival.exit();
    expect(r.windows.win.calls).toEqual([]);
  });

  it('a Portal opened outside survival mode ignores mode changes, and its Back button closes it with no PIN and no mode change', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'portal' });
    expect(r.survival.snapshot().reason).toBe('portal');
    expect(r.survival.state(() => '').portalOnly).toBe(true);
    r.survival.handle(modeChanged('offline', 'online'));
    await flush();
    expect(r.survival.active).toBe(true);
    r.survival.closePortal();
    await flush();
    expect(r.survival.active).toBe(false);
    expect(r.windows.log.at(-1)).toBe('ui:/:noreveal');
  });

  it('the Back button cannot leave real survival mode (that needs the PIN)', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    r.survival.closePortal();
    await flush();
    expect(r.survival.active).toBe(true);
  });

  it('survival mode arriving while a --portal view is open makes it real survival (no Back)', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'portal' });
    r.mode.value = 'survival';
    r.survival.handle(modeChanged('survival'));
    await flush();
    expect(r.survival.snapshot().reason).toBe('mode');
    r.survival.closePortal();
    await flush();
    expect(r.survival.active).toBe(true);
  });
});

describe('the agent feed', () => {
  it('mode.changed to survival enters; a status read that says survival enters too (a missed event, a reconnect)', async () => {
    const a = rig();
    a.survival.handle(modeChanged('survival'));
    await flush();
    expect(a.survival.active).toBe(true);
    const b = rig();
    b.survival.handle(status('survival'));
    await flush();
    expect(b.survival.active).toBe(true);
  });

  it('ignores other events, statuses without a mode and malformed data', async () => {
    const r = rig();
    r.survival.handle({ kind: 'event', event: { id: 2, type: 'alert.raised', data: { to: 'survival' } } as AgentEvent });
    r.survival.handle({ kind: 'event', event: { id: 3, type: 'mode.changed', data: null } as AgentEvent });
    r.survival.handle({ kind: 'event', event: { id: 4, type: 'mode.changed', data: { to: 5 } } as AgentEvent });
    r.survival.handle({ kind: 'status', status: {} as AgentStatus });
    r.survival.handle({ kind: 'connection', state: 'connected' });
    await flush();
    expect(r.survival.active).toBe(false);
    expect(r.windows.log).toEqual([]);
  });

  it('a stale status read that started before the event does not undo it, an old one does', async () => {
    const r = rig();
    r.survival.handle(modeChanged('survival'));
    await flush();
    r.clock.now += 500;
    r.survival.handle(status('online')); // read before the event
    await flush();
    expect(r.survival.active).toBe(true);
    r.clock.now += EVENT_PRECEDENCE_MS;
    r.survival.handle(status('online'));
    await flush();
    expect(r.survival.active).toBe(false);
  });

  it('a failing load is logged, never thrown into the feed', async () => {
    const log = { write: vi.fn(), log: vi.fn() };
    const r = rig({ log });
    r.windows.showAgentUi = async () => {
      throw new Error('boom');
    };
    expect(() => r.survival.handle(modeChanged('survival'))).not.toThrow();
    await flush();
    expect(log.log).toHaveBeenCalledWith('warn', 'app', expect.stringContaining('boom'));
  });
});

describe('the token file is unreadable (degraded survival)', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('carries the code as a banner and watches /health, because no event arrives without a token', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode', banner: 'LAI-DSK-001' });
    expect(r.survival.snapshot().banner).toBe('LAI-DSK-001');
    await vi.advanceTimersByTimeAsync(DEGRADED_POLL_MS);
    expect(r.probe).toHaveBeenCalledTimes(1);
    expect(r.survival.active).toBe(true);
    // The agent stops answering: still survival as far as we know.
    r.probe.mockResolvedValue(null);
    await vi.advanceTimersByTimeAsync(DEGRADED_POLL_MS);
    expect(r.survival.active).toBe(true);
    // Survival ended in the Portal: windowed again, and the start-up sequence runs again (there is no dashboard without a token).
    r.probe.mockResolvedValue('online');
    await vi.advanceTimersByTimeAsync(DEGRADED_POLL_MS);
    expect(r.survival.active).toBe(false);
    expect(r.restart).toHaveBeenCalledTimes(1);
    expect(r.windows.log.filter((l) => l.startsWith('ui:'))).toEqual(['ui:/portal']); // only the first load; no dashboard
    expect(r.survival.snapshot().banner).toBeNull();
    // And it stops asking.
    r.probe.mockClear();
    await vi.advanceTimersByTimeAsync(DEGRADED_POLL_MS * 3);
    expect(r.probe).not.toHaveBeenCalled();
  });

  it('with a good token nothing polls', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    await vi.advanceTimersByTimeAsync(DEGRADED_POLL_MS * 3);
    expect(r.probe).not.toHaveBeenCalled();
  });

  it('a probe that throws is treated as "no answer"', async () => {
    const r = rig();
    r.probe.mockRejectedValue(new Error('x'));
    await r.survival.enter({ reason: 'mode', banner: 'LAI-DSK-003' });
    await vi.advanceTimersByTimeAsync(DEGRADED_POLL_MS);
    expect(r.survival.active).toBe(true);
  });

  it('dispose stops the timer', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode', banner: 'LAI-DSK-002' });
    r.survival.dispose();
    await r.survival.exit();
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe('language', () => {
  it('writes the choice into the Portal storage and reloads it once; the same language again does nothing', async () => {
    const c = new FakeContents();
    c.url = 'http://127.0.0.1:7070/portal/status';
    expect(await setPortalLanguage(c, 'es')).toBe(true);
    expect(c.stored.get(PORTAL_LANG_KEY)).toBe('es');
    expect(c.reloads).toBe(1);
    expect(await setPortalLanguage(c, 'es')).toBe(false);
    expect(c.reloads).toBe(1);
    expect(await setPortalLanguage(c, 'en')).toBe(true);
    expect(c.reloads).toBe(2);
  });

  it.each(['http://127.0.0.1:7070/', 'http://127.0.0.1:7070/chat/', 'http://127.0.0.1:7070/portalx', 'not a url', ''])('leaves %s alone', async (url) => {
    const c = new FakeContents();
    c.url = url;
    expect(await setPortalLanguage(c, 'es')).toBe(false);
    expect(c.scripts).toEqual([]);
  });

  it('leaves a missing or destroyed page, and a page whose script fails, alone', async () => {
    expect(await setPortalLanguage(null, 'es')).toBe(false);
    const gone = new FakeContents();
    gone.url = 'http://127.0.0.1:7070/portal';
    gone.destroyed = true;
    expect(await setPortalLanguage(gone, 'es')).toBe(false);
    const broken = new FakeContents();
    broken.url = 'http://127.0.0.1:7070/portal';
    broken.executeJavaScript = async () => {
      throw new Error('detached');
    };
    expect(await setPortalLanguage(broken, 'es')).toBe(false);
    expect(broken.reloads).toBe(0);
  });

  it('a language picked in the strip is applied when the Portal loads and when it changes; "auto" leaves the Portal alone', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    expect(r.windows.contents.scripts).toEqual([]); // auto: the Portal resolves it from the profile and the OS locale
    r.lang.value = 'es';
    await r.survival.languageChanged();
    expect(r.windows.contents.stored.get(PORTAL_LANG_KEY)).toBe('es');
    expect(r.windows.contents.reloads).toBe(1);
    // The next load of a Portal page (the start-up host, a re-entry) reads the same choice.
    await r.survival.portalLoaded();
    expect(r.windows.contents.reloads).toBe(1);
  });

  it('a language change outside the survival form does not touch the Portal', async () => {
    const r = rig();
    r.lang.value = 'es';
    await r.survival.languageChanged();
    expect(r.windows.contents.scripts).toEqual([]);
  });
});

describe('never a kiosk (desktop.md 4.7 rule 3)', () => {
  it('no window call but full screen and visibility is made across a whole survival round', async () => {
    const r = rig();
    await r.survival.enter({ reason: 'mode' });
    r.survival.setFullScreen(false);
    r.survival.setFullScreen(true);
    r.survival.bringForward();
    await r.survival.exit();
    expect(r.windows.win.setKiosk).not.toHaveBeenCalled();
    expect(r.windows.win.setSimpleFullScreen).not.toHaveBeenCalled();
  });

  it('the source never calls setKiosk or asks for a kiosk window', () => {
    const root = join(here, '..', '..', 'src');
    const files: string[] = [];
    const walk = (dir: string): void => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        if (entry.isDirectory()) walk(join(dir, entry.name));
        else if (/\.(ts|tsx)$/.test(entry.name)) files.push(join(dir, entry.name));
      }
    };
    walk(root);
    expect(files.length).toBeGreaterThan(10);
    for (const file of files) {
      const code = readFileSync(file, 'utf8')
        .split('\n')
        .filter((line) => !/^\s*(\/\/|\*|\/\*)/.test(line))
        .join('\n');
      expect(code, file).not.toMatch(/setKiosk\s*\(|\bkiosk\s*:|setSimpleFullScreen\s*\(/);
    }
  });
});

describe('the module-level seam', () => {
  afterEach(() => install(null));

  it('is a no-op until a controller is installed, then forwards', async () => {
    install(null);
    await expect(enter({ reason: 'mode' })).resolves.toBeUndefined();
    await expect(exit()).resolves.toBeUndefined();
    expect(bringForward()).toBe(false);

    const r = rig();
    install(r.survival);
    await enter({ reason: 'mode' });
    expect(r.survival.active).toBe(true);
    expect(bringForward()).toBe(true);
    await exit();
    expect(r.survival.active).toBe(false);
  });
});
