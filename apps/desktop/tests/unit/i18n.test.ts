import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { ACTION_LABEL, ERROR_ACTIONS, ERROR_CODES, ERROR_COPY, PAGE_STRINGS, START_FAILURE_TEXT, errorText, isDskCode, isStartFailureReason, resolveLang } from '../../src/main/i18n';

const registry = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', '..', 'docs', 'specs', 'doctor-codes.md'), 'utf8');

describe('language', () => {
  it('auto follows the OS locale: any es_* is Spanish', () => {
    for (const locale of ['es', 'es-CR', 'es_MX.UTF-8', 'ES_ES']) expect(resolveLang('auto', locale), locale).toBe('es');
    for (const locale of ['en-US', 'en_GB.UTF-8', 'fr', 'esperanto', '', undefined]) expect(resolveLang('auto', locale), String(locale)).toBe('en');
  });
  it('an explicit setting wins over the locale', () => {
    expect(resolveLang('en', 'es_CR')).toBe('en');
    expect(resolveLang('es', 'en_US')).toBe('es');
  });
});

describe('error page text', () => {
  it.each(ERROR_CODES)('%s is registered, has both languages, a fix and the Portal button', (code) => {
    expect(registry).toMatch(new RegExp(`^\\| ${code} \\|`, 'm'));
    const copy = ERROR_COPY[code];
    for (const field of [copy.title, copy.message, copy.fix]) {
      expect(field.en.length).toBeGreaterThan(10);
      expect(field.es.length).toBeGreaterThan(10);
      expect(field.es).not.toBe(field.en);
    }
    expect(copy.actions).toContain('open-portal');
    for (const action of copy.actions) {
      expect(ERROR_ACTIONS).toContain(action);
      expect(ACTION_LABEL[action].es).not.toBe(ACTION_LABEL[action].en);
    }
    expect(errorText(code, 'es').fix).toBe(copy.fix.es);
  });

  it('a button fixes 001 and 007 (the typed command stays second); the agent-down pages have the two fallbacks', () => {
    expect(ERROR_COPY['LAI-DSK-001'].actions[0]).toBe('allow-user');
    expect(ERROR_COPY['LAI-DSK-001'].followUp).toEqual(['logout']);
    expect(ERROR_COPY['LAI-DSK-007'].actions[0]).toBe('rotate-token');
    expect(ERROR_COPY['LAI-DSK-017'].actions[0]).toBe('repair-access');
    expect(ERROR_COPY['LAI-DSK-017'].actions).not.toContain('allow-user');
    expect(ERROR_COPY['LAI-DSK-005'].actions[0]).toBe('start-agent');
    for (const code of ['LAI-DSK-003', 'LAI-DSK-004', 'LAI-DSK-005'] as const) {
      expect(ERROR_COPY[code].actions).toContain('open-library');
      expect(ERROR_COPY[code].actions).toContain('restart-computer');
    }
    // The page's own fix sentence names the button, not a terminal.
    expect(ERROR_COPY['LAI-DSK-001'].fix.en).toContain('"Allow this user"');
    expect(ERROR_COPY['LAI-DSK-001'].fix.es).toContain('"Permitir este usuario"');
    expect(ERROR_COPY['LAI-DSK-007'].fix.en).toContain('"Make a new token"');
    expect(ERROR_COPY['LAI-DSK-007'].fix.es).toContain('"Crear un token nuevo"');
    for (const code of ['LAI-DSK-001', 'LAI-DSK-005', 'LAI-DSK-007'] as const) {
      expect(ERROR_COPY[code].fix.en, code).not.toMatch(/terminal|sudo/);
      expect(ERROR_COPY[code].fix.es, code).not.toMatch(/terminal|sudo/);
    }
  });

  it('every "Start agent" failure reason has plain words in both languages', () => {
    for (const reason of ['not-authorized', 'pkexec-failed', 'pkexec-unavailable', 'start-failed', 'timeout']) {
      expect(isStartFailureReason(reason), reason).toBe(true);
      const t = START_FAILURE_TEXT[reason as keyof typeof START_FAILURE_TEXT];
      for (const field of [t.what, t.next]) {
        expect(field.en.length).toBeGreaterThan(10);
        expect(field.es).not.toBe(field.en);
        expect(field.en).not.toMatch(/terminal|sudo/);
      }
    }
    expect(isStartFailureReason('toString')).toBe(false);
  });

  it('every new page string exists in both languages', () => {
    for (const [key, value] of Object.entries(PAGE_STRINGS)) {
      expect(value.en.length, key).toBeGreaterThan(1);
      expect(value.es.length, key).toBeGreaterThan(1);
    }
  });

  it('the buttons of the spec are on the right pages', () => {
    expect(ERROR_COPY['LAI-DSK-001'].actions).toContain('copy-command');
    expect(ERROR_COPY['LAI-DSK-001'].command).toBe('sudo usermod -aG localai-operators $USER');
    expect(ERROR_COPY['LAI-DSK-002'].actions).toContain('logout');
    expect(ERROR_COPY['LAI-DSK-003'].actions).toContain('start-agent');
    expect(ERROR_COPY['LAI-DSK-004'].actions).toContain('start-agent');
    expect(ERROR_COPY['LAI-DSK-007'].actions).toContain('open-browser');
    expect(ERROR_COPY['LAI-DSK-007'].command).toBe('sudo aictl token rotate');
    expect(PAGE_STRINGS.browserHint.en).toContain('aictl ui-code');
  });

  it('keeps the exact texts of the spec for 001 and 002', () => {
    expect(ERROR_COPY['LAI-DSK-001'].message.en).toBe('This user is not allowed to control Local AI yet.');
    expect(ERROR_COPY['LAI-DSK-002'].message.en).toBe('You were added to the Local AI operators group, but you need to log out and log back in once.');
    expect(ERROR_COPY['LAI-DSK-002'].message.es).toBe('Te agregaron al grupo de operadores de Local AI, pero debes cerrar sesión y volver a entrar una vez.');
  });

  it('isDskCode only accepts the known codes', () => {
    expect(isDskCode('LAI-DSK-001')).toBe(true);
    expect(isDskCode('not-a-code')).toBe(false);
    expect(isDskCode(undefined)).toBe(false);
  });
});
