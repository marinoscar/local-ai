// Agent supervisor: the agent's systemd unit state and the group-membership diagnosis (docs/specs/desktop.md 4.2).
// DSK-03 owns the read-only parts. "Start agent" (systemctl, then pkexec; LAI-DSK-005) arrives with DSK-05, which
// fills `AgentSupervisor.start`. No shell is ever used: `execFile` with fixed arguments.
import { execFile } from 'node:child_process';
import { userInfo } from 'node:os';

export const MODULE = 'agent-supervisor' as const;

export const OPERATORS_GROUP = 'localai-operators';
export const AGENT_UNIT = 'localai-agent.service';
const SYSTEMCTL = '/usr/bin/systemctl';
const GETENT = '/usr/bin/getent';

/**
 * Why the token file could not be read (it answered `EACCES`):
 * - `not-member`: the user is not in `localai-operators` (LAI-DSK-001);
 * - `needs-relogin`: the user is in the group, but this login session does not have it yet (LAI-DSK-002);
 * - `member-active`: the user is in the group and has it; the group is not the cause (the file's mode is).
 */
export type GroupDiagnosis = { kind: 'not-member' | 'needs-relogin' | 'member-active'; gid?: number };

export interface GroupDeps {
  /** Output of `getent group localai-operators` (`name:x:gid:member,member`), or null when the group does not exist. */
  getentGroup(): Promise<string | null>;
  username(): string;
  /** The user's primary gid (members whose primary group it is are not listed by `getent`). */
  primaryGid(): number;
  /** `process.getgroups()`: the groups of this process (Node includes the effective gid). */
  processGroups(): number[];
}

function execText(file: string, args: string[], timeoutMs = 3000): Promise<{ code: number | string | null; stdout: string }> {
  return new Promise((resolve) => {
    execFile(file, args, { timeout: timeoutMs, encoding: 'utf8', windowsHide: true }, (error, stdout) => {
      if (!error) return resolve({ code: 0, stdout });
      const e = error as NodeJS.ErrnoException & { code?: number | string };
      resolve({ code: e.code ?? null, stdout: typeof stdout === 'string' ? stdout : '' });
    });
  });
}

export const defaultGroupDeps: GroupDeps = {
  async getentGroup() {
    const result = await execText(GETENT, ['group', OPERATORS_GROUP]);
    return result.code === 0 && result.stdout.trim() !== '' ? result.stdout.trim() : null;
  },
  username: () => userInfo().username,
  primaryGid: () => userInfo().gid,
  processGroups: () => (typeof process.getgroups === 'function' ? process.getgroups() : []),
};

/** The table of desktop.md 4.2, from the groups database and this process's groups. */
export async function diagnoseGroupAccess(deps: GroupDeps = defaultGroupDeps): Promise<GroupDiagnosis> {
  const line = await deps.getentGroup();
  if (line === null) return { kind: 'not-member' };
  const [, , gidText = '', members = ''] = line.split(':');
  const gid = Number(gidText);
  if (!Number.isInteger(gid)) return { kind: 'not-member' };
  const listed = members.split(',').map((m) => m.trim()).includes(deps.username()) || deps.primaryGid() === gid;
  if (!listed) return { kind: 'not-member', gid };
  return deps.processGroups().includes(gid) ? { kind: 'member-active', gid } : { kind: 'needs-relogin', gid };
}

/** `systemctl is-active localai-agent.service`: `active`, `inactive`, `failed`, ... or `unknown` when systemd does not answer. */
export async function unitState(unit: string = AGENT_UNIT, run: typeof execText = execText): Promise<string> {
  const result = await run(SYSTEMCTL, ['is-active', unit]);
  const word = result.stdout.trim().split('\n')[0] ?? '';
  return /^[a-z-]+$/.test(word) ? word : 'unknown';
}

export type StartAgentResult = { ok: true } | { ok: false; code: 'LAI-DSK-005' };

export interface AgentSupervisor {
  unitState(): Promise<string>;
  diagnoseTokenAccess(): Promise<GroupDiagnosis>;
  /** DSK-05. Absent until then: the error page answers LAI-DSK-005 ("run `sudo aictl doctor`"). */
  start?(): Promise<StartAgentResult>;
}

export function createAgentSupervisor(deps: { group?: GroupDeps; unit?: () => Promise<string> } = {}): AgentSupervisor {
  return {
    unitState: deps.unit ?? (() => unitState()),
    diagnoseTokenAccess: () => diagnoseGroupAccess(deps.group ?? defaultGroupDeps),
  };
}
