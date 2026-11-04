// Windows: the main window (BaseWindow + top bar + content view) and the Chat, Library and Maps windows.
// DSK-04 replaces this file. Until then it holds the stand-in DSK-03 needs: ONE window whose own page shows the error
// pages (default session, shell view) and one child view in `persist:agent` that shows the agent UI or the Portal.
import { join } from 'node:path';
import { type BrowserWindow, WebContentsView } from 'electron';
import { AGENT_HOST, AGENT_PORT, PARTITIONS } from './egress-guard';
import { hardenedWebPreferences, registerView } from './navigation';
import type { ContentHost } from './startup';
import type { DskCode, Lang } from './i18n';

export const MODULE = 'windows' as const;

export interface TemporaryHostOptions {
  window: BrowserWindow;
  isPackaged: boolean;
  /** Absolute folder of the built preload scripts (`out/preload`). */
  preloadDir: string;
  /** The language of the error pages (settings.lang resolved against the OS locale). */
  lang(): Lang;
  /** URL of a built shell page: `localai://app/error/?code=...` packaged, the Vite server in development. */
  shellPageUrl(page: string, query: string): string;
}

export interface TemporaryHost extends ContentHost {
  /** The agent view's webContents once it exists (tests and the 401 recovery read its URL). */
  agentContents(): Electron.WebContents | null;
  /** What the window shows now. */
  current(): 'none' | 'error' | 'agent-ui';
  /** The path the agent view was last asked to load (`/`, `/portal`), or null. */
  agentPath(): string | null;
}

export function createTemporaryHost(options: TemporaryHostOptions): TemporaryHost {
  const { window: win } = options;
  let view: WebContentsView | null = null;
  let attached = false;
  let showing: 'none' | 'error' | 'agent-ui' = 'none';
  let path: string | null = null;

  const fit = (): void => {
    if (!view) return;
    const { width, height } = win.getContentBounds();
    view.setBounds({ x: 0, y: 0, width, height });
  };
  win.on('resize', fit);

  const ensureView = (): WebContentsView => {
    if (view) return view;
    view = new WebContentsView({
      webPreferences: { ...hardenedWebPreferences(options.isPackaged), partition: PARTITIONS.agent, preload: join(options.preloadDir, 'agent.js') },
    });
    registerView(view.webContents, 'content');
    return view;
  };

  const detach = (): void => {
    if (view && attached) {
      win.contentView.removeChildView(view);
      attached = false;
    }
  };

  const reveal = (): void => {
    if (!win.isVisible()) win.show();
  };

  return {
    agentContents: () => view?.webContents ?? null,
    current: () => showing,
    agentPath: () => path,
    async showError(code: DskCode, extra?: { unit?: string }): Promise<void> {
      detach();
      showing = 'error';
      const query = new URLSearchParams({ code, lang: options.lang(), ...(extra?.unit !== undefined ? { unit: extra.unit } : {}) });
      reveal();
      await win.loadURL(options.shellPageUrl('error', `?${query.toString()}`)).catch(() => undefined);
    },
    async showAgentUi(target: string): Promise<void> {
      const v = ensureView();
      if (!attached) {
        win.contentView.addChildView(v);
        attached = true;
      }
      fit();
      showing = 'agent-ui';
      path = target;
      reveal();
      await v.webContents.loadURL(`http://${AGENT_HOST}:${AGENT_PORT}${target}`).catch(() => undefined);
    },
    reloadAgentUi(): void {
      view?.webContents.reloadIgnoringCache();
    },
  };
}
