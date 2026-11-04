import { mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { DEFAULT_SETTINGS, configDir, normalizeSettings, readSettings, readSettingsFile, settingsPath, updateSettings, writeSettings } from '../../src/main/settings';

let dir: string;
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'ai-desktop-settings-'));
});
afterEach(async () => {
  await rm(dir, { recursive: true, force: true });
});

describe('settings.json (desktop.md 3.8)', () => {
  it('a missing file is the documented defaults', async () => {
    expect(await readSettings(dir)).toEqual(DEFAULT_SETTINGS);
    expect(DEFAULT_SETTINGS).toEqual({
      schema: 1,
      autostart: true,
      updates: { checkForUpdates: false },
      cloud: { url: 'https://ai.marin.cr', linked: false },
      secrets: { allowPlaintext: false },
      window: { bounds: {} },
      lang: 'auto',
    });
  });

  it('keeps keys it does not know, at the top level and inside the known sections, through a write', async () => {
    await writeFile(
      settingsPath(dir),
      JSON.stringify({ schema: 1, lang: 'es', future: { a: 1 }, cloud: { url: 'https://ai.marin.cr', linked: true, deviceName: 'x' }, window: { bounds: { width: 5 }, maximized: true } }),
    );
    const updated = await updateSettings(dir, { autostart: false, cloud: { linked: false } });
    expect(updated).toMatchObject({ lang: 'es', autostart: false, future: { a: 1 }, cloud: { linked: false, deviceName: 'x', url: 'https://ai.marin.cr' }, window: { bounds: { width: 5 }, maximized: true } });
    const onDisk = JSON.parse(await readFile(settingsPath(dir), 'utf8')) as Record<string, unknown>;
    expect(onDisk['future']).toEqual({ a: 1 });
    expect((onDisk['window'] as Record<string, unknown>)['maximized']).toBe(true);
  });

  it('writes the file with mode 0600 and leaves no temporary file behind', async () => {
    await writeSettings(dir, normalizeSettings({}));
    expect((await stat(settingsPath(dir))).mode & 0o777).toBe(0o600);
    const { readdir } = await import('node:fs/promises');
    expect(await readdir(dir)).toEqual(['settings.json']);
  });

  it('replaces values of the wrong type by the defaults', () => {
    const s = normalizeSettings({ autostart: 'yes', lang: 'fr', updates: 5, cloud: { url: '', linked: 1 } });
    expect(s).toMatchObject({ autostart: true, lang: 'auto', updates: { checkForUpdates: false }, cloud: { url: 'https://ai.marin.cr', linked: false } });
  });

  it('a damaged file gives the defaults and says so, and is not overwritten by a read', async () => {
    await writeFile(settingsPath(dir), '{ not json');
    const result = await readSettingsFile(dir);
    expect(result.problem).toBe('invalid-json');
    expect(result.settings).toEqual(DEFAULT_SETTINGS);
    expect(await readFile(settingsPath(dir), 'utf8')).toBe('{ not json');
  });

  it('the folder follows XDG_CONFIG_HOME, then ~/.config', () => {
    expect(configDir({ XDG_CONFIG_HOME: '/x/cfg' }, '/home/u')).toBe('/x/cfg/ai-desktop');
    expect(configDir({}, '/home/u')).toBe('/home/u/.config/ai-desktop');
    expect(configDir({ XDG_CONFIG_HOME: 'relative' }, '/home/u')).toBe('/home/u/.config/ai-desktop');
  });
});
