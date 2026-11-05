// The window manager against fakes (docs/specs/desktop.md 3.4, 3.6, 3.8): layout, the content views, the tabs, child
// windows (one each), bounds clamping and saving, close behaviour. No Electron.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { viewOf } from '../../src/main/navigation';
import {
  type ChildWindowInit,
  type ChildWindowLike,
  type DisplayArea,
  type MainWindowEvent,
  type MainWindowInit,
  type MainWindowLike,
  type ModalInit,
  type ModalParent,
  type Rect,
  type ViewKind,
  type ViewLike,
  type WebContentsLike,
  MODAL_WINDOWS,
  VIEW_SPECS,
  createWindowManager,
  isCloudUrl,
  resolveBounds,
  wireNavigationHooks,
} from '../../src/main/windows';

// ---------------------------------------------------------------------------------------------------------------------
// resolveBounds
// ---------------------------------------------------------------------------------------------------------------------

const wa = (x: number, y: number, width: number, height: number): DisplayArea => ({ workArea: { x, y, width, height } });
const PRIMARY = wa(0, 27, 1920, 1053);
const SECOND = wa(1920, 0, 1280, 1024);

describe('resolveBounds', () => {
  it('nothing saved: the default size, no position (the window manager centres it)', () => {
    expect(resolveBounds(undefined, [PRIMARY], PRIMARY)).toEqual({ width: 1280, height: 860, maximized: false });
    expect(resolveBounds({}, [PRIMARY], PRIMARY)).toEqual({ width: 1280, height: 860, maximized: false });
  });

  it('the default size shrinks to a small display', () => {
    const small = wa(0, 0, 1024, 768);
    expect(resolveBounds(undefined, [small], small)).toEqual({ width: 1024, height: 768, maximized: false });
  });

  it('a saved rectangle on a display that exists is kept', () => {
    expect(resolveBounds({ x: 100, y: 120, width: 1000, height: 700 }, [PRIMARY, SECOND], PRIMARY)).toEqual({ x: 100, y: 120, width: 1000, height: 700, maximized: false });
    expect(resolveBounds({ x: 2000, y: 50, width: 1000, height: 700, maximized: true }, [PRIMARY, SECOND], PRIMARY)).toEqual({ x: 2000, y: 50, width: 1000, height: 700, maximized: true });
  });

  it('the display is gone: centred on the primary display', () => {
    const r = resolveBounds({ x: 2000, y: 50, width: 1000, height: 700 }, [PRIMARY], PRIMARY);
    expect(r).toEqual({ x: 460, y: 27 + Math.floor((1053 - 700) / 2), width: 1000, height: 700, maximized: false });
  });

  it('a rectangle that is almost wholly off screen counts as gone, one with its title bar on screen does not', () => {
    // 50 px visible: not reachable.
    expect(resolveBounds({ x: 1870, y: 100, width: 1000, height: 700 }, [PRIMARY], PRIMARY).x).toBe(460);
    // Hanging off the right edge but 400 px visible: pulled back inside.
    expect(resolveBounds({ x: 1520, y: 100, width: 1000, height: 700 }, [PRIMARY], PRIMARY)).toMatchObject({ x: 920, y: 100, width: 1000, height: 700 });
  });

  it('is pulled inside the work area vertically too (a panel at the top)', () => {
    expect(resolveBounds({ x: 10, y: 0, width: 1000, height: 700 }, [PRIMARY], PRIMARY)).toMatchObject({ y: 27 });
    expect(resolveBounds({ x: 10, y: 800, width: 1000, height: 700 }, [PRIMARY], PRIMARY)).toMatchObject({ y: 27 + 1053 - 700 });
  });

  it('a window larger than its display shrinks to it, a tiny one grows to the minimum', () => {
    expect(resolveBounds({ x: 0, y: 0, width: 4000, height: 3000 }, [PRIMARY], PRIMARY)).toMatchObject({ x: 0, y: 27, width: 1920, height: 1053 });
    expect(resolveBounds({ x: 10, y: 40, width: 100, height: 100 }, [PRIMARY], PRIMARY)).toMatchObject({ width: 900, height: 600 });
  });

  it('garbage in the file is ignored', () => {
    expect(resolveBounds({ x: Number.NaN, y: '3' as never, width: -5, height: 'big' as never }, [PRIMARY], PRIMARY)).toEqual({ width: 1280, height: 860, maximized: false });
    // Only half a position: no position.
    expect(resolveBounds({ x: 10, width: 1000, height: 700 }, [PRIMARY], PRIMARY)).toEqual({ width: 1000, height: 700, maximized: false });
  });

  it('picks the display the window overlaps most', () => {
    // 700 px on the second display, 300 px on the first.
    expect(resolveBounds({ x: 1620, y: 100, width: 1000, height: 700 }, [PRIMARY, SECOND], PRIMARY)).toMatchObject({ x: 1920, y: 100 });
  });
});

// ---------------------------------------------------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------------------------------------------------

let nextId = 1;

class FakeContents implements WebContentsLike {
  readonly id = nextId++;
  url = '';
  loads: string[] = [];
  reloads = 0;
  destroyed = false;
  sent: Array<{ channel: string; args: unknown[] }> = [];
  getURL(): string {
    return this.url;
  }
  async loadURL(url: string): Promise<void> {
    this.loads.push(url);
    this.url = url;
  }
  reloadIgnoringCache(): void {
    this.reloads += 1;
  }
  isDestroyed(): boolean {
    return this.destroyed;
  }
  send(channel: string, ...args: unknown[]): void {
    this.sent.push({ channel, args });
  }
}

class FakeView implements ViewLike {
  readonly webContents = new FakeContents();
  bounds: Rect | null = null;
  constructor(readonly kind: ViewKind) {}
  setBounds(bounds: Rect): void {
    this.bounds = bounds;
  }
}

class FakeMain implements MainWindowLike {
  children: ViewLike[] = [];
  contentView = {
    addChildView: (view: ViewLike) => void this.children.push(view),
    removeChildView: (view: ViewLike) => void (this.children = this.children.filter((c) => c !== view)),
  };
  content: Rect = { x: 0, y: 0, width: 1280, height: 800 };
  normal: Rect = { x: 50, y: 60, width: 1280, height: 860 };
  maximized = false;
  fullScreen = false;
  visible = false;
  minimized = false;
  destroyed = false;
  calls: string[] = [];
  listeners = new Map<string, Array<() => void>>();
  closeListeners: Array<(event: { preventDefault(): void }) => void> = [];
  closedListeners: Array<() => void> = [];
  constructor(readonly init: MainWindowInit) {}
  getContentBounds = () => this.content;
  getNormalBounds = () => this.normal;
  isMaximized = () => this.maximized;
  maximize = () => void (this.maximized = true);
  isFullScreen = () => this.fullScreen;
  setFullScreen = (flag: boolean) => void (this.fullScreen = flag);
  isVisible = () => this.visible;
  isMinimized = () => this.minimized;
  isDestroyed = () => this.destroyed;
  show = () => void ((this.visible = true), this.calls.push('show'));
  hide = () => void ((this.visible = false), this.calls.push('hide'));
  restore = () => void ((this.minimized = false), this.calls.push('restore'));
  focus = () => void this.calls.push('focus');
  /** Like Electron: `close` listeners may cancel; otherwise the window is destroyed and `closed` fires. */
  close = (): void => {
    this.calls.push('close');
    let prevented = false;
    for (const l of this.closeListeners) l({ preventDefault: () => void (prevented = true) });
    if (prevented) return;
    this.destroyed = true;
    for (const l of this.closedListeners) l();
  };
  onEvent = (event: MainWindowEvent, listener: () => void) => void this.listeners.set(event, [...(this.listeners.get(event) ?? []), listener]);
  onClose = (listener: (event: { preventDefault(): void }) => void) => void this.closeListeners.push(listener);
  onClosed = (listener: () => void) => void this.closedListeners.push(listener);
  fire(event: MainWindowEvent): void {
    for (const l of this.listeners.get(event) ?? []) l();
  }
}

class FakeChild implements ChildWindowLike {
  readonly webContents = new FakeContents();
  destroyed = false;
  minimized = false;
  calls: string[] = [];
  closed: Array<() => void> = [];
  constructor(readonly init: ChildWindowInit) {}
  isDestroyed = () => this.destroyed;
  isMinimized = () => this.minimized;
  restore = () => void ((this.minimized = false), this.calls.push('restore'));
  show = () => void this.calls.push('show');
  focus = () => void this.calls.push('focus');
  close = () => {
    this.destroyed = true;
    for (const l of this.closed) l();
  };
  onClosed = (listener: () => void) => void this.closed.push(listener);
}

class FakeModal extends FakeChild {
  constructor(readonly modal: ModalInit, readonly parent: ModalParent) {
    super({ width: modal.width, height: modal.height, title: modal.title });
  }
}

function setup(options: { saved?: Parameters<typeof createWindowManager>[0]['savedBounds']; startHidden?: boolean; lang?: 'en' | 'es' } = {}) {
  const made = { main: null as FakeMain | null, views: [] as FakeView[], children: [] as FakeChild[], modals: [] as FakeModal[] };
  const saves: Array<Record<string, unknown>> = [];
  const quit = vi.fn();
  let lang: 'en' | 'es' = options.lang ?? 'en';
  const manager = createWindowManager({
    factory: {
      createMainWindow: (init) => (made.main = new FakeMain(init)),
      createView: (kind) => {
        const view = new FakeView(kind);
        made.views.push(view);
        return view;
      },
      createChildWindow: (init) => {
        const child = new FakeChild(init);
        made.children.push(child);
        return child;
      },
      createModalWindow: (init, parent) => {
        const modal = new FakeModal(init, parent);
        made.modals.push(modal);
        return modal;
      },
    },
    lang: () => lang,
    shellPageUrl: (page, query = '') => `localai://app/${page}/${query}`,
    isShellUrl: (url) => url.startsWith('localai://app/'),
    savedBounds: options.saved,
    saveBounds: async (bounds) => void saves.push({ ...bounds }),
    displays: () => ({ all: [PRIMARY], primary: PRIMARY }),
    quitApp: quit,
    startHidden: options.startHidden,
    saveDelayMs: 500,
  });
  const view = (kind: ViewKind) => made.views.find((v) => v.kind === kind)!;
  return { manager, made, main: made.main!, view, saves, quit, setLang: (l: 'en' | 'es') => (lang = l) };
}

beforeEach(() => {
  vi.useFakeTimers();
  nextId = 1;
});
afterEach(() => {
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------------------------------------------------

describe('the main window', () => {
  it('opens hidden at 1280x860, min 900x600, titled "Local AI", with the top bar view loaded at once', () => {
    const { main, view } = setup();
    expect(main.init).toMatchObject({ width: 1280, height: 860, minWidth: 900, minHeight: 600, title: 'Local AI' });
    expect(main.visible).toBe(false);
    expect(view('topbar').webContents.loads).toEqual(['localai://app/topbar/']);
    expect(main.children).toEqual([view('topbar')]);
  });

  it('the view kinds match the spec table: partitions, preloads, navigation views', () => {
    expect(VIEW_SPECS).toEqual({
      topbar: { partition: undefined, preload: 'shell.js', view: 'shell' },
      agent: { partition: 'persist:agent', preload: 'agent.js', view: 'content' },
      lab: { partition: 'persist:lab', preload: 'shell.js', view: 'shell' },
      error: { partition: undefined, preload: 'shell.js', view: 'shell' },
    });
  });

  it('every view is registered with the navigation guard before it loads anything', () => {
    const { manager, view } = setup();
    expect(viewOf(view('topbar').webContents as never)).toBe('shell');
    void manager.showError('LAI-DSK-004');
    void manager.showAgentUi('/');
    expect(viewOf(view('error').webContents as never)).toBe('shell');
    expect(viewOf(view('agent').webContents as never)).toBe('content');
  });

  it('keeps the layout on resize: a 48 px bar over the content, which fills the rest', async () => {
    const { manager, main, view } = setup();
    await manager.showAgentUi('/');
    expect(view('topbar').bounds).toEqual({ x: 0, y: 0, width: 1280, height: 48 });
    expect(view('agent').bounds).toEqual({ x: 0, y: 48, width: 1280, height: 752 });
    main.content = { x: 0, y: 0, width: 900, height: 600 };
    main.fire('resize');
    expect(view('topbar').bounds).toEqual({ x: 0, y: 0, width: 900, height: 48 });
    expect(view('agent').bounds).toEqual({ x: 0, y: 48, width: 900, height: 552 });
  });

  it('setTopBarHeight re-lays out (the survival strip, DSK-09)', async () => {
    const { manager, view } = setup();
    await manager.showAgentUi('/portal');
    manager.setTopBarHeight(32);
    expect(view('topbar').bounds?.height).toBe(32);
    expect(view('agent').bounds).toMatchObject({ y: 32, height: 768 });
    manager.setTopBarHeight(-4);
    expect(view('topbar').bounds?.height).toBe(32);
  });

  it('showAgentUi / showError reveal the window and load the right URL', async () => {
    const { manager, main, view } = setup();
    await manager.showError('LAI-DSK-004', { unit: 'inactive' });
    expect(main.visible).toBe(true);
    expect(view('error').webContents.loads[0]).toBe('localai://app/error/?code=LAI-DSK-004&lang=en&unit=inactive');
    expect(main.children).toEqual([view('topbar'), view('error')]);
    expect(manager.current()).toBe('error');

    await manager.showAgentUi('/portal');
    expect(view('agent').webContents.loads).toEqual(['http://127.0.0.1:7070/portal']);
    // The error view is detached, the agent view attached: one content view at a time.
    expect(main.children).toEqual([view('topbar'), view('agent')]);
    expect(manager.current()).toBe('agent-ui');
    expect(manager.agentPath()).toBe('/portal');
  });

  it('showAgentUi({ reveal: false }) loads without showing a window that was closed to the tray (leaving survival)', async () => {
    const { manager, main, view } = setup();
    await manager.showAgentUi('/portal');
    manager.hideMain();
    expect(main.visible).toBe(false);
    await manager.showAgentUi('/', { reveal: false });
    expect(main.visible).toBe(false);
    expect(view('agent').webContents.loads.at(-1)).toBe('http://127.0.0.1:7070/');
    await manager.showAgentUi('/');
    expect(main.visible).toBe(true);
  });

  it('the error page is in the chosen language', async () => {
    const { manager, view } = setup({ lang: 'es' });
    await manager.showError('LAI-DSK-003');
    expect(view('error').webContents.loads[0]).toContain('lang=es');
  });

  it('startHidden keeps the window hidden until showMain()', async () => {
    const { manager, main } = setup({ startHidden: true });
    await manager.showAgentUi('/');
    expect(main.visible).toBe(false);
    manager.showMain();
    expect(main.visible).toBe(true);
    expect(main.calls).toContain('focus');
  });

  it('showMain restores a minimised window; hideMain hides it', () => {
    const { manager, main } = setup();
    main.minimized = true;
    manager.showMain();
    expect(main.calls).toEqual(['restore', 'show', 'focus']);
    manager.hideMain();
    expect(manager.isMainVisible()).toBe(false);
  });

  it('reloadAgentUi reloads the agent view only', async () => {
    const { manager, view } = setup();
    manager.reloadAgentUi(); // no agent view yet: nothing happens
    await manager.showAgentUi('/');
    manager.reloadAgentUi();
    expect(view('agent').webContents.reloads).toBe(1);
    expect(view('topbar').webContents.reloads).toBe(0);
  });
});

describe('tabs and the Lab', () => {
  it('the Lab tab cannot be shown while it is not available', async () => {
    const { manager, view } = setup();
    await manager.showAgentUi('/');
    manager.showTab('lab');
    expect(manager.tab()).toBe('local');
    expect(manager.labAvailable()).toBe(false);
    expect(view('agent').bounds).not.toBeNull();
    expect(() => view('lab')).not.toThrow(); // undefined: no lab view was created
    expect(view('lab')).toBeUndefined();
  });

  it('when available: Lab swaps the content view and loads localai://app/lab/ once; local brings the agent UI back', async () => {
    const { manager, main, view } = setup();
    await manager.showAgentUi('/');
    manager.setLabAvailable(true);
    manager.showTab('lab');
    expect(manager.tab()).toBe('lab');
    expect(main.children).toEqual([view('topbar'), view('lab')]);
    expect(view('lab').webContents.loads).toEqual(['localai://app/lab/']);
    expect(viewOf(view('lab').webContents as never)).toBe('shell');
    manager.showTab('local');
    expect(main.children).toEqual([view('topbar'), view('agent')]);
    manager.showTab('lab');
    expect(view('lab').webContents.loads).toHaveLength(1);
    // The agent view was not reloaded or recreated.
    expect(view('agent').webContents.loads).toHaveLength(1);
  });

  it('leaving connected mode while on the Lab tab returns to the local tab', async () => {
    const { manager, main, view } = setup();
    await manager.showAgentUi('/');
    manager.setLabAvailable(true);
    manager.showTab('lab');
    manager.setLabAvailable(false);
    expect(manager.tab()).toBe('local');
    expect(main.children).toEqual([view('topbar'), view('agent')]);
  });

  it('an error during the Lab tab brings the local tab (and the error page) forward', async () => {
    const { manager, main, view } = setup();
    await manager.showAgentUi('/');
    manager.setLabAvailable(true);
    manager.showTab('lab');
    await manager.showError('LAI-DSK-007');
    expect(manager.tab()).toBe('local');
    expect(main.children).toEqual([view('topbar'), view('error')]);
  });

  it('onChange fires for tab and availability changes, and a throwing listener does not stop the others', async () => {
    const { manager } = setup();
    const seen = vi.fn();
    manager.onChange(() => {
      throw new Error('x');
    });
    const off = manager.onChange(seen);
    manager.setLabAvailable(true);
    manager.showTab('lab');
    expect(seen).toHaveBeenCalledTimes(2);
    off();
    manager.showTab('local');
    expect(seen).toHaveBeenCalledTimes(2);
  });
});

describe('child windows', () => {
  it('opens Chat, Library and Maps at their paths, in the agent view class, titled in the language', () => {
    const { manager, made } = setup({ lang: 'es' });
    manager.openChild('chat');
    manager.openChild('library');
    manager.openChild('maps');
    expect(made.children.map((c) => c.webContents.loads)).toEqual([['http://127.0.0.1:7070/chat/'], ['http://127.0.0.1:7070/library/'], ['http://127.0.0.1:7070/maps/']]);
    expect(made.children.map((c) => c.init.title)).toEqual(['Local AI: Chat', 'Local AI: Biblioteca', 'Local AI: Mapas']);
    expect(made.children.map((c) => viewOf(c.webContents as never))).toEqual(['chat', 'library', 'maps']);
  });

  it('one window each: a second request focuses the open one (restoring it when minimised)', () => {
    const { manager, made } = setup();
    const first = manager.openChild('chat');
    (first as FakeChild).minimized = true;
    const second = manager.openChild('chat');
    expect(second).toBe(first);
    expect(made.children).toHaveLength(1);
    expect((first as FakeChild).calls).toEqual(['restore', 'show', 'focus']);
    expect(made.children[0]!.webContents.loads).toHaveLength(1);
  });

  it('after the window is closed a new request opens a new one', () => {
    const { manager, made } = setup();
    const first = manager.openChild('maps') as FakeChild;
    first.close();
    expect(manager.childWindow('maps')).toBeNull();
    const second = manager.openChild('maps');
    expect(second).not.toBe(first);
    expect(made.children).toHaveLength(2);
  });

  it('closing the main window closes the children and quits the app', async () => {
    const { manager, main, made, quit } = setup();
    manager.openChild('chat');
    manager.openChild('library');
    main.close();
    await vi.runAllTimersAsync();
    expect(made.children.every((c) => c.destroyed)).toBe(true);
    expect(quit).toHaveBeenCalledTimes(1);
  });
});

describe('modal shell windows (PIN prompt, connection info)', () => {
  it('opens each page once and focuses it when it is already open', () => {
    const { manager, main, made } = setup();
    main.visible = true;
    const first = manager.openModal('connection-info');
    expect(made.modals).toHaveLength(1);
    expect(made.modals[0]!.modal).toMatchObject({ url: 'localai://app/connection-info/', preload: 'shell.js', title: 'Connection info' });
    expect(manager.openModal('connection-info')).toBe(first);
    expect(made.modals).toHaveLength(1);
    expect((first as FakeModal).calls).toEqual(['show', 'focus']);
    first.close();
    expect(manager.modalWindow('connection-info')).toBeNull();
    manager.openModal('connection-info');
    expect(made.modals).toHaveLength(2);
  });

  it('the PIN prompt has its own strict preload and carries its query; titles follow the language', () => {
    const { manager, main, made } = setup({ lang: 'es' });
    main.visible = true;
    manager.openModal('pin', '?purpose=rotate-key&lang=es');
    expect(made.modals[0]!.modal).toMatchObject({ url: 'localai://app/pin/?purpose=rotate-key&lang=es', preload: 'pin.js', title: 'PIN de administrador' });
    expect(MODAL_WINDOWS.pin.preload).toBe('pin.js');
  });

  it('is modal to the main window while that is visible, to nothing when it is hidden or minimised', () => {
    const { manager, main, made } = setup();
    manager.openModal('connection-info');
    expect(made.modals[0]!.parent).toBeNull();
    made.modals[0]!.close();
    main.visible = true;
    manager.openModal('connection-info');
    expect(made.modals[1]!.parent).toEqual({ main });
    made.modals[1]!.close();
    main.minimized = true;
    manager.openModal('connection-info');
    expect(made.modals[2]!.parent).toBeNull();
  });

  it('the PIN prompt over the connection info is modal to the connection info', () => {
    const { manager, main, made } = setup();
    main.visible = true;
    const info = manager.openModal('connection-info');
    manager.openModal('pin');
    expect(made.modals[1]!.parent).toEqual({ child: info });
  });

  it('every modal is registered as a shell page with the navigation guard', () => {
    const { manager, main } = setup();
    main.visible = true;
    expect(viewOf(manager.openModal('pin').webContents as never)).toBe('shell');
  });

  it('closing the main window closes the modals too', async () => {
    const { manager, main } = setup();
    main.visible = true;
    const pin = manager.openModal('pin');
    main.close();
    await vi.advanceTimersByTimeAsync(10);
    expect(pin.isDestroyed()).toBe(true);
  });
});

describe('closing the main window', () => {
  it('quits by default, after saving the bounds', async () => {
    const { main, saves, quit } = setup();
    main.close();
    await vi.advanceTimersByTimeAsync(0);
    expect(saves).toEqual([{ x: 50, y: 60, width: 1280, height: 860, maximized: false }]);
    expect(main.destroyed).toBe(true);
    expect(quit).toHaveBeenCalledTimes(1);
  });

  it('with hide-on-close (a tray exists) it hides instead, and really closes once the app is quitting', async () => {
    const { manager, main, quit } = setup();
    manager.setHideOnClose(true);
    main.visible = true;
    main.close();
    expect(main.destroyed).toBe(false);
    expect(main.visible).toBe(false);
    expect(quit).not.toHaveBeenCalled();
    manager.prepareToQuit();
    main.close();
    await vi.advanceTimersByTimeAsync(0);
    expect(main.destroyed).toBe(true);
    expect(quit).toHaveBeenCalledTimes(1);
  });
});

describe('window bounds', () => {
  it('opens with the saved bounds, clamped, and maximised when it was', () => {
    const { main } = setup({ saved: { x: 5000, y: 5000, width: 1000, height: 700, maximized: true } });
    expect(main.init).toMatchObject({ x: 460, y: 203, width: 1000, height: 700 });
    expect(main.maximized).toBe(true);
  });

  it('saves the normal bounds once, 500 ms after the last resize or move', async () => {
    const { main, saves } = setup();
    main.fire('resize');
    await vi.advanceTimersByTimeAsync(300);
    main.normal = { x: 10, y: 20, width: 1100, height: 700 };
    main.fire('move');
    await vi.advanceTimersByTimeAsync(499);
    expect(saves).toEqual([]);
    await vi.advanceTimersByTimeAsync(2);
    expect(saves).toEqual([{ x: 10, y: 20, width: 1100, height: 700, maximized: false }]);
  });

  it('records maximised as a flag and keeps the normal rectangle', async () => {
    const { main, saves } = setup();
    main.maximized = true;
    main.fire('maximize');
    await vi.advanceTimersByTimeAsync(600);
    expect(saves).toEqual([{ x: 50, y: 60, width: 1280, height: 860, maximized: true }]);
  });

  it('saves nothing while full screen (the survival Portal)', async () => {
    const { main, saves } = setup();
    main.fullScreen = true;
    main.fire('resize');
    await vi.advanceTimersByTimeAsync(600);
    expect(saves).toEqual([]);
  });

  it('a failing save is swallowed', async () => {
    const warn = vi.fn();
    const main = setup();
    // A second manager whose store rejects.
    const failing = createWindowManager({
      factory: { createMainWindow: (init) => new FakeMain(init), createView: (k) => new FakeView(k), createChildWindow: (i) => new FakeChild(i), createModalWindow: (i, p) => new FakeModal(i, p) },
      lang: () => 'en',
      shellPageUrl: (p) => `localai://app/${p}/`,
      isShellUrl: () => true,
      saveBounds: () => Promise.reject(new Error('disk full')),
      displays: () => ({ all: [PRIMARY], primary: PRIMARY }),
      quitApp: () => undefined,
      log: { write: () => undefined, log: warn },
    });
    (failing.mainWindow() as FakeMain).fire('resize');
    await vi.advanceTimersByTimeAsync(600);
    expect(warn).toHaveBeenCalledWith('warn', 'app', expect.stringContaining('disk full'));
    expect(main.saves).toEqual([]);
  });
});

describe('messages to pages', () => {
  it('sendToTopBar reaches the top bar only', async () => {
    const { manager, view } = setup();
    await manager.showAgentUi('/');
    manager.sendToTopBar('localai:notice', { text: 'x' });
    expect(view('topbar').webContents.sent).toEqual([{ channel: 'localai:notice', args: [{ text: 'x' }] }]);
    expect(view('agent').webContents.sent).toEqual([]);
  });

  it('broadcastToShell reaches the shell pages that show an allowed shell URL, never the agent view', async () => {
    const { manager, view } = setup();
    await manager.showError('LAI-DSK-004');
    await manager.showAgentUi('/');
    manager.setLabAvailable(true);
    manager.showTab('lab');
    view('topbar').webContents.url = 'localai://app/topbar/';
    manager.broadcastToShell('localai:status-changed', { n: 1 });
    expect(view('topbar').webContents.sent).toHaveLength(1);
    expect(view('lab').webContents.sent).toHaveLength(1);
    expect(view('error').webContents.sent).toHaveLength(1);
    expect(view('agent').webContents.sent).toHaveLength(0);
    // A shell view that navigated somewhere that is not a shell page gets nothing.
    view('lab').webContents.url = 'http://127.0.0.1:7070/';
    manager.broadcastToShell('localai:status-changed', { n: 2 });
    expect(view('lab').webContents.sent).toHaveLength(1);
    expect(view('error').webContents.sent).toHaveLength(2);
    // A destroyed view is skipped.
    view('error').webContents.destroyed = true;
    manager.broadcastToShell('localai:status-changed', { n: 3 });
    expect(view('error').webContents.sent).toHaveLength(2);
  });
});

describe('navigation hooks', () => {
  it('open the child window, tell the top bar about a blocked link, and open only the cloud in the browser, only when connected', async () => {
    const { manager, made, view } = setup();
    const openExternal = vi.fn();
    let connected = false;
    let lang: 'en' | 'es' = 'en';
    wireNavigationHooks(manager, { openExternal, lang: () => lang, connected: () => connected });
    const { applyNavigation } = await import('../../src/main/navigation');
    const contents = {} as never;
    const state = { connected: false };

    applyNavigation(contents, 'content', 'http://127.0.0.1:7070/library/x', state, 'navigate');
    expect(made.children.map((c) => c.webContents.loads[0])).toEqual(['http://127.0.0.1:7070/library/']);

    applyNavigation(contents, 'content', 'https://example.com/', state, 'navigate');
    expect(view('topbar').webContents.sent.at(-1)).toEqual({
      channel: 'localai:notice',
      args: [{ kind: 'internet-off', text: 'Links to the internet are turned off on this computer.' }],
    });
    lang = 'es';
    applyNavigation(contents, 'content', 'https://example.com/', state, 'navigate');
    expect((view('topbar').webContents.sent.at(-1)!.args[0] as { text: string }).text).toContain('desactivados');

    // The cloud: blocked while offline; in connected mode it goes to the browser, once the hook re-checks.
    applyNavigation(contents, 'content', 'https://ai.marin.cr/lab/nodes/ai-n1', { connected: true }, 'navigate');
    expect(openExternal).not.toHaveBeenCalled(); // the hook's own state says not connected
    connected = true;
    applyNavigation(contents, 'content', 'https://ai.marin.cr/lab/nodes/ai-n1', { connected: true }, 'navigate');
    expect(openExternal).toHaveBeenCalledWith('https://ai.marin.cr/lab/nodes/ai-n1');
  });

  it('isCloudUrl accepts https://ai.marin.cr only', () => {
    expect(isCloudUrl('https://ai.marin.cr/activate?code=ABCD')).toBe(true);
    for (const bad of ['http://ai.marin.cr/', 'https://ai.marin.cr:8443/', `https://u:p${'@'}ai.marin.cr/`, 'https://ai.marin.cr.evil.example/', 'https://example.com/', 'javascript:alert(1)', '']) {
      expect(isCloudUrl(bad), bad).toBe(false);
    }
  });
});
