// The derived-health table of docs/specs/cloud-app.md section 4.5, over (EAB health x snapshot age x mode x phase),
// with the boundary values (120 s and 600 s are inclusive).
import type { NodeHealth } from '@marinoscar/platform-api/nodes';

import {
  ageSeconds,
  deriveHealth,
  isGatewayRoutable,
  isRoutable,
  type LabHealth,
  type LabMode,
  type LabPhase,
  type RoutableInput,
} from './derive-health';

const NOW = new Date('2026-11-03T12:00:00.000Z');
const ago = (seconds: number) => new Date(NOW.getTime() - seconds * 1000);

function health(eab: NodeHealth, ageS: number | null, mode: LabMode | null, phase: LabPhase): LabHealth {
  return deriveHealth({ eab, phase, mode, lastStateAt: ageS === null ? null : ago(ageS), now: NOW });
}

describe('deriveHealth', () => {
  it.each<[string, NodeHealth, number | null, LabMode | null, LabPhase, LabHealth]>([
    // healthy: EAB healthy and snapshot <= 120 s
    ['healthy EAB, fresh snapshot', 'healthy', 5, 'online', 'enrolled', 'healthy'],
    ['healthy EAB, snapshot at 0 s', 'healthy', 0, 'online', 'enrolled', 'healthy'],
    ['healthy EAB, snapshot at exactly 120 s (inclusive)', 'healthy', 120, 'online', 'enrolled', 'healthy'],
    // degraded: EAB not offline and snapshot <= 600 s
    ['healthy EAB, snapshot at 121 s', 'healthy', 121, 'online', 'enrolled', 'degraded'],
    ['healthy EAB, snapshot at exactly 600 s (inclusive)', 'healthy', 600, 'online', 'enrolled', 'degraded'],
    ['stale EAB, fresh snapshot', 'stale', 5, 'online', 'enrolled', 'degraded'],
    ['stale EAB, snapshot at 600 s', 'stale', 600, 'online', 'enrolled', 'degraded'],
    // down
    ['healthy EAB, snapshot at 601 s', 'healthy', 601, 'online', 'enrolled', 'down'],
    ['stale EAB, snapshot at 601 s', 'stale', 601, 'online', 'enrolled', 'down'],
    ['EAB offline, fresh snapshot', 'offline', 5, 'online', 'enrolled', 'down'],
    ['EAB offline, snapshot at 400 s', 'offline', 400, 'online', 'enrolled', 'down'],
    ['EAB offline, old snapshot', 'offline', 90_000, 'online', 'enrolled', 'down'],
    ['enrolled node that never sent a snapshot (EAB healthy)', 'healthy', null, null, 'enrolled', 'down'],
    ['enrolled node that never sent a snapshot (EAB stale)', 'stale', null, null, 'enrolled', 'down'],
    // quiet: expected silence wins over everything else
    ['mode offline, fresh snapshot', 'healthy', 5, 'offline', 'enrolled', 'quiet'],
    ['mode offline, EAB offline, old snapshot', 'offline', 90_000, 'offline', 'enrolled', 'quiet'],
    ['mode survival, EAB offline', 'offline', 9_000, 'survival', 'enrolled', 'quiet'],
    ['mode survival, EAB stale', 'stale', 700, 'survival', 'enrolled', 'quiet'],
    ['pending phase (no snapshot yet)', 'offline', null, null, 'pending', 'quiet'],
    ['pending phase with a mode', 'healthy', 5, 'online', 'pending', 'quiet'],
  ])('%s', (_name, eab, ageS, mode, phase, expected) => {
    expect(health(eab, ageS, mode, phase)).toBe(expected);
  });

  it('a snapshot dated slightly in the future (clock skew) counts as age 0', () => {
    expect(ageSeconds(new Date(NOW.getTime() + 30_000), NOW)).toBe(0);
    expect(health('healthy', -30, 'online', 'enrolled')).toBe('healthy');
  });

  it('reads age as Infinity when there is no snapshot', () => {
    expect(ageSeconds(null, NOW)).toBe(Number.POSITIVE_INFINITY);
  });

  it('is a pure function of its input (same input, same answer)', () => {
    const input = { eab: 'healthy' as const, phase: 'enrolled' as const, mode: 'online' as const, lastStateAt: ago(10), now: NOW };
    expect(deriveHealth(input)).toBe(deriveHealth({ ...input }));
  });
});

describe('routable and gatewayRoutable', () => {
  const base: RoutableInput = {
    health: 'healthy',
    mode: 'online',
    phase: 'enrolled',
    gatewayEnabled: true,
    servePattern: 'B',
    keyReportedAt: ago(3600),
  };

  it('is true for a healthy, online, enrolled, enabled node with a serve pattern', () => {
    expect(isRoutable(base)).toBe(true);
    expect(isGatewayRoutable(base)).toBe(true);
  });

  it.each<[string, Partial<RoutableInput>]>([
    ['degraded health', { health: 'degraded' }],
    ['down health', { health: 'down' }],
    ['quiet health', { health: 'quiet' }],
    ['mode offline', { mode: 'offline' }],
    ['mode survival', { mode: 'survival' }],
    ['mode unknown', { mode: null }],
    ['phase pending', { phase: 'pending' }],
    ['phase retired', { phase: 'retired' }],
    ['gateway disabled', { gatewayEnabled: false }],
    ['servePattern none', { servePattern: 'none' }],
  ])('is false for %s', (_name, patch) => {
    expect(isRoutable({ ...base, ...patch })).toBe(false);
    expect(isGatewayRoutable({ ...base, ...patch })).toBe(false);
  });

  it.each<['A' | 'B']>([['A'], ['B']])('serve pattern %s is routable', (servePattern) => {
    expect(isRoutable({ ...base, servePattern })).toBe(true);
  });

  it('gatewayRoutable additionally needs a reported llama key; routable does not', () => {
    const noKey = { ...base, keyReportedAt: null };
    expect(isRoutable(noKey)).toBe(true);
    expect(isGatewayRoutable(noKey)).toBe(false);
  });
});
