import { agentUpdateState, compareVersions, gateFor, offlineReason, requiredPermission } from './commandPolicy';
import { ALL_TYPES, detailFixture } from './fixture';

describe('requiredPermission', () => {
  it('needs lab:admin for exactly the four admin types and lab:operate for the other eleven, with or without the prefix', () => {
    const admin = ALL_TYPES.filter((type) => requiredPermission(type) === 'lab:admin');
    expect(admin.sort()).toEqual(['agent.update', 'key.rotate', 'mode.offline', 'model.delete']);
    expect(ALL_TYPES.filter((type) => requiredPermission(type) === 'lab:operate')).toHaveLength(11);
    expect(requiredPermission('localai.model.delete')).toBe('lab:admin');
    expect(requiredPermission('localai.model.load')).toBe('lab:operate');
  });
});

describe('gateFor', () => {
  it('opens for an enrolled online node that allows the type', () => {
    expect(gateFor(detailFixture(), 'model.load')).toEqual({ allowed: true, reason: null, code: null });
  });

  it('refuses an Offline or Survival node with the LAI-CLD-034 text', () => {
    expect(gateFor(detailFixture({ mode: 'offline' }), 'model.load')).toEqual({
      allowed: false,
      code: 'LAI-CLD-034',
      reason: 'This computer is in Offline mode. Only someone at it can switch it back.',
    });
    expect(gateFor(detailFixture({ mode: 'survival' }), 'status.refresh').reason).toBe('This computer is in Survival mode. Only someone at it can switch it back.');
  });

  it('refuses a node that is not enrolled with LAI-CLD-035, before it looks at the mode', () => {
    expect(gateFor(detailFixture({ phase: 'pending', mode: 'offline' }), 'model.load').code).toBe('LAI-CLD-035');
  });

  it('refuses a type the node does not allow with the LAI-CLD-057 text, and trusts a node whose list is unknown', () => {
    const limited = detailFixture({ acceptedTypes: ['status.refresh'] });
    expect(gateFor(limited, 'model.load')).toEqual({ allowed: false, code: 'LAI-CLD-057', reason: 'ai-n2 does not accept this command (disabled on the node)' });
    expect(gateFor(limited, 'localai.status.refresh').allowed).toBe(true);
    expect(gateFor(detailFixture({ acceptedTypes: null }), 'model.load').allowed).toBe(true);
  });

  it('offlineReason names the mode', () => {
    expect(offlineReason('offline')).toContain('Offline mode');
    expect(offlineReason('survival')).toContain('Survival mode');
  });
});

describe('compareVersions', () => {
  it('compares numerically, not as text', () => {
    expect(compareVersions('0.4.2', '0.4.1')).toBe(1);
    expect(compareVersions('0.4.10', '0.4.9')).toBe(1);
    expect(compareVersions('0.4.1', '0.4.1')).toBe(0);
    expect(compareVersions('0.3.9', '0.4.0')).toBe(-1);
    expect(compareVersions('1.0.0', '0.99.99')).toBe(1);
  });
  it('is null when either side is not a plain version', () => {
    expect(compareVersions(null, '0.4.1')).toBeNull();
    expect(compareVersions('0.4.1', 'dev')).toBeNull();
  });
});

describe('agentUpdateState', () => {
  const node = (patch: Parameters<typeof detailFixture>[0]) => agentUpdateState(detailFixture({ agentVersion: '0.4.1', ...patch }));

  it('is hidden when agent.latestVersion is null', () => {
    expect(node({ agentLatestVersion: null })).toMatchObject({ visible: false, enabled: false });
  });

  it('is visible but disabled when the latest version is equal to or older than the node', () => {
    expect(node({ agentLatestVersion: '0.4.1' })).toMatchObject({ visible: true, enabled: false });
    expect(node({ agentLatestVersion: '0.4.0' })).toMatchObject({ visible: true, enabled: false });
    expect(node({ agentLatestVersion: '0.4.2', agentVersion: null })).toMatchObject({ visible: true, enabled: false });
  });

  it('is disabled when the node is not online or not enrolled, with the reason', () => {
    expect(node({ agentLatestVersion: '0.4.2', mode: 'offline' })).toMatchObject({ enabled: false, reason: expect.stringContaining('Offline mode') });
    expect(node({ agentLatestVersion: '0.4.2', phase: 'pending' })).toMatchObject({ enabled: false });
    expect(node({ agentLatestVersion: '0.4.2', health: 'down' })).toMatchObject({ enabled: false });
  });

  it('is enabled for a newer version on an online enrolled node, with from and to', () => {
    expect(node({ agentLatestVersion: '0.4.2' })).toEqual({ visible: true, enabled: true, from: '0.4.1', to: '0.4.2', reason: null });
  });
});
