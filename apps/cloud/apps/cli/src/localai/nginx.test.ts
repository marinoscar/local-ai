import { readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

import { afterEach, describe, expect, it } from 'vitest';

import { NginxProxy, NginxRefusedError } from './nginx.js';
import { FakeVps } from './vps-fakes.fake.js';
import { VpsInputError } from './vps-host.js';

const HOST = 'ai-n3-staging.marin.cr';
let vps: FakeVps;
afterEach(() => vps?.dispose());

const proxy = () => new NginxProxy({ runner: vps.runner, proxyDir: vps.proxyDir, container: 'proxy-nginx', now: () => new Date(vps.clock) });
const execs = () => vps.runner.calls.filter((c) => c.file === 'docker').map((c) => c.args.slice(2).join(' '));

describe('NginxProxy.apply', () => {
  it('writes, tests and reloads; a second identical apply changes nothing', async () => {
    vps = new FakeVps();
    expect(await proxy().apply(HOST, 'server { listen 80; }\n')).toBe('applied');
    expect(execs()).toEqual(['nginx -t', 'nginx -s reload']);
    const before = vps.runner.calls.length;
    expect(await proxy().apply(HOST, 'server { listen 80; }\n')).toBe('unchanged');
    expect(vps.runner.calls.length).toBe(before); // no test, no reload
  });

  it('backs up the previous file to <file>.bak.<ts> before writing', async () => {
    vps = new FakeVps();
    writeFileSync(join(vps.confDir, `${HOST}.conf`), 'old\n');
    await proxy().apply(HOST, 'new\n');
    const names = readdirSync(vps.confDir);
    const backup = names.find((n) => n.startsWith(`${HOST}.conf.bak.`));
    expect(backup).toBeDefined();
    expect(readFileSync(join(vps.confDir, backup!), 'utf8')).toBe('old\n');
    expect(vps.files()[`${HOST}.conf`]).toBe('new\n');
  });

  it('rollback: a failed nginx -t restores the previous file, re-tests and reloads', async () => {
    vps = new FakeVps();
    writeFileSync(join(vps.confDir, `${HOST}.conf`), 'good\n');
    vps.nginxTestFails = (files) => Object.values(files).some((text) => text.includes('BROKEN'));
    const error = await proxy().apply(HOST, 'BROKEN\n').catch((e: unknown) => e);
    expect(error).toBeInstanceOf(NginxRefusedError);
    expect((error as NginxRefusedError).restored).toBe(true);
    expect(vps.files()[`${HOST}.conf`]).toBe('good\n');
    // test (fails), test again (passes), reload: nginx keeps serving the previous file
    expect(execs()).toEqual(['nginx -t', 'nginx -t', 'nginx -s reload']);
    expect(readdirSync(vps.confDir).filter((n) => n.includes('.tmp-'))).toEqual([]);
  });

  it('rollback of a first-time file removes it', async () => {
    vps = new FakeVps();
    vps.nginxTestFails = (files) => Object.values(files).some((text) => text.includes('BROKEN'));
    await expect(proxy().apply(HOST, 'BROKEN\n')).rejects.toBeInstanceOf(NginxRefusedError);
    expect(vps.files()).toEqual({});
  });

  it('reports restored=false when the proxy is still unhealthy after the restore', async () => {
    vps = new FakeVps();
    vps.nginxTestFails = () => true;
    const error = (await proxy().apply(HOST, 'x\n').catch((e: unknown) => e)) as NginxRefusedError;
    expect(error.restored).toBe(false);
  });

  it('keeps only the newest backups', async () => {
    vps = new FakeVps();
    const p = new NginxProxy({ runner: vps.runner, proxyDir: vps.proxyDir, container: 'proxy-nginx', keepBackups: 2, now: () => new Date(vps.clock) });
    for (let i = 0; i < 5; i += 1) {
      vps.clock += 1000;
      await p.apply(HOST, `v${i}\n`);
    }
    expect(readdirSync(vps.confDir).filter((n) => n.includes('.bak.'))).toHaveLength(2);
  });
});

describe('NginxProxy.remove', () => {
  it('moves the vhost and any bootstrap vhost aside, tests and reloads', async () => {
    vps = new FakeVps();
    writeFileSync(join(vps.confDir, `${HOST}.conf`), 'final\n');
    writeFileSync(join(vps.confDir, `${HOST}.bootstrap.conf`), 'boot\n');
    expect(await proxy().remove(HOST)).toBe('removed');
    expect(vps.files()).toEqual({});
    expect(readdirSync(vps.confDir).filter((n) => n.includes('.removed.'))).toHaveLength(2);
    expect(execs()).toEqual(['nginx -t', 'nginx -s reload']);
  });

  it('is a no-op when there is nothing to remove', async () => {
    vps = new FakeVps();
    expect(await proxy().remove(HOST)).toBe('absent');
    expect(vps.runner.calls).toHaveLength(0);
  });

  it('restores the files when nginx -t fails', async () => {
    vps = new FakeVps();
    writeFileSync(join(vps.confDir, `${HOST}.conf`), 'final\n');
    vps.nginxTestFails = (files) => Object.keys(files).length === 0;
    const error = (await proxy().remove(HOST).catch((e: unknown) => e)) as NginxRefusedError;
    expect(error).toBeInstanceOf(NginxRefusedError);
    expect(error.restored).toBe(true);
    expect(vps.files()[`${HOST}.conf`]).toBe('final\n');
  });
});

describe('input checks', () => {
  it('refuses a host outside the pattern before touching the disk or a process', async () => {
    vps = new FakeVps();
    await expect(proxy().apply('../../etc/passwd', 'x')).rejects.toBeInstanceOf(VpsInputError);
    await expect(proxy().remove('evil.example.test')).rejects.toBeInstanceOf(VpsInputError);
    expect(vps.runner.calls).toHaveLength(0);
  });

  it('refuses a container name that reads as an option and a relative proxy dir', () => {
    vps = new FakeVps();
    expect(() => new NginxProxy({ runner: vps.runner, proxyDir: vps.proxyDir, container: '-v /:/x' })).toThrow(VpsInputError);
    expect(() => new NginxProxy({ runner: vps.runner, proxyDir: 'relative', container: 'proxy-nginx' })).toThrow(VpsInputError);
    expect(() => new NginxProxy({ runner: vps.runner, proxyDir: '/opt/../etc', container: 'proxy-nginx' })).toThrow(VpsInputError);
  });

  it('every subprocess is docker with a fixed argument array', async () => {
    vps = new FakeVps();
    await proxy().apply(HOST, 'x\n');
    for (const call of vps.runner.calls) {
      expect(call.file).toBe('docker');
      expect(call.args.slice(0, 3)).toEqual(['exec', 'proxy-nginx', 'nginx']);
    }
  });
});
