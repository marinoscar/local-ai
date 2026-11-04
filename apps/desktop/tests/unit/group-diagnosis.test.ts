import { describe, expect, it } from 'vitest';
import { createAgentClient } from '../../src/main/agent-client';
import { type GroupDeps, diagnoseGroupAccess, unitState } from '../../src/main/agent-supervisor';
import { codeForTokenFailure } from '../../src/main/startup';

// desktop.md 4.2 / 9: (listed, gid present) -> readable path; (listed, gid absent) -> 002; (not listed) -> 001; ENOENT -> 003.
const GID = 994;
const deps = (over: Partial<GroupDeps> & { line?: string | null; groups?: number[] }): GroupDeps => ({
  getentGroup: async () => (over.line === undefined ? `localai-operators:x:${GID}:alice,bob` : over.line),
  username: () => 'alice',
  primaryGid: () => 1000,
  processGroups: () => over.groups ?? [1000, GID],
  ...over,
});

describe('group diagnosis on EACCES (desktop.md 4.2)', () => {
  it('listed and the gid is in this process: the group is not the cause', async () => {
    expect(await diagnoseGroupAccess(deps({}))).toEqual({ kind: 'member-active', gid: GID });
  });

  it('listed but the gid is missing from this login session: needs a re-login (LAI-DSK-002)', async () => {
    const diagnosis = await diagnoseGroupAccess(deps({ groups: [1000] }));
    expect(diagnosis.kind).toBe('needs-relogin');
    expect(await codeForTokenFailure({ ok: false, reason: 'permission-denied' }, { diagnoseTokenAccess: async () => diagnosis })).toBe('LAI-DSK-002');
  });

  it('not listed: not allowed yet (LAI-DSK-001)', async () => {
    const diagnosis = await diagnoseGroupAccess(deps({ line: `localai-operators:x:${GID}:bob`, groups: [1000] }));
    expect(diagnosis.kind).toBe('not-member');
    expect(await codeForTokenFailure({ ok: false, reason: 'permission-denied' }, { diagnoseTokenAccess: async () => diagnosis })).toBe('LAI-DSK-001');
  });

  it('a user whose primary group it is counts as listed even though getent omits them', async () => {
    expect((await diagnoseGroupAccess(deps({ line: `localai-operators:x:${GID}:`, primaryGid: () => GID, groups: [GID] }))).kind).toBe('member-active');
    expect((await diagnoseGroupAccess(deps({ line: `localai-operators:x:${GID}:`, primaryGid: () => GID, groups: [] }))).kind).toBe('needs-relogin');
  });

  it('a group that does not exist, or a line that cannot be read, means not a member', async () => {
    expect((await diagnoseGroupAccess(deps({ line: null }))).kind).toBe('not-member');
    expect((await diagnoseGroupAccess(deps({ line: 'garbage' }))).kind).toBe('not-member');
  });

  it('a member by name is not confused with a longer name', async () => {
    expect((await diagnoseGroupAccess(deps({ line: `localai-operators:x:${GID}:alice2,malice`, groups: [] }))).kind).toBe('not-member');
  });

  it('an unreadable group that is active but the file still refuses ends on LAI-DSK-001 (the page tells how to fix it)', async () => {
    const active = await diagnoseGroupAccess(deps({}));
    expect(await codeForTokenFailure({ ok: false, reason: 'permission-denied' }, { diagnoseTokenAccess: async () => active })).toBe('LAI-DSK-001');
  });
});

describe('token file read -> error page', () => {
  const clientReading = (error: NodeJS.ErrnoException | string) =>
    createAgentClient({
      tokenPath: '/nonexistent/agent-token',
      readFile: async () => {
        if (typeof error === 'string') return error;
        throw error;
      },
    });
  const errno = (code: string): NodeJS.ErrnoException => Object.assign(new Error(code), { code });
  const nobody = { diagnoseTokenAccess: async () => ({ kind: 'not-member' as const }) };

  it('ENOENT -> LAI-DSK-003, never the group diagnosis', async () => {
    const result = await clientReading(errno('ENOENT')).readToken();
    expect(result).toEqual({ ok: false, reason: 'missing', errno: 'ENOENT' });
    if (result.ok) throw new Error('unreachable');
    let asked = false;
    expect(await codeForTokenFailure(result, { diagnoseTokenAccess: async () => ((asked = true), { kind: 'not-member' as const }) })).toBe('LAI-DSK-003');
    expect(asked).toBe(false);
  });

  it('EACCES -> permission-denied -> group diagnosis', async () => {
    const result = await clientReading(errno('EACCES')).readToken();
    expect(result).toMatchObject({ ok: false, reason: 'permission-denied' });
    if (result.ok) throw new Error('unreachable');
    expect(await codeForTokenFailure(result, nobody)).toBe('LAI-DSK-001');
  });

  it('an empty or malformed file is treated as "not created yet" (LAI-DSK-003)', async () => {
    for (const content of ['', '\n', 'not-a-token']) {
      const result = await clientReading(content).readToken();
      expect(result.ok).toBe(false);
      if (result.ok) throw new Error('unreachable');
      expect(await codeForTokenFailure(result, nobody)).toBe('LAI-DSK-003');
    }
  });
});

describe('unit state', () => {
  it('returns the first word systemctl prints, and "unknown" when it prints nothing usable', async () => {
    expect(await unitState('x.service', async () => ({ code: 3, stdout: 'inactive\n' }))).toBe('inactive');
    expect(await unitState('x.service', async () => ({ code: 'ENOENT', stdout: '' }))).toBe('unknown');
    expect(await unitState('x.service', async () => ({ code: 1, stdout: 'Failed to connect to bus: No such file\n' }))).toBe('unknown');
  });
});
