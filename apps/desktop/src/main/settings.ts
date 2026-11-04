// Settings file at ~/.config/ai-desktop/settings.json (docs/specs/desktop.md 3.8), mode 0600.
// Keys this version does not know are kept when the file is written back (a newer or older ai-desktop shares it).
import { mkdir, readFile, rename, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { join } from 'node:path';

export const MODULE = 'settings' as const;

export type LangSetting = 'auto' | 'en' | 'es';

export interface Settings {
  schema: 1;
  autostart: boolean;
  updates: { checkForUpdates: boolean; [unknown: string]: unknown };
  cloud: { url: string; linked: boolean; [unknown: string]: unknown };
  secrets: { allowPlaintext: boolean; [unknown: string]: unknown };
  window: { bounds: Record<string, unknown>; [unknown: string]: unknown };
  lang: LangSetting;
  [unknown: string]: unknown;
}

export const DEFAULT_SETTINGS: Readonly<Settings> = Object.freeze({
  schema: 1,
  autostart: true,
  updates: { checkForUpdates: false },
  cloud: { url: 'https://ai.marin.cr', linked: false },
  secrets: { allowPlaintext: false },
  window: { bounds: {} },
  lang: 'auto',
});

/** `~/.config/ai-desktop`, or `$XDG_CONFIG_HOME/ai-desktop` (Electron's userData folder follows the same rule). */
export function configDir(env: NodeJS.ProcessEnv = process.env, home: string = homedir()): string {
  const base = env['XDG_CONFIG_HOME'] && env['XDG_CONFIG_HOME'].startsWith('/') ? env['XDG_CONFIG_HOME'] : join(home, '.config');
  return join(base, 'ai-desktop');
}

export function settingsPath(dir: string): string {
  return join(dir, 'settings.json');
}

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/** Known keys with the right type, defaults otherwise; every other key is carried over untouched. */
export function normalizeSettings(raw: unknown): Settings {
  const input = isObject(raw) ? raw : {};
  const section = (name: 'updates' | 'cloud' | 'secrets' | 'window'): Record<string, unknown> => (isObject(input[name]) ? (input[name] as Record<string, unknown>) : {});
  const d = DEFAULT_SETTINGS;
  const updates = section('updates');
  const cloud = section('cloud');
  const secrets = section('secrets');
  const win = section('window');
  const lang = input['lang'];
  return {
    ...input,
    schema: 1,
    autostart: typeof input['autostart'] === 'boolean' ? input['autostart'] : d.autostart,
    updates: { ...updates, checkForUpdates: typeof updates['checkForUpdates'] === 'boolean' ? updates['checkForUpdates'] : d.updates.checkForUpdates },
    cloud: {
      ...cloud,
      url: typeof cloud['url'] === 'string' && cloud['url'] !== '' ? cloud['url'] : d.cloud.url,
      linked: typeof cloud['linked'] === 'boolean' ? cloud['linked'] : d.cloud.linked,
    },
    secrets: { ...secrets, allowPlaintext: typeof secrets['allowPlaintext'] === 'boolean' ? secrets['allowPlaintext'] : d.secrets.allowPlaintext },
    window: { ...win, bounds: isObject(win['bounds']) ? win['bounds'] : {} },
    lang: lang === 'en' || lang === 'es' || lang === 'auto' ? lang : d.lang,
  };
}

export interface ReadResult {
  settings: Settings;
  /** Set when the file existed but could not be used (damaged JSON, unreadable); the defaults are returned. */
  problem?: 'unreadable' | 'invalid-json';
}

/** A missing file is the defaults; a damaged one is the defaults plus `problem`, and is left alone until something writes. */
export async function readSettingsFile(dir: string): Promise<ReadResult> {
  let text: string;
  try {
    text = await readFile(settingsPath(dir), 'utf8');
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return { settings: normalizeSettings({}) };
    return { settings: normalizeSettings({}), problem: 'unreadable' };
  }
  try {
    return { settings: normalizeSettings(JSON.parse(text) as unknown) };
  } catch {
    return { settings: normalizeSettings({}), problem: 'invalid-json' };
  }
}

export async function readSettings(dir: string): Promise<Settings> {
  return (await readSettingsFile(dir)).settings;
}

/** Atomic (temporary file, then rename), 0600, in a 0700 folder. */
export async function writeSettings(dir: string, settings: Settings): Promise<void> {
  await mkdir(dir, { recursive: true, mode: 0o700 });
  const target = settingsPath(dir);
  const temporary = `${target}.${process.pid}.tmp`;
  await writeFile(temporary, `${JSON.stringify(normalizeSettings(settings), null, 2)}\n`, { mode: 0o600 });
  await rename(temporary, target);
}

export type SettingsPatch = { [K in keyof Settings]?: Settings[K] extends Record<string, unknown> ? Partial<Settings[K]> : Settings[K] };

/** Merges a patch one level deep (the sections), keeping everything else, and writes the result. */
export async function updateSettings(dir: string, patch: SettingsPatch): Promise<Settings> {
  const current = await readSettings(dir);
  const merged: Record<string, unknown> = { ...current };
  for (const [key, value] of Object.entries(patch)) {
    merged[key] = isObject(value) && isObject(current[key]) ? { ...(current[key] as Record<string, unknown>), ...value } : value;
  }
  const next = normalizeSettings(merged);
  await writeSettings(dir, next);
  return next;
}
