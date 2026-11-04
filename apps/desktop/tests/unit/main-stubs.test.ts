/// <reference types="vite/client" />
import { readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const here = dirname(fileURLToPath(import.meta.url));
const mainDir = join(here, '..', '..', 'src', 'main');
const preloadDir = join(here, '..', '..', 'src', 'preload');

// desktop.md section 3.1: every main-process module, one file each.
const MAIN_MODULES = [
  'index', 'agent-client', 'agent-supervisor', 'windows', 'navigation', 'egress-guard', 'csp', 'protocol', 'tray',
  'autostart', 'clipboard', 'pin', 'survival', 'cloud-link', 'cloud-api', 'secret-store', 'settings', 'updates', 'i18n',
  'diagnose',
];

const loaders = import.meta.glob('../../src/main/*.ts');

describe('src/main layout', () => {
  it('has exactly the modules of the spec', () => {
    const files = readdirSync(mainDir).filter((f) => f.endsWith('.ts')).map((f) => f.replace(/\.ts$/, ''));
    expect(files.sort()).toEqual([...MAIN_MODULES].sort());
  });

  it.each(MAIN_MODULES)('%s imports without starting Electron', async (name) => {
    const load = loaders[`../../src/main/${name}.ts`];
    expect(load, `no loader for ${name}`).toBeDefined();
    const mod = (await load!()) as { MODULE?: string };
    expect(mod.MODULE).toBe(name);
  });

  it('index exports start() but does not call it under a test', async () => {
    const mod = (await import('../../src/main/index')) as { start: unknown };
    expect(typeof mod.start).toBe('function');
  });
});

describe('src/preload layout', () => {
  it('has the two bridges of the spec', () => {
    expect(readdirSync(preloadDir).sort()).toEqual(['agent.ts', 'shell.ts']);
  });

  it('shell.ts and agent.ts import and name their bridge', async () => {
    const shell = (await import('../../src/preload/shell')) as { BRIDGE: string };
    const agent = (await import('../../src/preload/agent')) as { BRIDGE: string };
    expect(shell.BRIDGE).toBe('localaiDesktop');
    expect(agent.BRIDGE).toBe('localaiDesktopHost');
  });
});

describe('src/renderer layout', () => {
  it.each(['topbar', 'error', 'pin', 'connection-info', 'link', 'lab'])('%s has an index.html and a main.tsx', (page) => {
    const files = readdirSync(join(here, '..', '..', 'src', 'renderer', page));
    expect(files).toContain('index.html');
    expect(files).toContain('main.tsx');
  });
});
