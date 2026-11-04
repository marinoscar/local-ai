// The VPS stack-agent on real SQL (docs/specs/cloud-app.md section 4.10): the whole app on a migrated database, a `nod_`
// credential minted by the platform, a worker registered as the stack-agent, and the real node routes: claim, the per-job
// secret (the broker), the result. Only the VPS itself is absent: the "agent" here posts a journal the executor would
// produce, and the executor's own behaviour against fakes is proven in apps/cli.
//
// What only real SQL proves: the Route53 key is ciphertext at rest and comes back through the broker only, the platform
// stores the broker's HANDLE and not the material, the strict result schema is the trust boundary, the journal and the
// checklist move in one row-locked write that a manual checklist action does not undo, and the secret tripwire over logs,
// audit rows, jobs, the stored checklist and every response.
import { randomBytes } from 'node:crypto';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

// A throwaway key for the cipher, generated per run and never committed. The CI database has none configured.
process.env.SECRETS_ENCRYPTION_KEY ??= randomBytes(32).toString('base64');
// The stack-agent runbook's setting: the API server leaves node-eligible types to the fleet.
process.env.JOBS_WORKER_MODE = 'system';

import { createApp } from '../../src/main';
import { JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH } from '../../src/localai/vps/vps.constants';
import { PrismaService } from '../../src/prisma/prisma.service';
import { captureOutput, enableNestLogging } from './support/capture-output';

const run = `${Date.now()}`;
const upper = (n: number) => randomBytes(n).toString('hex').toUpperCase().slice(0, n);
const KEY = {
  accessKeyId: `AKIA${upper(16)}`,
  secretAccessKey: randomBytes(30).toString('base64').replace(/[^A-Za-z0-9]/g, 'x').slice(0, 40),
  hostedZoneId: `Z${upper(14)}`,
};
const TAIL_IP = [100, 64 + (randomBytes(1)[0]! % 60), 1 + (randomBytes(1)[0]! % 250), 1 + (randomBytes(1)[0]! % 250)].join('.');
const TAIL_NAME = `dell-linux.${['tail', randomBytes(3).toString('hex')].join('')}.ts.net`;
const NODE_NUMBER = 6000 + (Number(run.slice(-3)) % 1000);
const NODE_ID = `ai-n${NODE_NUMBER}`;
const HOST = `${NODE_ID}-staging.marin.cr`;
const SECRETS = [KEY.accessKeyId, KEY.secretAccessKey, KEY.hostedZoneId, TAIL_IP, TAIL_NAME, TAIL_NAME.split('.')[1]!];

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
let ownerId: string;
let adminToken = '';
let workerToken = '';
let workerId = '';
let credentialId = '';
const nodeIds = [NODE_ID];

const admin = (method: 'GET' | 'POST' | 'PUT' | 'DELETE', url: string, payload?: unknown) =>
  app.inject({ method, url, headers: { authorization: `Bearer ${adminToken}` }, payload: payload as never });
const agent = (method: 'GET' | 'POST', url: string, payload?: unknown) =>
  app.inject({ method, url, headers: { authorization: `Bearer ${workerToken}` }, payload: payload as never });

const publishSteps = (over: Record<string, 'done' | 'skipped' | 'failed'> = {}) =>
  ['dns_upsert', 'dns_wait', 'bootstrap_vhost', 'certificate', 'final_vhost', 'validate'].map((name) => ({ name, status: over[name] ?? 'done', detail: `${name} finished.` }));
const publishResult = (over: Record<string, 'done' | 'skipped' | 'failed'> = {}, host = HOST) => ({ host, steps: publishSteps(over), certificateExpiresAt: '2027-02-01T00:00:00.000Z' });

/** Claims the next job of `type` as the stack-agent. */
async function claim(type: string) {
  const response = await agent('POST', `/api/nodes/${workerId}/claim`, { types: [type], limit: 1 });
  expect(response.statusCode).toBe(200);
  const assignments = (response.json() as { data: { jobs: Array<{ job: { id: string; type: string }; params: unknown; claimToken?: string }> } }).data.jobs;
  const found = assignments.find((assignment) => assignment.job.type === type);
  return found ? { id: found.job.id, type: found.job.type, params: found.params, claimToken: found.claimToken } : undefined;
}

const journalOf = async () => ((await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId: NODE_ID } })).publishChecklist as Record<string, any>);

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  credentials = app.get(NodeCredentialService);

  const current = (await prisma.systemSettings.findUnique({ where: { key: 'global' } }))?.value as Record<string, any> | null;
  const value = { ...(current ?? {}), nodes: { ...(current?.nodes ?? {}), jobSecretBrokerEnabled: true }, localai: { ...(current?.localai ?? {}), vps: { stagingSuffix: '-staging' } } };
  await prisma.systemSettings.update({ where: { key: 'global' }, data: { value } });

  const email = `vps-${run}@example.test`;
  const login = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'admin' } });
  adminToken = new URL(String(login.headers.location)).searchParams.get('token') ?? '';
  ownerId = (await prisma.user.findFirstOrThrow({ where: { email } })).id;

  const minted = await credentials.createCredential(ownerId, { name: `vps-stack-agent-${run}` } as never);
  credentialId = minted.id;
  workerToken = minted.token;
  const registered = await agent('POST', '/api/nodes/register', {
    name: 'vps-stack-agent',
    hostname: 'vps-host',
    platform: 'linux/amd64',
    cliVersion: 'laictl/0.1.0',
    eligibleTypes: [JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH],
    concurrency: 1,
  });
  expect([200, 201]).toContain(registered.statusCode);
  workerId = (registered.json() as { data: { node: { id: string } } }).data.node.id;

  await prisma.localaiNode.create({
    data: {
      nodeId: NODE_ID,
      displayName: 'Node under test',
      publicHost: `${NODE_ID}.marin.cr`,
      servePattern: 'B',
      tailnetIp: TAIL_IP,
      tailnetMachine: TAIL_NAME,
      phase: 'enrolled',
      createdById: ownerId,
    },
  });
});

afterAll(async () => {
  if (prisma) {
    const jobs = await prisma.job.findMany({ where: { subjectType: 'localai_node', subjectId: { in: nodeIds } }, select: { id: true } });
    await prisma.jobNodeSecret.deleteMany({ where: { jobId: { in: jobs.map((j) => j.id) } } });
    await prisma.job.deleteMany({ where: { id: { in: jobs.map((j) => j.id) } } });
    await prisma.credential.deleteMany({ where: { purpose: 'localai_route53' } });
    await prisma.auditEvent.deleteMany({ where: { OR: [{ targetId: { in: nodeIds } }, { action: { startsWith: 'localai.vps.' } }] } });
    await prisma.localaiNode.deleteMany({ where: { nodeId: { in: nodeIds } } });
    await prisma.workerNode.deleteMany({ where: { createdById: ownerId } });
    await prisma.nodeCredential.deleteMany({ where: { id: credentialId } });
    const current = (await prisma.systemSettings.findUnique({ where: { key: 'global' } }))?.value as Record<string, any> | null;
    if (current) {
      const { nodes, localai, ...rest } = current;
      const restored = { ...rest, ...(nodes ? { nodes: { ...nodes, jobSecretBrokerEnabled: undefined } } : {}), ...(localai ? { localai: { ...localai, vps: undefined } } : {}) };
      await prisma.systemSettings.update({ where: { key: 'global' }, data: { value: JSON.parse(JSON.stringify(restored)) } });
    }
  }
  await app?.close();
});

describe('the VPS stack-agent on real SQL', () => {
  it('end to end: key, publish, claim, brokered secret, result, journal, wizard, unpublish, with a secret tripwire', async () => {
    const restoreLogger = enableNestLogging();
    const capture = captureOutput();
    const responses: string[] = [];
    const keep = <T extends { body: string }>(response: T): T => {
      responses.push(response.body);
      return response;
    };
    try {
      // 1. Before a key is stored, publishing is refused with the reason.
      const refused = keep(await admin('POST', `/api/localai/nodes/${NODE_ID}/publish`));
      expect(refused.statusCode).toBe(409);
      expect(refused.body).toContain('route53_not_configured');

      // 2. Store the key (write-only). At rest it is ciphertext, under the new purpose.
      expect(keep(await admin('PUT', '/api/localai/vps/route53', KEY)).statusCode).toBe(200);
      const stored = await prisma.credential.findMany({ where: { purpose: 'localai_route53' } });
      expect(stored.map((row) => row.name).sort()).toEqual(['access_key_id', 'hosted_zone_id', 'secret_access_key']);
      for (const row of stored) for (const secret of [KEY.accessKeyId, KEY.secretAccessKey, KEY.hostedZoneId]) expect(JSON.stringify(row)).not.toContain(secret);
      const shown = keep(await admin('GET', '/api/localai/vps/route53'));
      expect((shown.json() as { data: { configured: boolean } }).data.configured).toBe(true);

      // 3. The status says publishing is available (the agent is registered and online, the broker is on).
      const status = keep(await admin('GET', `/api/localai/nodes/${NODE_ID}/publish`));
      const blockers = (status.json() as { data: { publish: { blockers: Array<{ code: string }> } } }).data.publish.blockers.map((b) => b.code);
      expect(blockers).toEqual([]);

      // 4. Publish: the job params are exactly { nodeId }.
      const queued = keep(await admin('POST', `/api/localai/nodes/${NODE_ID}/publish`));
      expect(queued.statusCode).toBe(202);
      const jobId = (queued.json() as { data: { jobId: string } }).data.jobId;
      const row = await prisma.job.findUniqueOrThrow({ where: { id: jobId } });
      expect(row).toMatchObject({ type: JOB_VPS_PUBLISH, subjectType: 'localai_node', subjectId: NODE_ID, payload: { nodeId: NODE_ID } });
      expect(JSON.stringify(row)).not.toContain(TAIL_IP);
      // A second press while it is active is refused.
      expect((await admin('POST', `/api/localai/nodes/${NODE_ID}/publish`)).statusCode).toBe(409);

      // 5. The stack-agent claims it and asks for the brokered secret: the one place the material appears.
      const claimed = await claim(JOB_VPS_PUBLISH);
      expect(claimed?.id).toBe(jobId);
      expect(claimed?.params).toEqual({ nodeId: NODE_ID });
      expect(JSON.stringify(claimed)).not.toContain(TAIL_IP);
      const secret = await agent('POST', `/api/nodes/${workerId}/jobs/${jobId}/secret`, claimed?.claimToken ? { claimToken: claimed.claimToken } : {});
      expect(secret.statusCode).toBe(200);
      expect(secret.headers['cache-control']).toContain('no-store');
      const material = (secret.json() as { data: { kind: string; material: Record<string, string> } }).data;
      expect(material.kind).toBe('localai.vps.route53');
      expect(material.material).toMatchObject({
        route53AccessKeyId: KEY.accessKeyId,
        route53SecretAccessKey: KEY.secretAccessKey,
        hostedZoneId: KEY.hostedZoneId,
        host: HOST,
        stagingSuffix: '-staging',
        servePattern: 'B',
        tailnetIp: TAIL_IP,
        tailnetMachine: TAIL_NAME,
      });
      // The platform persisted the handle only.
      const grants = await prisma.jobNodeSecret.findMany({ where: { jobId } });
      expect(grants).toHaveLength(1);
      expect(grants[0]!.handle).toBe(`vps:${jobId}`);
      for (const secretValue of SECRETS) expect(JSON.stringify(grants)).not.toContain(secretValue);

      // 6. A result that breaks the strict schema is refused and changes nothing.
      const claimBody = claimed?.claimToken ? { claimToken: claimed.claimToken } : {};
      for (const bad of [{ ...publishResult(), extra: true }, publishResult({}, 'evil.example.test'), { ...publishResult(), steps: [{ name: 'rm_rf', status: 'done', detail: 'x' }] }]) {
        const refusedResult = await agent('POST', `/api/nodes/${workerId}/jobs/${jobId}/result`, { type: JOB_VPS_PUBLISH, result: bad, ...claimBody });
        expect(refusedResult.statusCode).toBeGreaterThanOrEqual(400);
      }
      expect((await journalOf()).vps).toBeUndefined();

      // 7. The real result: the job settles, steps 2 to 5 are done, the journal is kept.
      const settled = await agent('POST', `/api/nodes/${workerId}/jobs/${jobId}/result`, { type: JOB_VPS_PUBLISH, result: publishResult({ bootstrap_vhost: 'skipped' }), ...claimBody });
      expect(settled.statusCode).toBe(200);
      expect((await prisma.job.findUniqueOrThrow({ where: { id: jobId } })).status).toBe('succeeded');
      let checklist = await journalOf();
      for (const step of ['dns', 'bootstrap_vhost', 'certificate', 'https_vhost']) expect(checklist[step].state).toBe('done');
      expect(checklist.vps).toMatchObject({ jobId, action: 'publish', host: HOST, outcome: 'done', certificateExpiresAt: '2027-02-01T00:00:00.000Z' });
      expect(await prisma.auditEvent.count({ where: { action: 'localai.vps.published', targetId: NODE_ID } })).toBe(1);
      expect(await prisma.auditEvent.count({ where: { action: 'localai.vps.publish_requested', targetId: NODE_ID, actorUserId: ownerId } })).toBe(1);

      // 8. The wizard shows steps 2 to 5 done, and a manual action afterwards does not erase the journal.
      const wizard = keep(await admin('GET', `/api/localai/nodes/${NODE_ID}/checklist`));
      const steps = (wizard.json() as { data: { steps: Array<{ step: string; state: string }> } }).data.steps;
      for (const step of ['dns', 'bootstrap_vhost', 'certificate', 'https_vhost']) expect(steps.find((s) => s.step === step)?.state).toBe('done');
      expect(keep(await admin('POST', `/api/localai/nodes/${NODE_ID}/checklist/tailnet`, { action: 'mark_done' })).statusCode).toBe(200);
      checklist = await journalOf();
      expect(checklist.tailnet.state).toBe('done');
      expect(checklist.vps.jobId).toBe(jobId);
      const after = keep(await admin('GET', `/api/localai/nodes/${NODE_ID}/publish`));
      const run = (after.json() as { data: { run: { state: string; steps: Array<{ name: string; status: string }>; certificateExpiresAt: string } } }).data.run;
      expect(run.state).toBe('succeeded');
      expect(run.steps.map((s) => `${s.name}:${s.status}`)).toEqual(['dns_upsert:done', 'dns_wait:done', 'bootstrap_vhost:skipped', 'certificate:done', 'final_vhost:done', 'validate:done']);

      // 9. Re-running is allowed once the job settled; a failed run stores its journal, marks the step and fails the job.
      const again = keep(await admin('POST', `/api/localai/nodes/${NODE_ID}/publish`));
      expect(again.statusCode).toBe(202);
      const secondId = (again.json() as { data: { jobId: string } }).data.jobId;
      const second = await claim(JOB_VPS_PUBLISH);
      expect(second?.id).toBe(secondId);
      const secondBody = second?.claimToken ? { claimToken: second.claimToken } : {};
      await agent('POST', `/api/nodes/${workerId}/jobs/${secondId}/secret`, secondBody);
      await agent('POST', `/api/nodes/${workerId}/jobs/${secondId}/result`, { type: JOB_VPS_PUBLISH, result: { host: HOST, steps: publishSteps({ certificate: 'failed' }).slice(0, 4), certificateExpiresAt: null }, ...secondBody });
      const failedJob = await prisma.job.findUniqueOrThrow({ where: { id: secondId } });
      expect(failedJob.status).toBe('failed');
      expect(failedJob.lastError).toContain('step certificate failed');
      checklist = await journalOf();
      expect(checklist.certificate.state).toBe('failed');
      expect(checklist.vps).toMatchObject({ jobId: secondId, outcome: 'failed' });

      // 10. Unpublish: no tailnet fields in its secret, and steps 2 to 5 go back to todo.
      const unq = keep(await admin('POST', `/api/localai/nodes/${NODE_ID}/unpublish`));
      expect(unq.statusCode).toBe(202);
      const unId = (unq.json() as { data: { jobId: string } }).data.jobId;
      const un = await claim(JOB_VPS_UNPUBLISH);
      expect(un?.id).toBe(unId);
      const unBody = un?.claimToken ? { claimToken: un.claimToken } : {};
      const unSecret = await agent('POST', `/api/nodes/${workerId}/jobs/${unId}/secret`, unBody);
      const unMaterial = (unSecret.json() as { data: { material: Record<string, string> } }).data.material;
      expect(unMaterial.tailnetIp).toBeUndefined();
      expect(unMaterial.tailnetMachine).toBeUndefined();
      expect(unMaterial.host).toBe(HOST);
      const done = await agent('POST', `/api/nodes/${workerId}/jobs/${unId}/result`, {
        type: JOB_VPS_UNPUBLISH,
        result: { host: HOST, steps: [{ name: 'vhost_remove', status: 'done', detail: 'Took the vhost out of service.' }, { name: 'dns_delete', status: 'done', detail: 'Deleted the A record.' }, { name: 'certificate_kept', status: 'skipped', detail: 'Left to expire.' }] },
        ...unBody,
      });
      expect(done.statusCode).toBe(200);
      checklist = await journalOf();
      for (const step of ['dns', 'bootstrap_vhost', 'certificate', 'https_vhost']) expect(checklist[step].state).toBe('todo');
      expect(checklist.vps).toMatchObject({ jobId: unId, action: 'unpublish', outcome: 'done' });
      expect(await prisma.auditEvent.count({ where: { action: 'localai.vps.unpublished', targetId: NODE_ID } })).toBe(1);

      // 11. THE TRIPWIRE. The key, the zone, the tailnet address and name appear in no log line, no audit row, no job row,
      // no stored checklist and no response of the admin routes. (The agent's own secret response is the only exception.)
      const audits = JSON.stringify(await prisma.auditEvent.findMany({ where: { OR: [{ targetId: NODE_ID }, { action: { startsWith: 'localai.vps.' } }] } }));
      const jobsDump = JSON.stringify(await prisma.job.findMany({ where: { subjectId: NODE_ID } }));
      const checklistDump = JSON.stringify((await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId: NODE_ID } })).publishChecklist);
      const logs = capture.text();
      for (const secretValue of SECRETS) {
        expect(audits).not.toContain(secretValue);
        expect(jobsDump).not.toContain(secretValue);
        expect(checklistDump).not.toContain(secretValue);
        expect(logs).not.toContain(secretValue);
        // The wizard checklist route renders the tailnet values in its snippets for a lab:admin; no other admin response may.
        for (const body of responses.filter((text) => !text.includes('"snippets"'))) expect(body).not.toContain(secretValue);
      }
    } finally {
      capture.restore();
      restoreLogger();
    }
  });

  it('refuses a publish while the staging suffix is invalid', async () => {
    const current = (await prisma.systemSettings.findUniqueOrThrow({ where: { key: 'global' } })).value as Record<string, any>;
    await prisma.systemSettings.update({ where: { key: 'global' }, data: { value: { ...current, localai: { ...current.localai, vps: { stagingSuffix: 'Staging!' } } } } });
    try {
      const response = await admin('POST', `/api/localai/nodes/${NODE_ID}/publish`);
      expect(response.statusCode).toBe(409);
      expect(response.body).toContain('staging_suffix_invalid');
    } finally {
      await prisma.systemSettings.update({ where: { key: 'global' }, data: { value: current } });
    }
  });

  it('the brokered secret is refused for a node that is not ready (no tailnet address)', async () => {
    const id = `ai-n${NODE_NUMBER + 1000 > 9999 ? NODE_NUMBER - 1 : NODE_NUMBER + 1000}`;
    nodeIds.push(id);
    await prisma.localaiNode.create({ data: { nodeId: id, displayName: 'No tailnet', publicHost: `${id}.marin.cr`, servePattern: 'B', phase: 'enrolled', createdById: ownerId } });
    const response = await admin('POST', `/api/localai/nodes/${id}/publish`);
    expect(response.statusCode).toBe(409);
    expect(response.body).toContain('tailnet_incomplete');
  });

  it('DELETE removes the key and the broker stops being usable', async () => {
    expect((await admin('DELETE', '/api/localai/vps/route53')).statusCode).toBe(200);
    expect(await prisma.credential.count({ where: { purpose: 'localai_route53' } })).toBe(0);
    const response = await admin('POST', `/api/localai/nodes/${NODE_ID}/publish`);
    expect(response.statusCode).toBe(409);
  });
});
