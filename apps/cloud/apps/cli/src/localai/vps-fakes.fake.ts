// Test doubles for the VPS stack-agent: a fake "VPS" (a temporary proxy directory, a fake `docker`, a fake certbot, a
// fake resolver, a fake Route53, a fake HTTP probe) behind the executor's ports. Nothing here starts a process, calls
// AWS or touches a real host. Not part of the build (tsconfig.build.json excludes *.fake.ts).
import { randomBytes } from 'node:crypto';
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { Certbot, type CertificateReader } from './certbot.js';
import { NginxProxy } from './nginx.js';
import { Route53Error, type ChangeStatus, type Route53Client } from './route53.js';
import type { DnsChecker } from './vps-dns.js';
import type { VpsDeps } from './vps-publish.executor.js';
import type { HttpProbe } from './vps-http.js';
import type { CommandResult, CommandRunner } from './vps-process.js';

export const FAKE_VPS_NAME = 'ai.marin.cr';
/** A documentation-range address (RFC 5737 TEST-NET-3), built from parts. */
export const FAKE_VPS_IP = ['203', '0', '113', '10'].join('.');

/** Test-only values built at run time, so no key-shaped literal sits in the repository. */
export function fakeSecretValues() {
  const upper = (n: number) => randomBytes(n).toString('hex').toUpperCase().slice(0, n);
  return {
    route53AccessKeyId: `AKIA${upper(16)}`,
    route53SecretAccessKey: randomBytes(30).toString('base64').replace(/[^A-Za-z0-9/+]/g, 'a').slice(0, 40),
    hostedZoneId: `Z${upper(14)}`,
    tailnetIp: `100.${64 + (randomBytes(1)[0]! % 60)}.${randomBytes(1)[0]!}.${1 + (randomBytes(1)[0]! % 250)}`,
    tailnetMachine: `dell-linux.tail${randomBytes(3).toString('hex')}.ts.net`,
  };
}

export class FakeRunner implements CommandRunner {
  readonly calls: Array<{ file: string; args: string[] }> = [];
  constructor(private readonly handler: (file: string, args: readonly string[]) => CommandResult) {}
  async run(file: string, args: readonly string[]): Promise<CommandResult> {
    this.calls.push({ file, args: [...args] });
    return this.handler(file, args);
  }
}

const ok = (stdout = ''): CommandResult => ({ exitCode: 0, stdout, stderr: '' });
const fail = (): CommandResult => ({ exitCode: 1, stdout: '', stderr: 'boom' });

export class FakeDns implements DnsChecker {
  /** name -> addresses every resolver answers. */
  readonly records = new Map<string, string[]>([[FAKE_VPS_NAME, [FAKE_VPS_IP]]]);
  readonly lookups: Array<{ name: string; resolver: string }> = [];
  /** Hosts whose new record becomes visible only after this many lookups (propagation delay). */
  lag = 0;
  private pending = new Map<string, { ips: string[]; visibleAfter: number }>();
  private seen = 0;

  async resolveA(name: string, resolver: string): Promise<string[]> {
    this.lookups.push({ name, resolver });
    this.seen += 1;
    const pending = this.pending.get(name);
    if (pending && this.seen >= pending.visibleAfter) {
      this.records.set(name, pending.ips);
      this.pending.delete(name);
    }
    return this.records.get(name) ?? [];
  }

  set(name: string, ips: string[]): void {
    this.pending.set(name, { ips, visibleAfter: this.seen + this.lag });
    if (this.lag === 0) {
      this.records.set(name, ips);
      this.pending.delete(name);
    }
  }

  remove(name: string): void {
    this.records.delete(name);
    this.pending.delete(name);
  }
}

export class FakeRoute53 implements Route53Client {
  readonly calls: Array<{ action: 'UPSERT' | 'DELETE' | 'GET'; host?: string; ip?: string }> = [];
  readonly zone = new Map<string, string>();
  insyncAfter = 0;
  refuse: string | null = null;
  mismatch = false;
  private polls = 0;

  constructor(private readonly dns: FakeDns) {}

  async upsertA(_zoneId: string, host: string, ip: string): Promise<{ changeId: string }> {
    this.calls.push({ action: 'UPSERT', host, ip });
    if (this.refuse) throw new Route53Error(this.refuse, 'refused');
    this.zone.set(host, ip);
    this.dns.set(host, [ip]);
    return { changeId: 'C123FAKE' };
  }

  async deleteA(_zoneId: string, host: string, ip: string): Promise<{ changeId: string } | 'absent'> {
    this.calls.push({ action: 'DELETE', host, ip });
    if (this.refuse) throw new Route53Error(this.refuse, 'refused');
    if (this.mismatch) throw new Route53Error('RecordValueMismatch', 'mismatch');
    if (!this.zone.has(host)) return 'absent';
    this.zone.delete(host);
    this.dns.remove(host);
    return { changeId: 'C456FAKE' };
  }

  async getChange(): Promise<ChangeStatus> {
    this.calls.push({ action: 'GET' });
    this.polls += 1;
    return this.polls > this.insyncAfter ? 'INSYNC' : 'PENDING';
  }
}

export class FakeHttp implements HttpProbe {
  readonly calls: string[] = [];
  health = 200;
  models = 401;
  async status(host: string, path: '/health' | '/v1/models'): Promise<number> {
    this.calls.push(`${host}${path}`);
    return path === '/health' ? this.health : this.models;
  }
}

/** A whole fake VPS around one temporary proxy directory. */
export class FakeVps {
  readonly proxyDir = mkdtempSync(join(tmpdir(), 'vps-agent-test-'));
  readonly confDir = join(this.proxyDir, 'nginx', 'conf.d');
  readonly dns = new FakeDns();
  readonly route53 = new FakeRoute53(this.dns);
  readonly http = new FakeHttp();
  readonly sleeps: number[] = [];
  clock = new Date('2026-11-04T12:00:00Z').getTime();
  /** When it returns true for the current files, `nginx -t` fails. */
  nginxTestFails: (files: Record<string, string>) => boolean = () => false;
  certbotFails = false;
  /** Whether certbot writes a renewal file that uses the webroot. */
  certbotRenewalOk = true;
  readonly certExpiry = new Date('2027-02-01T00:00:00Z');

  readonly runner = new FakeRunner((file, args) => this.handle(file, args));
  readonly reader: CertificateReader = {
    notAfter: async (path) => {
      try {
        readFileSync(path);
        return this.certExpiry;
      } catch {
        return null;
      }
    },
  };

  constructor() {
    mkdirSync(this.confDir, { recursive: true });
  }

  files(): Record<string, string> {
    const out: Record<string, string> = {};
    for (const name of readdirSync(this.confDir)) if (name.endsWith('.conf')) out[name] = readFileSync(join(this.confDir, name), 'utf8');
    return out;
  }

  private handle(file: string, args: readonly string[]): CommandResult {
    if (file === 'docker' && args[0] === 'exec') {
      if (args[2] === 'nginx' && args[3] === '-t') return this.nginxTestFails(this.files()) ? fail() : ok();
      if (args[2] === 'nginx' && args[3] === '-s' && args[4] === 'reload') return ok();
    }
    if (file === 'docker' && args[0] === 'run') {
      if (this.certbotFails) return fail();
      const host = args[args.indexOf('-d') + 1]!;
      const live = join(this.proxyDir, 'letsencrypt', 'live', host);
      mkdirSync(live, { recursive: true });
      writeFileSync(join(live, 'fullchain.pem'), 'fake-chain');
      writeFileSync(join(live, 'privkey.pem'), 'fake-key');
      mkdirSync(join(this.proxyDir, 'letsencrypt', 'renewal'), { recursive: true });
      writeFileSync(
        join(this.proxyDir, 'letsencrypt', 'renewal', `${host}.conf`),
        this.certbotRenewalOk ? 'authenticator = webroot\n[[webroot_map]]\nwebroot_path = /var/www/certbot\n' : 'authenticator = standalone\n',
      );
      return ok();
    }
    return fail();
  }

  deps(options: { email?: string } = {}): VpsDeps {
    const nginx = new NginxProxy({ runner: this.runner, proxyDir: this.proxyDir, container: 'proxy-nginx', now: () => new Date(this.clock) });
    return {
      dns: this.dns,
      http: this.http,
      route53: () => this.route53,
      nginx,
      certbot: () => new Certbot({ runner: this.runner, proxyDir: this.proxyDir, email: options.email ?? 'ops@example.test', reader: this.reader, now: () => new Date(this.clock) }),
      sleep: async (ms) => {
        this.sleeps.push(ms);
        this.clock += ms;
      },
      now: () => new Date(this.clock),
      timing: { changeWaitMs: 60_000, dnsWaitMs: 60_000, pollMs: 5_000, healthTries: 3, healthPauseMs: 1_000 },
    };
  }

  dispose(): void {
    rmSync(this.proxyDir, { recursive: true, force: true });
  }
}
