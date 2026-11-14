// Corona (ADR-0014) in the shell pages: the MUI theme is dark and takes its colours from packages/ui/brand/tokens.json,
// the window background and the tray colours match the tokens, and every text pair the pages use reaches 4.5:1.
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { getContrastRatio } from '@mui/material/styles';
import { describe, expect, it } from 'vitest';
import { TRAY_COLOURS } from '../../scripts/icons.mjs';
import { WINDOW_BACKGROUND } from '../../src/main/windows';
import { TOPBAR_BG, coronaTheme } from '../../src/renderer/shared/theme';
import { PILL_MARK } from '../../src/renderer/topbar/model';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const tokens = JSON.parse(readFileSync(join(root, '..', '..', 'packages/ui/brand/tokens.json'), 'utf8')) as {
  font: string;
  color: Record<string, string>;
  tray: Record<string, string>;
  radius: Record<string, number>;
};
const c = tokens.color;
const p = coronaTheme.palette;

describe('the MUI theme', () => {
  it('is dark', () => {
    expect(p.mode).toBe('dark');
  });

  it('takes its palette from tokens.json', () => {
    expect(p.background).toMatchObject({ default: c['bg'], paper: c['surface'] });
    expect(p.text).toMatchObject({ primary: c['text'], secondary: c['textMuted'] });
    expect(p.divider).toBe(c['line']);
    expect(p.primary).toMatchObject({ main: c['gold'], contrastText: c['onGold'] });
    expect(p.success.main).toBe(c['ok']);
    expect(p.warning.main).toBe(c['warn']);
    expect(p.error.main).toBe(c['bad']);
    expect(p.info.main).toBe(c['info']);
  });

  it('uses the control radius and the bundled font', () => {
    expect(coronaTheme.shape.borderRadius).toBe(tokens.radius['control']);
    expect(coronaTheme.typography.fontFamily).toContain(tokens.font);
  });
});

describe('the main process and the icons', () => {
  it('opens every window on the Corona background (no white flash)', () => {
    expect(WINDOW_BACKGROUND).toBe(c['bg']);
  });

  it('npm run icons draws the tray in the tokens.json tray colours', () => {
    expect(TRAY_COLOURS).toEqual(tokens.tray);
  });
});

describe('accessibility (ADR-0014: text >= 4.5:1, status never colour alone)', () => {
  const AA = 4.5;
  it.each([
    ['text on bg', c['text'], c['bg']],
    ['text on surface', c['text'], c['surface']],
    ['text on surface2 (dialogs, inputs)', c['text'], c['surface2']],
    ['text on raised (default chips)', c['text'], c['raised']],
    ['text on the top bar', c['text'], TOPBAR_BG],
    ['muted text on bg', c['textMuted'], c['bg']],
    ['muted text on surface', c['textMuted'], c['surface']],
    ['muted text on surface2', c['textMuted'], c['surface2']],
    ['muted text on the top bar (tabs, notice)', c['textMuted'], TOPBAR_BG],
    ['dim text on the top bar (the Music button while unavailable)', c['textDim'], TOPBAR_BG],
    ['the survival label on the strip', c['warn'], TOPBAR_BG],
    ['onGold on gold (the main action)', c['onGold'], c['gold']],
    ['onGold on goldHover', c['onGold'], c['goldHover']],
    ['text on okBg', c['text'], c['okBg']],
    ['text on warnBg', c['text'], c['warnBg']],
    ['text on badBg', c['text'], c['badBg']],
    ['text on infoBg', c['text'], c['infoBg']],
    ['bg on ok (success chip)', p.success.contrastText, c['ok']],
    ['bg on warn (warning chip)', p.warning.contrastText, c['warn']],
    ['bg on bad (error chip)', p.error.contrastText, c['bad']],
    ['bg on info', p.info.contrastText, c['info']],
    ['warn text (outlined warning button) on surface', c['warn'], c['surface']],
  ])('%s', (_name, fg, bg) => {
    expect(getContrastRatio(fg!, bg!)).toBeGreaterThanOrEqual(AA);
  });

  it('the focus ring (gold) stands out from every surface by >= 3:1', () => {
    for (const bg of [c['bg'], c['surface'], c['surface2'], TOPBAR_BG]) expect(getContrastRatio(c['focus']!, bg!)).toBeGreaterThanOrEqual(3);
  });

  it('every pill tone carries its own mark beside its colour', () => {
    const marks = Object.values(PILL_MARK);
    expect(Object.keys(PILL_MARK).sort()).toEqual(['bad', 'busy', 'ok', 'warn']);
    expect(new Set(marks).size).toBe(marks.length);
  });
});
