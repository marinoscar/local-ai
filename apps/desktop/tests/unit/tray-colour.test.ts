import { describe, expect, it } from 'vitest';
import { NO_INTERNET_MS, readStatus, trayColour, type AlertLike, type TrayStatusInput } from '../../src/main/tray';
import { TRAY_COLOURS, TRAY_TOOLTIPS } from '../../src/main/i18n';

const NOW = Date.parse('2026-11-02T15:00:00Z');
const ago = (ms: number): string => new Date(NOW - ms).toISOString();
const up = (mode: string | undefined, modelFailed = false): TrayStatusInput => ({ link: 'up', mode, modelFailed });
const warning: AlertLike = { code: 'LAI-HW-012', severity: 'warning' };
const critical: AlertLike = { code: 'LAI-HW-001', severity: 'critical' };
const info: AlertLike = { code: 'LAI-SVC-013', severity: 'info' };

interface Row {
  name: string;
  status: TrayStatusInput | null;
  alerts?: AlertLike[];
  connectivity?: string;
  noneSince?: string | null;
  expected: (typeof TRAY_COLOURS)[number];
}

// Every row of the table of desktop.md 4.3, then the precedence cases.
const rows: Row[] = [
  { name: 'green: agent up, online, no alerts', status: up('online'), expected: 'green' },
  { name: 'green: an info alert does not count', status: up('online'), alerts: [info], expected: 'green' },
  { name: 'green: connectivity none for 29 min is not yet a problem', status: up('online'), connectivity: 'none', noneSince: ago(NO_INTERNET_MS - 60_000), expected: 'green' },
  { name: 'green: connectivity full', status: up('online'), connectivity: 'full', noneSince: null, expected: 'green' },
  { name: 'blue: offline', status: up('offline'), expected: 'blue' },
  { name: 'blue: offline with connectivity none for an hour (not online, so not a problem)', status: up('offline'), connectivity: 'none', noneSince: ago(3_600_000), expected: 'blue' },
  { name: 'orange: survival', status: up('survival'), expected: 'orange' },
  { name: 'orange: survival with connectivity none for an hour', status: up('survival'), connectivity: 'none', noneSince: ago(3_600_000), expected: 'orange' },
  { name: 'amber: a warning alert', status: up('online'), alerts: [warning], expected: 'amber' },
  { name: 'amber: an error alert needs attention too', status: up('online'), alerts: [{ severity: 'error' }], expected: 'amber' },
  { name: 'amber: a model failed to load', status: up('online', true), expected: 'amber' },
  { name: 'amber: connectivity none for exactly 30 min while online', status: up('online'), connectivity: 'none', noneSince: ago(NO_INTERNET_MS), expected: 'amber' },
  { name: 'amber: connectivity none for 2 h while online', status: up('online'), connectivity: 'none', noneSince: ago(2 * 3_600_000), expected: 'amber' },
  { name: 'green: connectivity none but no none_since', status: up('online'), connectivity: 'none', noneSince: null, expected: 'green' },
  { name: 'green: none_since is not a date', status: up('online'), connectivity: 'none', noneSince: 'yesterday', expected: 'green' },
  { name: 'red: agent unreachable', status: { link: 'down', mode: 'online', modelFailed: false }, expected: 'red' },
  { name: 'red: agent up but unusable by this app', status: { link: 'blocked', mode: undefined, modelFailed: false }, expected: 'red' },
  { name: 'red: a critical alert', status: up('online'), alerts: [critical], expected: 'red' },
  { name: 'grey: starting', status: { link: 'starting', mode: undefined, modelFailed: false }, expected: 'grey' },
  { name: 'grey: nothing known yet', status: null, expected: 'grey' },
  { name: 'grey: reconnecting wins over an old critical alert and survival', status: { link: 'starting', mode: 'survival', modelFailed: false }, alerts: [critical], expected: 'grey' },
  { name: 'grey: a mode this version does not know', status: up('hibernating'), expected: 'grey' },
  { name: 'grey: up but the mode is not known', status: up(undefined), expected: 'grey' },
  // Precedence: red > orange > amber > blue > green
  { name: 'precedence: survival + critical alert is red', status: up('survival'), alerts: [critical], expected: 'red' },
  { name: 'precedence: survival + warning is orange', status: up('survival'), alerts: [warning], expected: 'orange' },
  { name: 'precedence: survival + failed model is orange', status: up('survival', true), expected: 'orange' },
  { name: 'precedence: offline + warning is amber', status: up('offline'), alerts: [warning], expected: 'amber' },
  { name: 'precedence: offline + failed model is amber', status: up('offline', true), expected: 'amber' },
  { name: 'precedence: offline + critical is red', status: up('offline'), alerts: [warning, critical], expected: 'red' },
  { name: 'precedence: online + warning + long outage is amber', status: up('online'), alerts: [warning], connectivity: 'none', noneSince: ago(NO_INTERNET_MS * 2), expected: 'amber' },
  { name: 'precedence: down + survival is red', status: { link: 'down', mode: 'survival', modelFailed: false }, expected: 'red' },
];

describe('trayColour', () => {
  it.each(rows)('$name', ({ status, alerts = [], connectivity, noneSince, expected }) => {
    expect(trayColour(status, alerts, connectivity, noneSince, NOW)).toBe(expected);
  });

  it('every colour of the icon set is produced by some row, and has a tooltip in both languages', () => {
    expect(new Set(rows.map((r) => r.expected))).toEqual(new Set(TRAY_COLOURS));
    for (const colour of TRAY_COLOURS) {
      expect(TRAY_TOOLTIPS[colour].en).toMatch(/^Local AI: /);
      expect(TRAY_TOOLTIPS[colour].es).toMatch(/^Local AI: /);
      expect(TRAY_TOOLTIPS[colour].es).not.toBe(TRAY_TOOLTIPS[colour].en);
    }
  });

  it('keeps the tooltips of the spec word for word', () => {
    expect(TRAY_TOOLTIPS.green).toEqual({ en: 'Local AI: ready (online)', es: 'Local AI: listo (en línea)' });
    expect(TRAY_TOOLTIPS.blue).toEqual({ en: 'Local AI: ready (offline)', es: 'Local AI: listo (sin conexión)' });
    expect(TRAY_TOOLTIPS.orange).toEqual({ en: 'Local AI: survival mode', es: 'Local AI: modo supervivencia' });
    expect(TRAY_TOOLTIPS.amber).toEqual({ en: 'Local AI: needs attention', es: 'Local AI: necesita atención' });
    expect(TRAY_TOOLTIPS.red).toEqual({ en: 'Local AI: not working, click for help', es: 'Local AI: no funciona, haz clic para ayuda' });
    expect(TRAY_TOOLTIPS.grey).toEqual({ en: 'Local AI: starting', es: 'Local AI: iniciando' });
  });

  it('time alone moves the light: the same status turns amber when 30 minutes have passed', () => {
    const since = ago(10 * 60_000);
    expect(trayColour(up('online'), [], 'none', since, NOW)).toBe('green');
    expect(trayColour(up('online'), [], 'none', since, NOW + 20 * 60_000)).toBe('amber');
  });
});

describe('readStatus', () => {
  it('reads mode, alerts, connectivity and the model of a /status body', () => {
    const read = readStatus({
      mode: { value: 'online' },
      alerts: [{ code: 'LAI-HW-012', severity: 'warning', title: 'x' }, { nonsense: true }, 'junk'],
      connectivity: { value: 'none', none_since: '2026-11-02T14:00:00Z' },
      models: { default: 'model-a', loaded: ['model-b'] },
    } as never);
    expect(read).toEqual({ mode: 'online', alerts: [{ code: 'LAI-HW-012', severity: 'warning' }, {}], connectivity: 'none', noneSince: '2026-11-02T14:00:00Z', model: 'model-b' });
  });

  it('falls back to the default model, and survives an empty or damaged body', () => {
    expect(readStatus({ models: { default: 'model-a', loaded: [] } } as never).model).toBe('model-a');
    expect(readStatus(null)).toEqual({ mode: undefined, alerts: [], connectivity: undefined, noneSince: null, model: null });
    expect(readStatus({ mode: 'online', alerts: 'none', models: 5, connectivity: [] } as never)).toEqual({ mode: undefined, alerts: [], connectivity: undefined, noneSince: null, model: null });
  });
});
