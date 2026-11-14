import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { BACKGROUND_COLOR, THEME_COLOR } from '@app/shared';

import { APP_THEMES, CORONA, LIGHT_GOLD, THEME_STORAGE_KEY, seedDarkDefault } from '../theme';

// packages/ui/brand/tokens.json through the @localai/ui link (apps/cloud/packages/localai-ui -> packages/ui).
const TOKENS_PATH = join(process.cwd(), '..', '..', 'packages', 'localai-ui', 'brand', 'tokens.json');
const ICON_PATH = join(process.cwd(), 'public', 'favicon.svg');

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5]
    .map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!;
}
function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

describe('the Corona theme (ADR-0014)', () => {
  it('uses the brand tokens of packages/ui/brand/tokens.json', () => {
    const tokens = JSON.parse(readFileSync(TOKENS_PATH, 'utf8')) as { color: Record<string, string> };
    for (const [name, value] of Object.entries(CORONA)) expect([name, value.toLowerCase()]).toEqual([name, tokens.color[name]!.toLowerCase()]);
  });

  it('is gold on near-black in dark mode', () => {
    const { palette } = APP_THEMES.dark;
    expect(palette.mode).toBe('dark');
    expect(palette.primary.main).toBe(CORONA.gold);
    expect(palette.primary.contrastText).toBe(CORONA.onGold);
    expect(palette.background.default).toBe(CORONA.bg);
    expect(palette.background.paper).toBe(CORONA.surface);
    expect(palette.text.primary).toBe(CORONA.text);
  });

  it('keeps every text colour at 4.5:1 or more on the surfaces it sits on', () => {
    const dark = APP_THEMES.dark.palette;
    for (const bg of [dark.background.default, dark.background.paper]) {
      for (const fg of [dark.text.primary, dark.text.secondary, dark.primary.main, dark.success.main, dark.warning.main, dark.error.main, dark.info.main]) {
        expect(contrast(fg, bg)).toBeGreaterThanOrEqual(4.5);
      }
    }
    expect(contrast(dark.primary.contrastText, dark.primary.main)).toBeGreaterThanOrEqual(4.5);
    const light = APP_THEMES.light.palette;
    expect(light.primary.main).toBe(LIGHT_GOLD);
    for (const bg of ['#ffffff', '#f5f5f5']) expect(contrast(LIGHT_GOLD, bg)).toBeGreaterThanOrEqual(4.5);
    expect(contrast(light.primary.contrastText, LIGHT_GOLD)).toBeGreaterThanOrEqual(4.5);
  });

  it('paints the browser and PWA chrome near-black, not gold', () => {
    expect(THEME_COLOR.toLowerCase()).toBe(CORONA.bg.toLowerCase());
    expect(BACKGROUND_COLOR.toLowerCase()).toBe(CORONA.bg.toLowerCase());
  });

  it('ships the eclipse icon, with the plate scripts/rename.mjs rewrites', () => {
    const svg = readFileSync(ICON_PATH, 'utf8');
    expect(svg).toContain('#F2B544');
    expect(svg.split(`fill="${THEME_COLOR}"`).length - 1).toBe(1);
  });
});

describe('the dark default', () => {
  function memoryStorage(initial?: string) {
    const map = new Map<string, string>(initial === undefined ? [] : [[THEME_STORAGE_KEY, initial]]);
    return { getItem: (k: string) => map.get(k) ?? null, setItem: (k: string, v: string) => void map.set(k, v), map };
  }

  it('stores dark for a first visit', () => {
    const storage = memoryStorage();
    seedDarkDefault(storage);
    expect(storage.map.get(THEME_STORAGE_KEY)).toBe('dark');
  });

  it.each(['light', 'system', 'dark'])('never overwrites a stored choice (%s)', (choice) => {
    const storage = memoryStorage(choice);
    seedDarkDefault(storage);
    expect(storage.map.get(THEME_STORAGE_KEY)).toBe(choice);
  });

  it('survives blocked storage', () => {
    const blocked = { getItem: () => { throw new Error('blocked'); }, setItem: () => { throw new Error('blocked'); } };
    expect(() => seedDarkDefault(blocked)).not.toThrow();
  });
});
