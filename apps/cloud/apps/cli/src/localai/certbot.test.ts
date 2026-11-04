import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

import { afterEach, describe, expect, it } from 'vitest';

import { Certbot, CertbotRefusedError } from './certbot.js';
import { FakeVps } from './vps-fakes.fake.js';
import { VpsInputError } from './vps-host.js';

const HOST = 'ai-n3-staging.marin.cr';
let vps: FakeVps;
afterEach(() => vps?.dispose());

const certbot = (email = 'ops@example.test') => new Certbot({ runner: vps.runner, proxyDir: vps.proxyDir, email, reader: vps.reader, now: () => new Date(vps.clock) });

describe('Certbot.ensure', () => {
  it('issues with the runbook command as a fixed argument array', async () => {
    vps = new FakeVps();
    const result = await certbot().ensure(HOST);
    expect(result.outcome).toBe('issued');
    expect(result.expiresAt).toEqual(vps.certExpiry);
    const call = vps.runner.calls[0]!;
    expect(call.file).toBe('docker');
    expect(call.args).toEqual([
      'run', '--rm',
      '-v', `${vps.proxyDir}/letsencrypt:/etc/letsencrypt`,
      '-v', `${vps.proxyDir}/webroot:/var/www/certbot`,
      'certbot/certbot:latest', 'certonly', '--webroot', '-w', '/var/www/certbot',
      '-d', HOST, '--email', 'ops@example.test', '--agree-tos', '--no-eff-email', '--non-interactive',
    ]);
  });

  it('is a no-op when a healthy certificate with working renewal settings exists', async () => {
    vps = new FakeVps();
    await certbot().ensure(HOST);
    const calls = vps.runner.calls.length;
    expect((await certbot().ensure(HOST)).outcome).toBe('present');
    expect(vps.runner.calls.length).toBe(calls);
  });

  it('renews when the certificate is close to its end', async () => {
    vps = new FakeVps();
    await certbot().ensure(HOST);
    vps.clock = vps.certExpiry.getTime() - 5 * 24 * 60 * 60 * 1000;
    expect((await certbot().ensure(HOST)).outcome).toBe('issued');
  });

  it('issues again when the renewal file does not use the shared webroot, then refuses if it still does not', async () => {
    vps = new FakeVps();
    vps.certbotRenewalOk = false;
    await expect(certbot().ensure(HOST)).rejects.toBeInstanceOf(CertbotRefusedError);
  });

  it('refuses when certbot fails, with a fixed message', async () => {
    vps = new FakeVps();
    vps.certbotFails = true;
    const error = (await certbot().ensure(HOST).catch((e: unknown) => e)) as Error;
    expect(error).toBeInstanceOf(CertbotRefusedError);
    expect(error.message).not.toContain('boom'); // the child's output is never passed on
  });

  it('does not treat a key without a readable certificate as present', async () => {
    vps = new FakeVps();
    const live = join(vps.proxyDir, 'letsencrypt', 'live', HOST);
    mkdirSync(live, { recursive: true });
    writeFileSync(join(live, 'privkey.pem'), 'k');
    expect(await certbot().expiresAt(HOST)).toBeNull();
  });
});

describe('input checks', () => {
  it('refuses a bad contact, host or image before any process', async () => {
    vps = new FakeVps();
    expect(() => certbot('not an email')).toThrow(VpsInputError);
    expect(() => new Certbot({ runner: vps.runner, proxyDir: vps.proxyDir, email: 'a@example.test', image: '--privileged x' })).toThrow(VpsInputError);
    await expect(certbot().ensure('evil.example.test')).rejects.toBeInstanceOf(VpsInputError);
    expect(vps.runner.calls).toHaveLength(0);
  });
});
