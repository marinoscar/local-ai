import { classifyNodeHealth, isCatalogNotifyEligible } from './node-eligibility';

const NOW = new Date('2026-11-04T12:00:00.000Z');
const ago = (seconds: number) => new Date(NOW.getTime() - seconds * 1000);
const STALE = 45;

const node = (over: Partial<Parameters<typeof classifyNodeHealth>[0]> = {}) => ({
  phase: 'enrolled' as const,
  mode: 'online',
  lastState: null,
  lastStateAt: ago(10),
  ...over,
});
const worker = (over: Partial<{ status: string; lastHeartbeatAt: Date | null }> = {}) => ({ status: 'online', lastHeartbeatAt: ago(5), ...over });

describe('derived health for catalog notification', () => {
  it('healthy: fresh heartbeat and a snapshot under 2 minutes', () => {
    expect(classifyNodeHealth(node(), worker(), STALE, NOW)).toBe('healthy');
    expect(isCatalogNotifyEligible(node(), worker(), STALE, NOW)).toBe(true);
  });

  it('degraded: a stale heartbeat or a snapshot of 2 to 10 minutes', () => {
    expect(classifyNodeHealth(node(), worker({ lastHeartbeatAt: ago(120) }), STALE, NOW)).toBe('degraded');
    expect(classifyNodeHealth(node({ lastStateAt: ago(300) }), worker(), STALE, NOW)).toBe('degraded');
    expect(isCatalogNotifyEligible(node({ lastStateAt: ago(300) }), worker(), STALE, NOW)).toBe(true);
  });

  it('down: EAB offline, a snapshot older than 10 minutes, no snapshot or no worker', () => {
    expect(classifyNodeHealth(node(), worker({ status: 'offline' }), STALE, NOW)).toBe('down');
    expect(classifyNodeHealth(node({ lastStateAt: ago(601) }), worker(), STALE, NOW)).toBe('down');
    expect(classifyNodeHealth(node({ lastStateAt: null }), worker(), STALE, NOW)).toBe('down');
    expect(classifyNodeHealth(node(), null, STALE, NOW)).toBe('down');
    expect(isCatalogNotifyEligible(node({ lastStateAt: ago(601) }), worker(), STALE, NOW)).toBe(false);
  });

  it('quiet (expected silence): offline or survival mode, or a pending node', () => {
    expect(classifyNodeHealth(node({ mode: 'offline' }), worker(), STALE, NOW)).toBe('quiet');
    expect(classifyNodeHealth(node({ mode: 'survival' }), worker(), STALE, NOW)).toBe('quiet');
    expect(classifyNodeHealth(node({ mode: null, lastState: { mode: 'survival' } }), worker(), STALE, NOW)).toBe('quiet');
    expect(classifyNodeHealth(node({ phase: 'pending' }), worker(), STALE, NOW)).toBe('quiet');
    expect(isCatalogNotifyEligible(node({ mode: 'offline' }), worker(), STALE, NOW)).toBe(false);
  });

  it('never notifies a pending or retired node', () => {
    expect(isCatalogNotifyEligible(node({ phase: 'pending' }), worker(), STALE, NOW)).toBe(false);
    expect(isCatalogNotifyEligible(node({ phase: 'retired' }), worker(), STALE, NOW)).toBe(false);
  });
});
