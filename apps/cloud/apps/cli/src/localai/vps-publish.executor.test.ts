import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

import type { JobExecutionContext } from '@marinoscar/platform-cli/node';
import { afterEach, describe, expect, it } from 'vitest';

import { FAKE_VPS_IP, FakeVps, fakeSecretValues } from './vps-fakes.fake.js';
import { VpsPublishExecutor, VpsUnpublishExecutor, parseParams, parseSecret } from './vps-publish.executor.js';
import { VPS_SECRET_KIND } from './vps-types.js';

const HOST = 'ai-n3-staging.marin.cr';
const ip4 = (...parts: number[]) => parts.join('.');
const SHARED = fakeSecretValues();
let vps: FakeVps;
afterEach(() => vps?.dispose());

interface Harness {
  ctx: JobExecutionContext;
  logs: string[];
  secrets: ReturnType<typeof fakeSecretValues>;
}

function harness(options: { secrets?: ReturnType<typeof fakeSecretValues>; params?: Record<string, unknown>; material?: Record<string, unknown>; kind?: string; pattern?: 'A' | 'B'; suffix?: string; nodeId?: string } = {}): Harness {
  const secrets = options.secrets ?? SHARED;
  const nodeId = options.nodeId ?? 'ai-n3';
  const suffix = options.suffix ?? '-staging';
  const logs: string[] = [];
  const material = {
    route53AccessKeyId: secrets.route53AccessKeyId,
    route53SecretAccessKey: secrets.route53SecretAccessKey,
    hostedZoneId: secrets.hostedZoneId,
    host: `${nodeId}${suffix}.marin.cr`,
    stagingSuffix: suffix,
    vpsPublicHostname: 'ai.marin.cr',
    servePattern: options.pattern ?? 'B',
    tailnetIp: secrets.tailnetIp,
    tailnetMachine: secrets.tailnetMachine,
    ...options.material,
  };
  const ctx = {
    job: { id: 'job-1', type: 'x', subjectType: null, subjectId: null, priority: 0, attempts: 1, startedAt: '', leaseExpiresAt: '' },
    params: options.params ?? { nodeId },
    inputPath: undefined,
    input: undefined,
    api: { jobSecret: async () => ({ kind: options.kind ?? VPS_SECRET_KIND, expiresAt: '2030-01-01T00:00:00Z', material }) },
    nodeId: 'worker-1',
    claimToken: 'claim',
    signal: new AbortController().signal,
    log: (message: string, fields?: Record<string, unknown>) => logs.push(`${message} ${JSON.stringify(fields ?? {})}`),
  } as unknown as JobExecutionContext;
  return { ctx, logs, secrets };
}

const publisher = () => new VpsPublishExecutor(() => vps.deps());
const unpublisher = () => new VpsUnpublishExecutor(() => vps.deps());
const statuses = (steps: Array<{ name: string; status: string }>) => steps.map((s) => `${s.name}:${s.status}`);

describe('publish: the happy path', () => {
  it('publishes ai-n3-staging.marin.cr end to end (DNS, certificate, final vhost, validation)', async () => {
    vps = new FakeVps();
    vps.route53.insyncAfter = 2;
    vps.dns.lag = 3;
    const h = harness();
    const result = await publisher().execute(h.ctx);
    expect(result.host).toBe(HOST);
    expect(statuses(result.steps)).toEqual([
      'dns_upsert:done',
      'dns_wait:done',
      'bootstrap_vhost:done',
      'certificate:done',
      'final_vhost:done',
      'validate:done',
    ]);
    expect(result.certificateExpiresAt).toBe(vps.certExpiry.toISOString());
    expect(vps.route53.zone.get(HOST)).toBe(FAKE_VPS_IP);
    const final = vps.files()[`${HOST}.conf`]!;
    expect(final).toContain('listen 443 ssl;');
    expect(final).toContain(`server_name ${HOST};`);
    expect(final).toContain(`proxy_pass https://${h.secrets.tailnetIp}:443;`); // the real values only in the nginx file
    expect(vps.http.calls).toEqual([`${HOST}/health`, `${HOST}/v1/models`]);
  });

  it('renders Pattern A', async () => {
    vps = new FakeVps();
    const h = harness({ pattern: 'A', material: { tailnetMachine: 'dell-linux' } });
    const result = await publisher().execute(h.ctx);
    expect(result.steps.at(-1)).toMatchObject({ name: 'validate', status: 'done' });
    expect(vps.files()[`${HOST}.conf`]).toContain(`proxy_pass http://${h.secrets.tailnetIp}:18080;`);
  });

  it('publishes a production host under an empty suffix', async () => {
    vps = new FakeVps();
    const result = await publisher().execute(harness({ suffix: '' }).ctx);
    expect(result.host).toBe('ai-n3.marin.cr');
  });
});

describe('publish: idempotency (re-running is a no-op)', () => {
  it('a second run yields skipped/done only and changes nothing', async () => {
    vps = new FakeVps();
    await publisher().execute(harness().ctx);
    const filesBefore = vps.files();
    const backupsBefore = readdirSync(vps.confDir).sort();
    const nginxCalls = vps.runner.calls.length;
    const upserts = vps.route53.calls.filter((c) => c.action === 'UPSERT').length;

    const second = await publisher().execute(harness().ctx);
    expect(statuses(second.steps)).toEqual([
      'dns_upsert:skipped',
      'dns_wait:skipped',
      'bootstrap_vhost:skipped',
      'certificate:skipped',
      'final_vhost:skipped',
      'validate:done',
    ]);
    expect(second.steps.some((s) => s.status === 'failed')).toBe(false);
    expect(vps.files()).toEqual(filesBefore);
    expect(readdirSync(vps.confDir).sort()).toEqual(backupsBefore);
    expect(vps.runner.calls.length).toBe(nginxCalls); // no nginx -t, no reload, no certbot
    expect(vps.route53.calls.filter((c) => c.action === 'UPSERT').length).toBe(upserts);
    expect(second.certificateExpiresAt).toBe(vps.certExpiry.toISOString());
  });

  it('each step on its own is skipped when its work is already done', async () => {
    vps = new FakeVps();
    // DNS already right; a final vhost, certificate and renewal file already present.
    vps.dns.records.set(HOST, [FAKE_VPS_IP]);
    await publisher().execute(harness().ctx);
    vps.dns.records.delete(HOST); // only DNS is lost
    vps.route53.zone.delete(HOST);
    const again = await publisher().execute(harness().ctx);
    expect(statuses(again.steps).slice(0, 3)).toEqual(['dns_upsert:done', 'dns_wait:done', 'bootstrap_vhost:skipped']);
  });

  it('never downgrades an existing final vhost to the bootstrap file', async () => {
    vps = new FakeVps();
    await publisher().execute(harness().ctx);
    const final = vps.files()[`${HOST}.conf`]!;
    await publisher().execute(harness().ctx);
    expect(vps.files()[`${HOST}.conf`]).toBe(final);
  });
});

describe('publish: rollback', () => {
  it('an injected nginx -t failure on the final vhost restores the previous vhost and nginx keeps serving', async () => {
    vps = new FakeVps();
    vps.nginxTestFails = (files) => Object.values(files).some((text) => text.includes('listen 443'));
    const h = harness();
    const result = await publisher().execute(h.ctx);
    expect(statuses(result.steps)).toEqual([
      'dns_upsert:done',
      'dns_wait:done',
      'bootstrap_vhost:done',
      'certificate:done',
      'final_vhost:failed',
    ]);
    expect(result.steps.at(-1)!.detail).toContain('previous file was restored');
    // The bootstrap file is back, byte for byte, and the last two calls are a successful test and a reload.
    const restored = vps.files()[`${HOST}.conf`]!;
    expect(restored).not.toContain('listen 443');
    expect(restored).toContain(`server_name ${HOST};`);
    const tail = vps.runner.calls.slice(-2).map((c) => c.args.slice(2).join(' '));
    expect(tail).toEqual(['nginx -t', 'nginx -s reload']);
    expect(result.certificateExpiresAt).toBe(vps.certExpiry.toISOString());
    expect(vps.http.calls).toEqual([]); // later steps did not run
  });

  it('a failing bootstrap vhost leaves no file and stops the run', async () => {
    vps = new FakeVps();
    vps.nginxTestFails = (files) => Object.keys(files).length > 0;
    const result = await publisher().execute(harness().ctx);
    expect(statuses(result.steps)).toEqual(['dns_upsert:done', 'dns_wait:done', 'bootstrap_vhost:failed']);
    expect(vps.files()).toEqual({});
  });
});

describe('publish: failures are journalled, not thrown', () => {
  it('Route53 refusing the change', async () => {
    vps = new FakeVps();
    vps.route53.refuse = 'AccessDenied';
    const result = await publisher().execute(harness().ctx);
    expect(statuses(result.steps)).toEqual(['dns_upsert:failed']);
    expect(result.steps[0]!.detail).toContain('AccessDenied');
  });

  it('INSYNC that never comes', async () => {
    vps = new FakeVps();
    vps.route53.insyncAfter = 10_000;
    const result = await publisher().execute(harness().ctx);
    expect(statuses(result.steps)).toEqual(['dns_upsert:done', 'dns_wait:failed']);
    expect(result.steps[1]!.detail).toContain('INSYNC');
  });

  it('resolvers that never answer', async () => {
    vps = new FakeVps();
    vps.dns.lag = 10_000;
    const result = await publisher().execute(harness().ctx);
    expect(statuses(result.steps)).toEqual(['dns_upsert:done', 'dns_wait:failed']);
    expect(result.steps[1]!.detail).toContain('resolvers');
  });

  it('a VPS hostname that does not resolve', async () => {
    vps = new FakeVps();
    vps.dns.records.delete('ai.marin.cr');
    const result = await publisher().execute(harness().ctx);
    expect(statuses(result.steps)).toEqual(['dns_upsert:failed']);
    expect(vps.route53.calls).toEqual([]);
  });

  it('certbot failing', async () => {
    vps = new FakeVps();
    vps.certbotFails = true;
    const result = await publisher().execute(harness().ctx);
    expect(statuses(result.steps).at(-1)).toBe('certificate:failed');
  });

  it('a missing certificate contact is a certificate failure with a fixed message', async () => {
    vps = new FakeVps();
    const executor = new VpsPublishExecutor(() => ({ ...vps.deps(), certbot: () => vps.deps({ email: 'not an email' }).certbot() }));
    const result = await executor.execute(harness().ctx);
    expect(statuses(result.steps).at(-1)).toBe('certificate:failed');
  });

  it('/health not answering 200', async () => {
    vps = new FakeVps();
    vps.http.health = 502;
    const result = await publisher().execute(harness().ctx);
    expect(result.steps.at(-1)).toMatchObject({ name: 'validate', status: 'failed' });
    expect(vps.http.calls.filter((c) => c.endsWith('/health'))).toHaveLength(3);
    expect(vps.files()[`${HOST}.conf`]).toContain('listen 443 ssl;'); // a working vhost is not torn down for a node that is down
  });

  it('DANGER: an unauthenticated /v1/models answering 200 takes the vhost out of service', async () => {
    vps = new FakeVps();
    vps.http.models = 200;
    const result = await publisher().execute(harness().ctx);
    const last = result.steps.at(-1)!;
    expect(last).toMatchObject({ name: 'validate', status: 'failed' });
    expect(last.detail).toContain('DANGER');
    expect(vps.files()).toEqual({});
    expect(readdirSync(vps.confDir).some((n) => n.includes('.removed.'))).toBe(true);
  });

  it('/v1/models answering something other than 401 fails', async () => {
    vps = new FakeVps();
    vps.http.models = 500;
    const result = await publisher().execute(harness().ctx);
    expect(result.steps.at(-1)).toMatchObject({ name: 'validate', status: 'failed' });
  });

  it('stops when the worker is draining', async () => {
    vps = new FakeVps();
    const controller = new AbortController();
    controller.abort();
    const h = harness();
    await expect(publisher().execute({ ...h.ctx, signal: controller.signal })).rejects.toThrow(/stopped/);
  });
});

describe('unpublish', () => {
  it('removes the vhost, deletes the A record and leaves the certificate; running it again is a no-op', async () => {
    vps = new FakeVps();
    await publisher().execute(harness().ctx);
    const first = await unpublisher().execute(harness().ctx);
    expect(first.host).toBe(HOST);
    expect(statuses(first.steps)).toEqual(['vhost_remove:done', 'dns_delete:done', 'certificate_kept:skipped']);
    expect(vps.files()).toEqual({});
    expect(vps.route53.zone.has(HOST)).toBe(false);
    expect(readFileSync(join(vps.proxyDir, 'letsencrypt', 'live', HOST, 'fullchain.pem'), 'utf8')).toBe('fake-chain');
    const reloads = vps.runner.calls.filter((c) => c.args.includes('reload')).length;

    const second = await unpublisher().execute(harness().ctx);
    expect(statuses(second.steps)).toEqual(['vhost_remove:skipped', 'dns_delete:skipped', 'certificate_kept:skipped']);
    expect(vps.runner.calls.filter((c) => c.args.includes('reload')).length).toBe(reloads);
  });

  it('restores the files when nginx -t fails and stops before touching DNS', async () => {
    vps = new FakeVps();
    await publisher().execute(harness().ctx);
    vps.nginxTestFails = (files) => Object.keys(files).length === 0;
    const result = await unpublisher().execute(harness().ctx);
    expect(statuses(result.steps)).toEqual(['vhost_remove:failed']);
    expect(vps.files()[`${HOST}.conf`]).toContain('listen 443 ssl;');
    expect(vps.route53.zone.has(HOST)).toBe(true);
  });

  it('a record that changed since is reported, never forced', async () => {
    vps = new FakeVps();
    await publisher().execute(harness().ctx);
    vps.route53.mismatch = true;
    const result = await unpublisher().execute(harness().ctx);
    expect(statuses(result.steps)).toEqual(['vhost_remove:done', 'dns_delete:failed']);
    expect(result.steps[1]!.detail).toContain('by hand');
  });

  it('needs no tailnet fields', async () => {
    vps = new FakeVps();
    const result = await unpublisher().execute(harness({ material: { tailnetIp: undefined, tailnetMachine: undefined, servePattern: undefined } }).ctx);
    expect(result.steps.at(-1)!.status).not.toBe('failed');
  });
});

describe('refusals before any work', () => {
  const rejectsWithoutWork = async (h: Harness, pattern: RegExp) => {
    await expect(publisher().execute(h.ctx)).rejects.toThrow(pattern);
    expect(vps.runner.calls).toEqual([]);
    expect(vps.route53.calls).toEqual([]);
    expect(vps.http.calls).toEqual([]);
  };

  it.each([
    ['extra params', { nodeId: 'ai-n3', host: 'evil.test' }],
    ['no params', {}],
    ['a bad node id', { nodeId: 'ai-n3; rm -rf /' }],
    ['a path as the node id', { nodeId: '../../etc' }],
    ['a non-string node id', { nodeId: 3 }],
  ])('params: %s', async (_label, params) => {
    vps = new FakeVps();
    await rejectsWithoutWork(harness({ params }), /node id|parameters/);
  });

  it.each([
    ['a host that is not this node\'s', { host: 'ai-n4-staging.marin.cr' }, /brokered host/],
    ['a host with another suffix', { host: 'ai-n3-other.marin.cr' }, /brokered host/],
    ['a suffix that is not the configured one', { stagingSuffix: '' }, /brokered host/],
    ['an invalid suffix', { stagingSuffix: 'staging', host: 'ai-n3staging.marin.cr' }, /suffix/],
    ['a malformed key id', { route53AccessKeyId: 'nope' }, /Route53 key/],
    ['a malformed secret', { route53SecretAccessKey: 'short' }, /Route53 key/],
    ['a bad zone id', { hostedZoneId: '../x' }, /hosted zone/],
    ['a non-tailnet address', { tailnetIp: '10.0.0.5' }, /tailnet address/],
    ['an address that could end an nginx directive', { tailnetIp: `${ip4(100, 100, 1, 1)}; }` }, /tailnet address/],
    ['a machine name with a newline', { tailnetMachine: 'a\nserver {' }, /machine name/],
    ['Pattern B with a short machine name', { tailnetMachine: 'dell-linux' }, /full tailnet name/],
    ['a serve pattern of none', { servePattern: 'none' }, /serve pattern/],
    ['a bad VPS hostname', { vpsPublicHostname: '203.0.113.9' }, /public hostname/],
  ])('secret: %s', async (_label, material, pattern) => {
    vps = new FakeVps();
    await rejectsWithoutWork(harness({ material }), pattern as RegExp);
  });

  it('a credential of another kind', async () => {
    vps = new FakeVps();
    await rejectsWithoutWork(harness({ kind: 'postgres.readonly' }), /not a VPS Route53/);
  });

  it('a control plane that refuses the secret', async () => {
    vps = new FakeVps();
    const h = harness();
    const ctx = { ...h.ctx, api: { jobSecret: async () => Promise.reject(new Error(`403 ${h.secrets.route53AccessKeyId}`)) } } as unknown as JobExecutionContext;
    const error = (await publisher().execute(ctx).catch((e: unknown) => e)) as Error;
    expect(error.message).toMatch(/did not issue/);
    expect(error.message).not.toContain(h.secrets.route53AccessKeyId);
  });

  it('parseParams and parseSecret are exported for the API contract tests', () => {
    expect(parseParams({ nodeId: 'ai-n12' })).toEqual({ nodeId: 'ai-n12' });
    expect(() => parseSecret(null, 'ai-n3', 'publish')).toThrow();
  });
});

describe('secret tripwire: no tailnet address, key or zone in the result, the logs or the error', () => {
  function leaks(h: Harness): string[] {
    return [h.secrets.tailnetIp, h.secrets.tailnetMachine, h.secrets.tailnetMachine.split('.')[1]!, h.secrets.route53AccessKeyId, h.secrets.route53SecretAccessKey, h.secrets.hostedZoneId, FAKE_VPS_IP];
  }

  it('across a full publish, a failed publish and an unpublish', async () => {
    for (const scenario of ['ok', 'nginx', 'route53', 'danger'] as const) {
      vps = new FakeVps();
      if (scenario === 'nginx') vps.nginxTestFails = (files) => Object.values(files).some((t) => t.includes('listen 443'));
      if (scenario === 'route53') vps.route53.refuse = 'AccessDenied';
      if (scenario === 'danger') vps.http.models = 200;
      const h = harness();
      const result = await publisher().execute(h.ctx);
      const text = JSON.stringify(result) + h.logs.join('\n');
      for (const secret of leaks(h)) expect(text).not.toContain(secret);
      if (scenario === 'ok') {
        const u = harness();
        const out = JSON.stringify(await unpublisher().execute(u.ctx)) + u.logs.join('\n');
        for (const secret of leaks(u)) expect(out).not.toContain(secret);
      }
      vps.dispose();
    }
  });

  it('no subprocess argument and no Route53 call carries a secret', async () => {
    vps = new FakeVps();
    const h = harness();
    await publisher().execute(h.ctx);
    const argv = JSON.stringify(vps.runner.calls);
    for (const secret of [h.secrets.route53AccessKeyId, h.secrets.route53SecretAccessKey, h.secrets.tailnetIp, h.secrets.tailnetMachine]) expect(argv).not.toContain(secret);
    expect(vps.runner.calls.every((c) => c.file === 'docker' || c.file === 'dig')).toBe(true);
  });

  it('validation errors never echo the offending value', async () => {
    vps = new FakeVps();
    const h = harness({ material: { tailnetIp: `${ip4(100, 100, 9, 9)}; evil` } });
    const error = (await publisher().execute(h.ctx).catch((e: unknown) => e)) as Error;
    expect(error.message).not.toContain('evil');
    expect(error.message).not.toContain(ip4(100, 100, 9, 9));
  });

  it('the journal details are scrubbed even if a step wrote an address', async () => {
    vps = new FakeVps();
    vps.dns.records.delete('ai.marin.cr');
    const result = await publisher().execute(harness().ctx);
    expect(JSON.stringify(result)).not.toMatch(/\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b/);
  });
});

describe('the result matches the API contract shape', () => {
  it('has only host, steps and certificateExpiresAt, each step only name, status and detail', async () => {
    vps = new FakeVps();
    const result = await publisher().execute(harness().ctx);
    expect(Object.keys(result).sort()).toEqual(['certificateExpiresAt', 'host', 'steps']);
    for (const step of result.steps) {
      expect(Object.keys(step).sort()).toEqual(['detail', 'name', 'status']);
      expect(step.detail.length).toBeLessThanOrEqual(500);
    }
    writeFileSync(join(vps.proxyDir, 'result.json'), JSON.stringify(result)); // proves it is plain JSON
  });
});
