import { describe, expect, it } from 'vitest';
import { ACTION_LABEL, PAGE_STRINGS } from '../../src/main/i18n';
import { ALWAYS_USABLE, SECONDARY_ACTIONS, errorPageLayout, fixFailureNote } from '../../src/renderer/error/model';

// desktop.md 4.2 "Buttons instead of commands": the error page leads with the button; the typed command is second,
// folded under "Use a terminal instead".

describe('error page layout', () => {
  it('LAI-DSK-001 leads with "Allow this user"; the usermod command goes under "Use a terminal instead"', () => {
    expect(errorPageLayout('LAI-DSK-001')).toEqual({ buttons: ['allow-user', 'open-portal'], leadCommand: null, terminalCommand: 'sudo usermod -aG localai-operators $USER' });
  });

  it('LAI-DSK-017 leads with "Fix access"; the chmod command goes under "Use a terminal instead"', () => {
    expect(errorPageLayout('LAI-DSK-017')).toEqual({ buttons: ['repair-access', 'open-portal', 'restart-computer'], leadCommand: null, terminalCommand: 'sudo chmod o+x /etc/localai' });
    expect(ACTION_LABEL['repair-access']).toEqual({ en: 'Fix access', es: 'Reparar el acceso' });
  });

  it('LAI-DSK-007 leads with "Make a new token", keeps Open in browser and the Portal', () => {
    expect(errorPageLayout('LAI-DSK-007')).toEqual({ buttons: ['rotate-token', 'open-browser', 'open-portal'], leadCommand: null, terminalCommand: 'sudo aictl token rotate' });
  });

  it('the agent-down pages offer Start agent first, then the library only and a restart', () => {
    for (const code of ['LAI-DSK-003', 'LAI-DSK-004', 'LAI-DSK-005']) {
      const { buttons, leadCommand } = errorPageLayout(code);
      expect(buttons[0], code).toBe('start-agent');
      expect(buttons, code).toContain('open-library');
      expect(buttons, code).toContain('restart-computer');
      expect(leadCommand, code).toBeNull();
    }
    expect(errorPageLayout('LAI-DSK-005').terminalCommand).toBe('sudo aictl doctor');
  });

  it('a page whose only fix is a command still shows it first (LAI-DSK-006, LAI-DSK-015)', () => {
    expect(errorPageLayout('LAI-DSK-006')).toEqual({ buttons: ['copy-command', 'open-portal'], leadCommand: 'sudo aictl update', terminalCommand: null });
    expect(errorPageLayout('LAI-DSK-015').leadCommand).toContain('sudo apt install');
  });

  it('an unknown code shows only the Portal', () => {
    expect(errorPageLayout('nope')).toEqual({ buttons: ['open-portal'], leadCommand: null, terminalCommand: null });
    expect(errorPageLayout(null).buttons).toEqual(['open-portal']);
  });

  it('the fallbacks are outlined, the ways out stay usable while a password window is open, never Restart', () => {
    expect(SECONDARY_ACTIONS).toEqual(expect.arrayContaining(['open-library', 'restart-computer', 'open-portal']));
    expect(ALWAYS_USABLE).not.toContain('restart-computer');
    expect(ALWAYS_USABLE).not.toContain('allow-user');
  });

  it('labels in both languages', () => {
    expect(ACTION_LABEL['allow-user']).toEqual({ en: 'Allow this user', es: 'Permitir este usuario' });
    expect(ACTION_LABEL['rotate-token']).toEqual({ en: 'Make a new token', es: 'Crear un token nuevo' });
    expect(ACTION_LABEL['open-library']).toEqual({ en: 'Open the library only', es: 'Abrir solo la biblioteca' });
    expect(ACTION_LABEL['restart-computer']).toEqual({ en: 'Restart the computer', es: 'Reiniciar la computadora' });
    expect(PAGE_STRINGS.terminalSummary.es).toBe('Usar una terminal en su lugar');
  });

  it('the failure notes of a privileged button', () => {
    expect(fixFailureNote({ ok: false, reason: 'cancelled' })).toBe(PAGE_STRINGS.fixCancelled);
    expect(fixFailureNote({ ok: false, reason: 'unavailable' })).toBe(PAGE_STRINGS.fixUnavailable);
    expect(fixFailureNote({ ok: false, reason: 'failed' })).toBe(PAGE_STRINGS.fixFailed);
    expect(fixFailureNote({ ok: false, busy: true })).toBeNull();
  });
});
