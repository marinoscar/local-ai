import { EventEmitter } from 'node:events';
import { describe, expect, it, vi } from 'vitest';

// The stand-in window host of DSK-03 with `--hidden` (DSK-07): loading a page does not reveal the window until something shows it.
vi.mock('electron', () => ({
  WebContentsView: class {
    setBounds = (): void => undefined;
    webContents = { loadURL: () => Promise.resolve(), reloadIgnoringCache: () => undefined, on: () => undefined, setWindowOpenHandler: () => undefined, session: {} };
  },
}));
vi.mock('../../src/main/navigation', () => ({ hardenedWebPreferences: () => ({}), registerView: () => undefined }));

class FakeWindow extends EventEmitter {
  visible = false;
  shown = 0;
  contentView = { addChildView: () => undefined, removeChildView: () => undefined };
  isVisible = (): boolean => this.visible;
  getContentBounds = (): { width: number; height: number } => ({ width: 800, height: 600 });
  loadURL = (): Promise<void> => Promise.resolve();
  show(): void {
    this.visible = true;
    this.shown += 1;
    this.emit('show');
  }
}

async function make(startHidden: boolean) {
  const { createTemporaryHost } = await import('../../src/main/windows');
  const win = new FakeWindow();
  const host = createTemporaryHost({
    window: win as never,
    isPackaged: true,
    preloadDir: '/x',
    lang: () => 'en',
    shellPageUrl: (page, query) => `localai://app/${page}/${query}`,
    ...(startHidden ? { startHidden } : {}),
  });
  return { win, host };
}

describe('createTemporaryHost startHidden', () => {
  it('without --hidden the window shows with the first page, as before', async () => {
    const { win, host } = await make(false);
    await host.showAgentUi('/');
    expect(win.shown).toBe(1);
  });

  it('with --hidden neither the agent UI nor an error page reveals the window', async () => {
    const { win, host } = await make(true);
    await host.showAgentUi('/');
    await host.showError('LAI-DSK-004');
    await host.showAgentUi('/portal');
    expect(win.shown).toBe(0);
    expect(host.current()).toBe('agent-ui');
  });

  it('once something shows the window (tray, second launch, survival) pages reveal it again', async () => {
    const { win, host } = await make(true);
    await host.showAgentUi('/');
    expect(win.shown).toBe(0);
    win.show();
    win.visible = false; // hidden to the tray again
    await host.showError('LAI-DSK-004');
    expect(win.shown).toBe(2);
  });
});
