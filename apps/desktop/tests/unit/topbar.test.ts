// The top bar's view model and the menu template (docs/specs/desktop.md 3.4).
import { describe, expect, it, vi } from 'vitest';
import { ABOUT, NOT_AVAILABLE, buildMenuTemplate } from '../../src/main/menu';
import { modeLabel, pillFor, selectedTab, stripFor, tabsFor } from '../../src/renderer/topbar/model';
import { STRINGS, copiedText } from '../../src/renderer/topbar/strings';
import type { DesktopStatus } from '../../src/shared/types';

const status = (over: Partial<DesktopStatus> = {}): DesktopStatus => ({
  version: '0.1.0',
  agent: 'up',
  mode: 'online',
  connectivity: 'full',
  alerts: 0,
  cloud: { linked: false, tokenHeld: false, url: 'https://ai.marin.cr' },
  connected: false,
  tab: 'local',
  ...over,
});

describe('tabs', () => {
  it('the Lab tab is hidden while not connected', () => {
    expect(tabsFor(null)).toEqual(['local']);
    expect(tabsFor(status())).toEqual(['local']);
    expect(tabsFor(status({ cloud: { linked: true, tokenHeld: true, url: 'x' }, connected: false }))).toEqual(['local']);
  });
  it('and offered in connected mode', () => {
    expect(tabsFor(status({ connected: true }))).toEqual(['local', 'lab']);
  });
  it('the selected tab is never lab while the tab is not offered', () => {
    expect(selectedTab(status({ tab: 'lab', connected: false }))).toBe('local');
    expect(selectedTab(status({ tab: 'lab', connected: true }))).toBe('lab');
    expect(selectedTab(null)).toBe('local');
  });
});

describe('status pill and mode chip', () => {
  it.each([
    [null, 'busy', 'Starting'],
    [status({ agent: 'starting' }), 'busy', 'Starting'],
    [status(), 'ok', 'Ready'],
    [status({ alerts: 2 }), 'warn', 'Needs attention'],
    [status({ agent: 'down' }), 'bad', 'Not working'],
    [status({ agent: 'down', alerts: 3 }), 'bad', 'Not working'],
    [status({ agent: 'unauthorized' }), 'bad', 'Sign-in needed'],
  ] as const)('%#: %s', (input, tone, label) => {
    expect(pillFor(input, 'en')).toEqual({ tone, label });
  });

  it('speaks Spanish', () => {
    expect(pillFor(status(), 'es').label).toBe('Listo');
    expect(modeLabel('survival', 'es')).toBe('Supervivencia');
    expect(modeLabel('offline', 'es')).toBe('Sin conexión');
  });

  it('mode labels', () => {
    expect(modeLabel('online', 'en')).toBe('Online');
    expect(modeLabel(null, 'en')).toBeNull();
  });

  it('every string exists in both languages', () => {
    for (const [name, text] of Object.entries(STRINGS)) {
      expect(text.en, name).not.toBe('');
      expect(text.es, name).not.toBe('');
    }
    expect(copiedText(60, 'es')).toContain('60 segundos');
    expect(copiedText(60, 'en')).toContain('60 seconds');
  });
});

describe('the menu (the three dots)', () => {
  const actions = () => ({ connectionInfo: vi.fn(), ensure: vi.fn(), connect: vi.fn(), disconnect: vi.fn(), setAutostart: vi.fn(), setUpdates: vi.fn(), about: vi.fn(), quit: vi.fn() });
  const state = { lang: 'en' as const, linked: false, autostart: true, checkForUpdates: false };

  it('lists the spec items in order', () => {
    const items = buildMenuTemplate(state, actions());
    expect(items.map((i) => i.label ?? '---')).toEqual(['Connection info', "Install what's missing", 'Connect to the lab', '---', 'Start at login', 'Check for updates', '---', 'About', 'Quit']);
  });

  it('offers Disconnect instead of Connect when the lab is linked', () => {
    const labels = buildMenuTemplate({ ...state, linked: true }, actions()).map((i) => i.label);
    expect(labels).toContain('Disconnect from the lab');
    expect(labels).not.toContain('Connect to the lab');
  });

  it('shows Start at login and Check for updates as ticked or not, and a click flips them', () => {
    const a = actions();
    const items = buildMenuTemplate(state, a);
    const login = items.find((i) => i.label === 'Start at login')!;
    const updates = items.find((i) => i.label === 'Check for updates')!;
    expect([login.type, login.checked, updates.type, updates.checked]).toEqual(['checkbox', true, 'checkbox', false]);
    login.click!();
    updates.click!();
    expect(a.setAutostart).toHaveBeenCalledWith(false);
    expect(a.setUpdates).toHaveBeenCalledWith(true);
  });

  it('routes the other items to their actions', () => {
    const a = actions();
    const items = buildMenuTemplate(state, a);
    for (const label of ['Connection info', "Install what's missing", 'Connect to the lab', 'About', 'Quit']) items.find((i) => i.label === label)!.click!();
    expect([a.connectionInfo, a.ensure, a.connect, a.about, a.quit].map((f) => f.mock.calls.length)).toEqual([1, 1, 1, 1, 1]);
  });

  it('is in Spanish when asked', () => {
    const labels = buildMenuTemplate({ ...state, lang: 'es' }, actions()).map((i) => i.label ?? '---');
    expect(labels).toEqual(['Información de conexión', 'Instalar lo que falta', 'Conectar con el laboratorio', '---', 'Iniciar al abrir sesión', 'Buscar actualizaciones', '---', 'Acerca de', 'Salir']);
    expect(NOT_AVAILABLE.es).toContain('Todavía');
    expect(ABOUT.detail('0.1.0', 'es')).toContain('Versión 0.1.0');
  });
});

describe('the survival strip (desktop.md 4.7)', () => {
  const survival = (over: Partial<NonNullable<DesktopStatus['survival']>> = {}) => status({ mode: 'survival', survival: { active: true, fullScreen: true, portalOnly: false, banner: null, ...over } });

  it('is off until the main process says the Portal is in survival form', () => {
    expect(stripFor(null, 'en').active).toBe(false);
    expect(stripFor(status(), 'en').active).toBe(false);
    expect(stripFor(status({ mode: 'survival' }), 'en').active).toBe(false);
    expect(stripFor(status({ survival: { active: false, fullScreen: false, portalOnly: false, banner: null } }), 'en').active).toBe(false);
  });

  it('says "Survival mode" / "Modo supervivencia" and offers Exit full screen while full screen', () => {
    expect(stripFor(survival(), 'en')).toEqual({ active: true, label: 'Survival mode', fullScreenLabel: 'Exit full screen', showBack: false, banner: null });
    expect(stripFor(survival(), 'es')).toMatchObject({ label: 'Modo supervivencia', fullScreenLabel: 'Salir de pantalla completa' });
  });

  it('after leaving full screen the button goes back in (nobody is stuck either way)', () => {
    expect(stripFor(survival({ fullScreen: false }), 'en').fullScreenLabel).toBe('Full screen');
    expect(stripFor(survival({ fullScreen: false }), 'es').fullScreenLabel).toBe('Pantalla completa');
  });

  it('a Portal opened outside survival mode is not called survival mode and has a way back', () => {
    expect(stripFor(survival({ portalOnly: true }), 'en')).toMatchObject({ label: 'Survival Portal', showBack: true });
    expect(stripFor(survival({ portalOnly: true }), 'es').label).toBe('Portal de supervivencia');
  });

  it('shows the token-file code as a small banner', () => {
    expect(stripFor(survival({ banner: { code: 'LAI-DSK-001', text: 'x' } }), 'en').banner).toBe('LAI-DSK-001 · x');
  });
});
