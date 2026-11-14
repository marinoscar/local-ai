import { describe, expect, it, vi } from 'vitest';
import {
  type ElectronTrayApi,
  type MenuFeatures,
  type MenuItemSpec,
  type MenuModel,
  NO_FEATURES,
  buildTrayMenu,
  createElectronTrayPort,
  toMenuTemplate,
  trayIconPath,
} from '../../src/main/tray';
import { TRAY_COLOURS } from '../../src/main/i18n';

const model = (over: Partial<MenuModel> = {}): MenuModel => ({ colour: 'green', link: 'up', mode: 'online', model: 'qwen3.5-9b-q4km', features: NO_FEATURES, ...over });
const ALL: MenuFeatures = { windows: true, copyKey: true, connectionInfo: true, lab: true, startAgent: true, autostart: true, pin: true };

/** The menu as the plain lines a person reads, so a table can compare it with the spec. */
function lines(items: readonly MenuItemSpec[], indent = ''): string[] {
  return items.flatMap((item) => {
    switch (item.type) {
      case 'separator':
        return [`${indent}--`];
      case 'label':
        return [`${indent}${item.label}`];
      case 'action':
        return [`${indent}${item.label}${item.enabled ? '' : ' (disabled)'}`];
      case 'radio':
        return [`${indent}${item.checked ? '(•)' : '( )'} ${item.label}${item.enabled ? '' : ' (disabled)'}`];
      case 'check':
        return [`${indent}${item.checked ? '✓' : '☐'} ${item.label}`];
      case 'submenu':
        return [`${indent}${item.label} ▸`, ...lines(item.items, `${indent}  `)];
    }
  });
}

describe('tray menu (desktop.md 4.3)', () => {
  it('online with every optional item: the menu of the spec, line by line', () => {
    expect(lines(buildTrayMenu(model({ features: ALL }), 'en'))).toEqual([
      '● Local AI: ready (online) · qwen3.5-9b-q4km',
      '--',
      'Open Local AI',
      'Chat',
      'Library',
      'Maps',
      '--',
      'Mode ▸',
      '  (•) Online',
      '  ( ) Offline',
      '⚠ Survival mode',
      '--',
      'Copy API key',
      'Show connection info',
      'Lab (ai.marin.cr)',
      '--',
      '✓ Start at login',
      'Quit',
    ]);
  });

  it('the same menu in Spanish', () => {
    expect(lines(buildTrayMenu(model({ features: ALL }), 'es'))).toEqual([
      '● Local AI: listo (en línea) · qwen3.5-9b-q4km',
      '--',
      'Abrir Local AI',
      'Chat',
      'Biblioteca',
      'Mapas',
      '--',
      'Modo ▸',
      '  (•) En línea',
      '  ( ) Sin conexión',
      '⚠ Modo supervivencia',
      '--',
      'Copiar la clave de la API',
      'Mostrar datos de conexión',
      'Laboratorio (ai.marin.cr)',
      '--',
      '✓ Iniciar al entrar a la sesión',
      'Salir',
    ]);
  });

  it('with nothing optional landed yet: Open, Mode, Survival and Quit only', () => {
    expect(lines(buildTrayMenu(model(), 'en'))).toEqual([
      '● Local AI: ready (online) · qwen3.5-9b-q4km',
      '--',
      'Open Local AI',
      '--',
      'Mode ▸',
      '  (•) Online',
      '  ( ) Offline',
      '⚠ Survival mode',
      '--',
      'Quit',
    ]);
  });

  it('offline: the radio follows the mode and the header is blue', () => {
    const menu = lines(buildTrayMenu(model({ colour: 'blue', mode: 'offline' }), 'en'));
    expect(menu[0]).toBe('● Local AI: ready (offline) · qwen3.5-9b-q4km');
    expect(menu).toContain('  ( ) Online');
    expect(menu).toContain('  (•) Offline');
  });

  it('agent down: red header without a model, mode items and the agent-only items disabled, Start agent shown, Survival still enabled', () => {
    const menu = lines(buildTrayMenu(model({ colour: 'red', link: 'down', features: ALL }), 'en'));
    expect(menu[0]).toBe('● Local AI: not working, click for help');
    expect(menu).toContain('  (•) Online (disabled)');
    expect(menu).toContain('  ( ) Offline (disabled)');
    expect(menu).toContain('⚠ Survival mode');
    expect(menu).toContain('Copy API key (disabled)');
    expect(menu).toContain('Show connection info (disabled)');
    expect(menu).toContain('Chat (disabled)');
    expect(menu).toContain('Start agent');
    expect(menu).toContain('Open Local AI');
    expect(menu).toContain('Quit');
  });

  it('"Start agent" only when the agent is down, never when it is merely starting, blocked or up', () => {
    for (const link of ['up', 'starting', 'blocked'] as const) {
      expect(lines(buildTrayMenu(model({ link, features: ALL }), 'en'))).not.toContain('Start agent');
    }
    expect(lines(buildTrayMenu(model({ link: 'down', features: ALL }), 'en'))).toContain('Start agent');
    // ... and not at all when DSK-05 has not provided the action.
    expect(lines(buildTrayMenu(model({ link: 'down', features: { ...ALL, startAgent: false } }), 'en'))).not.toContain('Start agent');
  });

  it('survival with a PIN prompt: Online and Offline are enabled and neither is checked', () => {
    const menu = lines(buildTrayMenu(model({ colour: 'orange', mode: 'survival', features: { ...NO_FEATURES, pin: true } }), 'en'));
    expect(menu).toContain('  ( ) Online');
    expect(menu).toContain('  ( ) Offline');
    expect(menu.join('\n')).not.toContain('Survival Portal');
  });

  it('survival: Online and Offline are enabled with no PIN prompt in the build (ADR-0011)', () => {
    const menu = lines(buildTrayMenu(model({ colour: 'orange', mode: 'survival' }), 'en'));
    expect(menu).toContain('  ( ) Online');
    expect(menu).toContain('  ( ) Offline');
    expect(menu).not.toContain('  ( ) Online (disabled)');
    expect(menu).not.toContain('  Leave survival in the Survival Portal');
  });

  it('the Survival item is enabled in every state, with no PIN', () => {
    for (const link of ['up', 'starting', 'down', 'blocked'] as const) {
      for (const mode of ['online', 'offline', 'survival', undefined]) {
        const survival = buildTrayMenu(model({ link, mode }), 'en').find((i) => i.type === 'action' && i.action === 'survival');
        expect(survival, `${link}/${String(mode)}`).toMatchObject({ enabled: true });
      }
    }
  });

  it('the Lab item shows only when connected (the feature is false otherwise), and autostart shows its state', () => {
    expect(lines(buildTrayMenu(model({ features: { ...ALL, lab: false } }), 'en'))).not.toContain('Lab (ai.marin.cr)');
    expect(lines(buildTrayMenu(model({ features: { ...ALL, autostart: false } }), 'en'))).toContain('☐ Start at login');
    expect(lines(buildTrayMenu(model({ features: { ...ALL, autostart: null } }), 'en')).join('\n')).not.toContain('Start at login');
  });

  it('never puts two separators in a row or at the ends, whatever is left out', () => {
    for (const link of ['up', 'down', 'starting'] as const) {
      for (const features of [NO_FEATURES, ALL, { ...NO_FEATURES, copyKey: true }, { ...NO_FEATURES, autostart: false }]) {
        const menu = buildTrayMenu(model({ link, features }), 'en');
        expect(menu[0]?.type).not.toBe('separator');
        expect(menu.at(-1)?.type).not.toBe('separator');
        menu.forEach((item, i) => {
          if (item.type === 'separator') expect(menu[i + 1]?.type).not.toBe('separator');
        });
      }
    }
  });

  it('every colour has a header', () => {
    for (const colour of TRAY_COLOURS) expect(lines(buildTrayMenu(model({ colour }), 'en'))[0]).toMatch(/^● Local AI: /);
  });
});

describe('Electron adapter', () => {
  it('turns the spec into a template whose clicks report the action', () => {
    const onAction = vi.fn();
    const template = toMenuTemplate(buildTrayMenu(model({ features: ALL }), 'en'), onAction) as Array<Record<string, unknown>>;
    const walk = (items: Array<Record<string, unknown>>): Array<Record<string, unknown>> => items.flatMap((i) => [i, ...(Array.isArray(i['submenu']) ? walk(i['submenu'] as Array<Record<string, unknown>>) : [])]);
    const flat = walk(template);
    expect(flat.find((i) => i['label'] === '● Local AI: ready (online) · qwen3.5-9b-q4km')).toMatchObject({ enabled: false });
    expect(flat.find((i) => i['label'] === 'Offline')).toMatchObject({ type: 'radio', checked: false, enabled: true });
    expect(flat.find((i) => i['label'] === 'Start at login')).toMatchObject({ type: 'checkbox', checked: true });
    expect(flat.filter((i) => i['type'] === 'separator').length).toBeGreaterThan(0);
    (flat.find((i) => i['label'] === '⚠ Survival mode')!['click'] as () => void)();
    (flat.find((i) => i['label'] === 'Offline')!['click'] as () => void)();
    expect(onAction.mock.calls).toEqual([['survival'], ['mode:offline']]);
  });

  it('creates the tray on the first update, swaps the image only when the colour changes, and destroys it', () => {
    const calls: string[] = [];
    class FakeTray {
      constructor(image: unknown) {
        calls.push(`new ${String(image)}`);
      }
      setImage = (image: unknown): void => void calls.push(`image ${String(image)}`);
      setToolTip = (text: string): void => void calls.push(`tooltip ${text}`);
      setContextMenu = (menu: unknown): void => void calls.push(`menu ${JSON.stringify(menu)}`);
      on = (event: string): void => void calls.push(`on ${event}`);
      destroy = (): void => void calls.push('destroy');
    }
    const api: ElectronTrayApi = {
      Tray: FakeTray as never,
      Menu: { buildFromTemplate: (template) => ({ items: template.length }) },
      nativeImage: { createFromPath: (path) => path },
    };
    const port = createElectronTrayPort(api, { iconDir: '/res/tray', onAction: () => undefined, onActivate: () => undefined });
    const view = (colour: 'green' | 'red', tooltip: string) => ({ colour, tooltip, menu: [{ type: 'separator' as const }] });
    port.update(view('green', 'a'));
    port.update(view('green', 'b'));
    port.update(view('red', 'c'));
    port.destroy();
    expect(calls).toEqual([
      'new /res/tray/tray-green.png',
      'on click',
      'tooltip a',
      'menu {"items":1}',
      'tooltip b',
      'menu {"items":1}',
      'image /res/tray/tray-red.png',
      'tooltip c',
      'menu {"items":1}',
      'destroy',
    ]);
    expect(trayIconPath('/x', 'amber')).toBe('/x/tray-amber.png');
  });
});
