// Tripwires for the Doctor contract of docs/specs/cloud-app.md section 7: the seven checks are READ-ONLY and expose no
// secret material. Three independent proofs: a recording client that refuses writes, a scan of the check sources, and a
// full run with every stored value replaced by bait that must not come back out.
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { createRecordingPrisma } from '@marinoscar/platform-api/doctor/testing';

import { expectContract, expectNoSecretShapes, leakBait } from '../../../test/localai/support/doctor-asserts';
import { AictlDoctorCheck } from './aictl.check';
import { CatalogApprovedDoctorCheck } from './catalog-approved.check';
import { CommandsStuckDoctorCheck } from './commands-stuck.check';
import { CredentialsDoctorCheck } from './credentials.check';
import { LOCALAI_DOCTOR_CHECKS } from './doctor.module';
import { NodesKeysDoctorCheck } from './nodes-keys.check';
import { NodesPendingDoctorCheck } from './nodes-pending.check';
import { SettingsNodesDoctorCheck } from './settings-nodes.check';

const EXPECTED_IDS = [
  'localai.credentials',
  'localai.aictl',
  'localai.catalog.approved',
  'localai.nodes.pending',
  'localai.commands.stuck',
  'localai.nodes.keys',
  'localai.settings.nodes',
];

const registry = { register: jest.fn() };

describe('the seven Doctor checks', () => {
  it('are exactly the checks of the spec, each with a unique id', () => {
    // The constructors only store their collaborators, so empty stand-ins are enough to read the id.
    const ids = LOCALAI_DOCTOR_CHECKS.map((check) => new (check as unknown as new (...args: unknown[]) => { id: string })(registry, {}, {}).id);
    expect([...ids].sort()).toEqual([...EXPECTED_IDS].sort());
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('perform no write: a recording client that refuses writes sees none, whatever the data', async () => {
    const bait = leakBait();
    const prisma = createRecordingPrisma({
      results: {
        'localaiNode.findMany': [{ nodeId: 'ai-n4', createdAt: new Date('2026-01-01T00:00:00Z') }],
        'localaiCommand.count': 2,
        'localaiCatalogVersion.findFirst': null,
        'auditEvent.findMany': [],
      },
    });
    const credentials = {
      list: jest.fn(async () => [{ name: 'ai-n4' }]),
      getSecret: jest.fn(async () => bait.hex64),
      setSecret: jest.fn(),
      deleteSecret: jest.fn(),
    };
    const settings = { getNamespace: jest.fn(async () => ({ offlineRetentionDays: 30, staleHeartbeatSeconds: 120 })) };
    const checks = [
      new CredentialsDoctorCheck(registry as never, credentials as never, { roundTrip: () => undefined }),
      new AictlDoctorCheck(registry as never, { probe: async () => ({ ok: false as const, reason: 'missing' as const }) }),
      new CatalogApprovedDoctorCheck(registry as never, prisma.client),
      new NodesPendingDoctorCheck(registry as never, prisma.client),
      new CommandsStuckDoctorCheck(registry as never, prisma.client),
      new NodesKeysDoctorCheck(registry as never, prisma.client),
      new SettingsNodesDoctorCheck(registry as never, settings as never),
    ];

    for (const check of checks) {
      const outcome = await check.run();
      expectContract(outcome);
      // The decrypted value, a node token or an address never travels out.
      expect(JSON.stringify(outcome)).not.toContain(bait.hex64);
    }
    expect(prisma.operations().length).toBeGreaterThan(0);
    expect(prisma.violations()).toEqual([]);
    expect(credentials.setSecret).not.toHaveBeenCalled();
    expect(credentials.deleteSecret).not.toHaveBeenCalled();
    // One stored key is read, never all of them.
    expect(credentials.getSecret).toHaveBeenCalledTimes(1);
  });

  it('expose no secret shape even when stored node ids and errors are made of bait', async () => {
    const bait = leakBait();
    const prisma = createRecordingPrisma({
      results: {
        'localaiNode.findMany': [{ nodeId: `${bait.token}-${bait.ip}`, createdAt: new Date('2026-01-01T00:00:00Z') }],
        'auditEvent.findMany': [],
      },
    });
    const failing = {
      list: async () => {
        throw new Error(`cannot decrypt ${bait.hex64} at ${bait.ip} with ${bait.token}`);
      },
    };
    for (const check of [
      new NodesPendingDoctorCheck(registry as never, prisma.client),
      new NodesKeysDoctorCheck(registry as never, prisma.client),
      new CredentialsDoctorCheck(registry as never, failing as never, { roundTrip: () => undefined }),
    ]) {
      expectNoSecretShapes(await check.run());
    }
  });

  it('have sources that call no Prisma, credential or job write', () => {
    const forbidden = /\.(create|createMany|update|updateMany|upsert|delete|deleteMany)\(|\$executeRaw|\$queryRaw|\$transaction|setSecret|deleteSecret|\.enqueue\(|\.record\(|\.publish\(|\.notify\(/;
    const files = readdirSync(__dirname).filter((name) => name.endsWith('.ts') && !name.endsWith('.spec.ts'));
    expect(files.length).toBeGreaterThanOrEqual(10);
    for (const name of files) {
      const stripped = readFileSync(join(__dirname, name), 'utf8')
        .split('\n')
        .filter((line) => !line.trim().startsWith('//'))
        .join('\n');
      expect({ file: name, write: forbidden.exec(stripped)?.[0] ?? null }).toEqual({ file: name, write: null });
    }
  });
});
