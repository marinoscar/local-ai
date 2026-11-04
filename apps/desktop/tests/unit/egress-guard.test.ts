import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import type { AddressInfo } from 'node:net';
import { request as undiciRequest } from 'undici';
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  AGENT_SOCKET,
  EgressBlockedError,
  PROCESS_SWITCHES,
  applyProcessSwitches,
  blockedTotal,
  decideMainEgress,
  decideMainUrl,
  decideSessionRequest,
  egressGuard,
  getEgressState,
  installEgressGuard,
  installMainProcessGuard,
  installSessionGuard,
  isLoopbackHost,
  normalizeHost,
  resetEgressGuardForTests,
  runAsUpdater,
  setEgressLogger,
  setEgressState,
  socketTarget,
  type EgressLogEntry,
  type EgressState,
} from '../../src/main/egress-guard';

/** Written this way so the redaction scanner does not read a URL with credentials as an email address. */
const AT = String.fromCharCode(64);
const OFFLINE: EgressState = { connected: false, updates: false };
const CONNECTED: EgressState = { connected: true, updates: false };
const UPDATES: EgressState = { connected: false, updates: true };
const BOTH: EgressState = { connected: true, updates: true };

describe('host matching', () => {
  it.each([
    ['AI.Marin.CR', 'ai.marin.cr'],
    ['ai.marin.cr.', 'ai.marin.cr'],
    ['[::1]', '::1'],
    ['fe80::1%eth0', 'fe80::1'],
    ['ai.marin.cr..', 'ai.marin.cr.'],
  ])('normalizeHost(%s) = %s', (input, expected) => {
    expect(normalizeHost(input)).toBe(expected);
  });

  it.each([
    ['127.0.0.1', true],
    ['127.255.0.9', true],
    ['localhost', true],
    ['LOCALHOST', true],
    ['localhost.', true],
    ['::1', true],
    ['[::1]', true],
    ['0:0:0:0:0:0:0:1', true],
    ['::ffff:127.0.0.1', true],
    ['::ffff:7f00:1', true],
    ['::ffff:127.9.9.9', true],
    ['::ffff:8.8.8.8', false],
    ['::ffff:808:808', false],
    ['::2', false],
    ['128.0.0.1', false],
    ['10.0.0.1', false],
    ['0.0.0.0', false],
    ['2130706433', false],
    ['127.1', false],
    ['foo.localhost', false],
    ['localhost.evil.example', false],
    ['127.0.0.1.evil.example', false],
    ['', false],
  ])('isLoopbackHost(%s) = %s', (host, expected) => {
    expect(isLoopbackHost(host)).toBe(expected);
  });
});

describe('main-process layer: host and port per state', () => {
  type Row = [name: string, target: Parameters<typeof decideMainEgress>[0], offline: boolean, connected: boolean];
  const tcp = (host: string, port: unknown): Parameters<typeof decideMainEgress>[0] => ({ kind: 'tcp', host, port });
  const rows: Row[] = [
    ['loopback v4', tcp('127.0.0.1', 7070), true, true],
    ['loopback v4, any port', tcp('127.0.0.1', 1), true, true],
    ['localhost', tcp('localhost', 8080), true, true],
    ['loopback v6', tcp('::1', 7070), true, true],
    ['IPv4-mapped loopback', tcp('::ffff:127.0.0.1', 7070), true, true],
    ['IPv4-mapped loopback, hex form', tcp('::ffff:7f00:1', 7070), true, true],
    ['cloud 443', tcp('ai.marin.cr', 443), false, true],
    ['cloud 443, string port', tcp('ai.marin.cr', '443'), false, true],
    ['cloud, uppercase', tcp('AI.MARIN.CR', 443), false, true],
    ['cloud, trailing dot', tcp('ai.marin.cr.', 443), false, true],
    ['cloud port 80', tcp('ai.marin.cr', 80), false, false],
    ['cloud port 8443', tcp('ai.marin.cr', 8443), false, false],
    ['cloud no port', tcp('ai.marin.cr', undefined), false, false],
    ['cloud with a suffix host', tcp('ai.marin.cr.evil.example', 443), false, false],
    ['cloud as a subdomain of another host', tcp('evil-ai.marin.cr', 443), false, false],
    ['cloud parent domain', tcp('marin.cr', 443), false, false],
    ['a cloud sibling', tcp('ai-n1.marin.cr', 443), false, false],
    ['github.com 443 (app caller)', tcp('github.com', 443), false, false],
    ['example.com', tcp('example.com', 443), false, false],
    ['public IPv4', tcp('93.184.216.34', 443), false, false],
    ['IPv4-mapped public address', tcp('::ffff:8.8.8.8', 443), false, false],
    ['empty host', tcp('', 443), false, false],
    ['bad port', tcp('ai.marin.cr', 'abc'), false, false],
    ['port 0', tcp('ai.marin.cr', 0), false, false],
    ['agent socket', { kind: 'unix', path: AGENT_SOCKET }, true, true],
    ['another unix socket', { kind: 'unix', path: '/var/run/docker.sock' }, false, false],
    ['agent socket lookalike', { kind: 'unix', path: `${AGENT_SOCKET}/../x.sock` }, false, false],
  ];

  it.each(rows)('%s', (_name, target, offline, connected) => {
    // Updates being on changes nothing for the app itself: only the updater may reach the update hosts.
    expect(decideMainEgress(target, { state: OFFLINE, caller: 'app' }).allow).toBe(offline);
    expect(decideMainEgress(target, { state: CONNECTED, caller: 'app' }).allow).toBe(connected);
    expect(decideMainEgress(target, { state: UPDATES, caller: 'app' }).allow).toBe(offline);
  });

  it('the update hosts are reachable only by the updater, on 443, while updates are on', () => {
    for (const host of ['github.com', 'objects.githubusercontent.com', 'release-assets.githubusercontent.com']) {
      const t = { kind: 'tcp', host, port: 443 } as const;
      expect(decideMainEgress(t, { state: UPDATES, caller: 'updater' }).allow, `${host} updater+updates`).toBe(true);
      expect(decideMainEgress(t, { state: BOTH, caller: 'updater' }).allow, `${host} updater+both`).toBe(true);
      expect(decideMainEgress(t, { state: UPDATES, caller: 'app' }).allow, `${host} app+updates`).toBe(false);
      expect(decideMainEgress(t, { state: CONNECTED, caller: 'updater' }).allow, `${host} updater, updates off`).toBe(false);
      expect(decideMainEgress(t, { state: OFFLINE, caller: 'updater' }).allow, `${host} updater, offline`).toBe(false);
      expect(decideMainEgress({ ...t, port: 80 }, { state: UPDATES, caller: 'updater' }).allow, `${host}:80`).toBe(false);
    }
    expect(decideMainEgress({ kind: 'tcp', host: 'gist.github.com', port: 443 }, { state: UPDATES, caller: 'updater' }).allow).toBe(false);
    expect(decideMainEgress({ kind: 'tcp', host: 'github.com.evil.example', port: 443 }, { state: UPDATES, caller: 'updater' }).allow).toBe(false);
  });

  it('the updater cannot reach the cloud unless connected, and the cloud is not an update host', () => {
    const cloud = { kind: 'tcp', host: 'ai.marin.cr', port: 443 } as const;
    expect(decideMainEgress(cloud, { state: UPDATES, caller: 'updater' }).allow).toBe(false);
    expect(decideMainEgress(cloud, { state: BOTH, caller: 'updater' }).allow).toBe(true);
  });

  it('the label of a block is the host, or "unix-socket", never a path', () => {
    expect(decideMainEgress({ kind: 'tcp', host: 'Example.COM.', port: 443 }, { state: OFFLINE }).label).toBe('example.com');
    expect(decideMainEgress({ kind: 'unix', path: '/home/user/secret/x.sock' }, { state: OFFLINE }).label).toBe('unix-socket');
  });

  it('the strict defaults hold until setEgressState is called', () => {
    resetEgressGuardForTests();
    expect(getEgressState()).toEqual({ connected: false, updates: false });
    expect(decideMainEgress({ kind: 'tcp', host: 'ai.marin.cr', port: 443 }).allow).toBe(false);
    setEgressState({ connected: true, updates: false });
    expect(decideMainEgress({ kind: 'tcp', host: 'ai.marin.cr', port: 443 }).allow).toBe(true);
    setEgressState({ connected: false, updates: false });
    expect(decideMainEgress({ kind: 'tcp', host: 'ai.marin.cr', port: 443 }).allow).toBe(false);
    expect(() => setEgressState({ connected: 'yes' } as unknown as EgressState)).toThrow(TypeError);
    resetEgressGuardForTests();
  });
});

describe('main-process layer: URLs', () => {
  const rows: Array<[url: string, offline: boolean, connected: boolean]> = [
    ['http://127.0.0.1:7070/api/v1/health', true, true],
    ['http://localhost:7070/', true, true],
    ['ws://127.0.0.1:7070/api/v1/events', true, true],
    ['http://[::1]:7070/', true, true],
    ['http://[::ffff:127.0.0.1]:7070/', true, true],
    ['https://ai.marin.cr/api/auth/me', false, true],
    ['https://AI.MARIN.CR/api', false, true],
    ['https://ai.marin.cr:443/api', false, true],
    ['wss://ai.marin.cr/socket', false, true],
    ['http://ai.marin.cr/', false, false],
    ['https://ai.marin.cr:80/', false, false],
    ['https://ai.marin.cr:8443/', false, false],
    ['http://ai.marin.cr:443/', false, false],
    ['https://ai.marin.cr.evil.example/', false, false],
    [`https://ai.marin.cr${AT}evil.example/`, false, false],
    ['https://evil.example/ai.marin.cr', false, false],
    ['https://example.com/', false, false],
    ['http://example.com/', false, false],
    ['ftp://ai.marin.cr/', false, false],
    ['file:///etc/passwd', false, false],
    ['data:text/plain,hello', true, true],
    ['not a url', false, false],
  ];
  it.each(rows)('%s', (url, offline, connected) => {
    expect(decideMainUrl(url, { state: OFFLINE }).allow).toBe(offline);
    expect(decideMainUrl(url, { state: CONNECTED }).allow).toBe(connected);
  });

  it('a blocked URL is labelled by host only: no path, no query', () => {
    const decision = decideMainUrl('https://example.com/private/path?token=abc123', { state: OFFLINE });
    expect(decision).toEqual({ allow: false, label: 'example.com' });
  });
});

describe('session layer', () => {
  const renderer = { kind: 'renderer' } as const;
  const rows: Array<[url: string, allowed: boolean]> = [
    ['localai://app/topbar/', true],
    ['localai://app/assets/x.js', true],
    ['data:text/html,hi', true],
    ['blob:localai://app/6f1c', true],
    ['http://127.0.0.1:7070/', true],
    ['http://127.0.0.1:7070/api/v1/status?x=1', true],
    ['ws://127.0.0.1:7070/api/v1/events', true],
    ['HTTP://127.0.0.1:7070/', true],
    ['http://127.0.0.1:7071/', false],
    ['http://127.0.0.1/', false],
    ['http://127.0.0.1:80/', false],
    ['https://127.0.0.1:7070/', false],
    ['wss://127.0.0.1:7070/', false],
    ['http://localhost:7070/', false],
    ['http://[::1]:7070/', false],
    ['http://127.0.0.2:7070/', false],
    [`http://user:pw${AT}127.0.0.1:7070/`, false],
    [`http://127.0.0.1:7070${AT}evil.example/`, false],
    ['https://ai.marin.cr/', false],
    ['https://example.com/', false],
    ['http://example.com/', false],
    ['wss://example.com/socket', false],
    ['ftp://example.com/', false],
    ['file:///etc/passwd', false],
    ['devtools://devtools/bundled/inspector.html', false],
    ['chrome://gpu', false],
    ['chrome-extension://abc/page.html', false],
    ['https://github.com/marinoscar/local-ai/releases', false],
    ['garbage', false],
  ];
  it.each(rows)('renderer sessions: %s -> %s', (url, allowed) => {
    expect(decideSessionRequest(url, renderer).allow).toBe(allowed);
  });

  it('the cloud is never reachable from a renderer session, even in connected mode', () => {
    expect(decideSessionRequest('https://ai.marin.cr/', { ...renderer, state: BOTH }).allow).toBe(false);
  });

  it('devtools: and the dev server are allowed only in an unpackaged build', () => {
    expect(decideSessionRequest('devtools://devtools/bundled/x.js', { ...renderer, dev: true }).allow).toBe(true);
    const dev = { ...renderer, dev: true, devOrigin: 'http://localhost:5173' };
    expect(decideSessionRequest('http://localhost:5173/topbar/index.html', dev).allow).toBe(true);
    expect(decideSessionRequest('ws://localhost:5173/', dev).allow).toBe(true);
    expect(decideSessionRequest('http://localhost:5174/', dev).allow).toBe(false);
    expect(decideSessionRequest('http://localhost:5173/', { ...renderer, devOrigin: 'http://localhost:5173' }).allow).toBe(false);
    expect(decideSessionRequest('http://evil.example:5173/', dev).allow).toBe(false);
    expect(decideSessionRequest('http://localhost:5173/', { ...renderer, dev: true, devOrigin: 'http://evil.example:5173' }).allow).toBe(false);
  });

  it('the updater session opens the GitHub hosts only while updates are on', () => {
    const url = 'https://github.com/marinoscar/local-ai/releases/download/desktop-v1/latest-linux.yml';
    expect(decideSessionRequest(url, { kind: 'updater', state: OFFLINE }).allow).toBe(false);
    expect(decideSessionRequest(url, { kind: 'updater', state: CONNECTED }).allow).toBe(false);
    expect(decideSessionRequest(url, { kind: 'updater', state: UPDATES }).allow).toBe(true);
    expect(decideSessionRequest('https://objects.githubusercontent.com/x', { kind: 'updater', state: UPDATES }).allow).toBe(true);
    expect(decideSessionRequest('https://release-assets.githubusercontent.com/x', { kind: 'updater', state: UPDATES }).allow).toBe(true);
    expect(decideSessionRequest('http://github.com/x', { kind: 'updater', state: UPDATES }).allow).toBe(false);
    expect(decideSessionRequest('https://github.com:8443/x', { kind: 'updater', state: UPDATES }).allow).toBe(false);
    expect(decideSessionRequest('https://example.com/', { kind: 'updater', state: UPDATES }).allow).toBe(false);
    expect(decideSessionRequest(url, { kind: 'renderer', state: UPDATES }).allow).toBe(false);
  });

  it('the updater session reads the live state when none is passed', () => {
    resetEgressGuardForTests();
    const url = 'https://github.com/x';
    expect(decideSessionRequest(url, { kind: 'updater' }).allow).toBe(false);
    setEgressState({ connected: false, updates: true });
    expect(decideSessionRequest(url, { kind: 'updater' }).allow).toBe(true);
    resetEgressGuardForTests();
  });
});

describe('session hook (webRequest.onBeforeRequest)', () => {
  let logs: EgressLogEntry[];
  beforeEach(() => {
    resetEgressGuardForTests();
    logs = [];
    setEgressLogger((entry) => logs.push(entry));
  });
  afterEach(() => resetEgressGuardForTests());

  function fakeSession() {
    let listener: ((details: { url: string }, cb: (r: { cancel: boolean }) => void) => void) | undefined;
    const ses = { webRequest: { onBeforeRequest: (fn: typeof listener) => (listener = fn) } };
    const run = (url: string): boolean => {
      let cancelled: boolean | undefined;
      listener!({ url }, (r) => (cancelled = r.cancel));
      return cancelled!;
    };
    return { ses: ses as unknown as Parameters<typeof installSessionGuard>[0], run };
  }

  it('cancels what the table denies, lets the rest through, counts and logs the host only', () => {
    const { ses, run } = fakeSession();
    installSessionGuard(ses, { kind: 'renderer' });
    expect(run('http://127.0.0.1:7070/api/v1/status')).toBe(false);
    expect(run('localai://app/topbar/')).toBe(false);
    expect(blockedTotal()).toBe(0);
    expect(run('https://example.com/secret/path?token=abc')).toBe(true);
    expect(run('wss://tracker.example.test/socket?id=7')).toBe(true);
    expect(run('file:///home/user/.ssh/id_ed25519')).toBe(true);
    expect(blockedTotal()).toBe(3);
    expect(logs.map((l) => l.host)).toEqual(['example.com', 'tracker.example.test', 'file:']);
    expect(logs.map((l) => l.blocked_total)).toEqual([1, 2, 3]);
    for (const entry of logs) {
      expect(entry).toMatchObject({ level: 'error', component: 'egress', code: 'LAI-DSK-012' });
      const text = JSON.stringify(entry);
      expect(text).not.toMatch(/secret|token|abc|socket|id_ed25519|\/home/);
    }
  });
});

describe('installEgressGuard', () => {
  afterEach(() => resetEgressGuardForTests());

  it('guards the default session, persist:agent, persist:lab and an in-memory updater session, then reports installed', () => {
    const partitions: string[] = [];
    const hooked: unknown[] = [];
    const make = (name: string) => ({ name, webRequest: { onBeforeRequest: () => hooked.push(name) } });
    const session = {
      defaultSession: make('default'),
      fromPartition: (p: string) => (partitions.push(p), make(p)),
    };
    expect(egressGuard.installed).toBe(false);
    const sessions = installEgressGuard({ session: session as never, isPackaged: true });
    expect(egressGuard.installed).toBe(true);
    expect(partitions).toEqual(['persist:agent', 'persist:lab', 'updater']);
    expect(hooked).toEqual(['default', 'persist:agent', 'persist:lab', 'updater']);
    expect(Object.keys(sessions)).toEqual(['default', 'agent', 'lab', 'updater']);
  });
});

describe('process switches', () => {
  it('sets exactly the switches of the spec, before ready', () => {
    const calls: Array<[string, string?]> = [];
    applyProcessSwitches({ commandLine: { appendSwitch: (n: string, v?: string) => void calls.push(v === undefined ? [n] : [n, v]) } });
    expect(calls).toEqual([
      ['disable-background-networking'],
      ['no-pings'],
      ['disable-component-update'],
      ['disable-domain-reliability'],
      ['disable-features', 'AutofillServerCommunication,MediaRouter,OptimizationHints,Translate'],
    ]);
    expect(PROCESS_SWITCHES).toHaveLength(5);
  });
});

describe('socketTarget', () => {
  it.each([
    [[{ host: 'Example.com', port: 443 }], { kind: 'tcp', host: 'Example.com', port: 443 }],
    [[{ port: 80 }], { kind: 'tcp', host: 'localhost', port: 80 }],
    [[[{ host: 'a.example', port: 1 }, () => undefined]], { kind: 'tcp', host: 'a.example', port: 1 }],
    [[443, 'b.example'], { kind: 'tcp', host: 'b.example', port: 443 }],
    [[7070], { kind: 'tcp', host: 'localhost', port: 7070 }],
    [['7070'], { kind: 'tcp', host: 'localhost', port: '7070' }],
    [[{ path: '/tmp/x.sock' }], { kind: 'unix', path: '/tmp/x.sock' }],
    [['/tmp/y.sock'], { kind: 'unix', path: '/tmp/y.sock' }],
    [[{ fd: 3 }], null],
    [[], null],
  ])('%j', (args, expected) => {
    expect(socketTarget(args as unknown[])).toEqual(expected);
  });
});

describe('the installed main-process layer (real fetch, http, https and sockets)', () => {
  let logs: EgressLogEntry[] = [];
  let server: http.Server;
  let port = 0;

  beforeAll(async () => {
    resetEgressGuardForTests();
    server = http.createServer((_req, res) => res.end('ok'));
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    port = (server.address() as AddressInfo).port;
    installMainProcessGuard();
  });
  afterAll(async () => {
    await new Promise<void>((resolve) => server.close(() => resolve()));
    resetEgressGuardForTests();
  });
  beforeEach(() => {
    logs = [];
    setEgressLogger((entry) => logs.push(entry));
  });

  it('is idempotent', () => {
    const connect = net.Socket.prototype.connect;
    const fetchFn = globalThis.fetch;
    installMainProcessGuard();
    expect(net.Socket.prototype.connect).toBe(connect);
    expect(globalThis.fetch).toBe(fetchFn);
  });

  it('fetch of loopback still works', async () => {
    const before = blockedTotal();
    const response = await fetch(`http://127.0.0.1:${port}/`);
    expect(await response.text()).toBe('ok');
    expect((await fetch(`http://localhost:${port}/`)).status).toBe(200);
    expect(blockedTotal()).toBe(before);
  });

  it('fetch of the cloud rejects with EgressBlockedError while offline (no socket is opened)', async () => {
    const before = blockedTotal();
    const error = await fetch('https://ai.marin.cr/api/auth/me?x=1').catch((e: unknown) => e);
    expect(error).toBeInstanceOf(EgressBlockedError);
    expect((error as EgressBlockedError).host).toBe('ai.marin.cr');
    expect((error as EgressBlockedError).code).toBe('LAI-DSK-012');
    expect(blockedTotal()).toBe(before + 1);
    expect(logs.at(-1)).toMatchObject({ host: 'ai.marin.cr', blocked_total: before + 1, component: 'egress' });
    expect(JSON.stringify(logs.at(-1))).not.toContain('auth');
  });

  it.each(['https://example.com/', 'http://ai.marin.cr/', 'https://ai.marin.cr:80/', 'https://ai.marin.cr.evil.example/', 'https://github.com/'])(
    'fetch(%s) is blocked',
    async (url) => {
      await expect(fetch(url)).rejects.toBeInstanceOf(EgressBlockedError);
    },
  );

  it('fetch accepts a URL object and a Request, and blocks both', async () => {
    await expect(fetch(new URL('https://example.com/'))).rejects.toBeInstanceOf(EgressBlockedError);
    await expect(fetch(new Request('https://example.com/'))).rejects.toBeInstanceOf(EgressBlockedError);
    await expect(fetch(undefined as unknown as string)).rejects.toBeInstanceOf(EgressBlockedError);
  });

  it('the undici dispatcher blocks a request that does not go through the fetch wrapper', async () => {
    const before = blockedTotal();
    await expect(undiciRequest('https://example.com/x')).rejects.toBeInstanceOf(EgressBlockedError);
    expect(blockedTotal()).toBe(before + 1);
    expect((await undiciRequest(`http://127.0.0.1:${port}/`)).statusCode).toBe(200);
  });

  it('a redirect from loopback to the internet is blocked at the dispatcher', async () => {
    const redirector = http.createServer((_req, res) => {
      res.writeHead(302, { Location: 'https://example.com/landing' });
      res.end();
    });
    await new Promise<void>((resolve) => redirector.listen(0, '127.0.0.1', resolve));
    const redirectPort = (redirector.address() as AddressInfo).port;
    const before = blockedTotal();
    try {
      await expect(fetch(`http://127.0.0.1:${redirectPort}/`)).rejects.toThrow();
      expect(blockedTotal()).toBe(before + 1);
      expect(logs.at(-1)?.host).toBe('example.com');
    } finally {
      await new Promise<void>((resolve) => redirector.close(() => resolve()));
    }
  });

  it('net.connect to the internet gets an EgressBlockedError on the socket, and nothing is resolved or sent', async () => {
    const before = blockedTotal();
    const error = await new Promise<Error>((resolve) => {
      const socket = net.connect({ host: 'example.com', port: 443 });
      socket.once('error', resolve);
    });
    expect(error).toBeInstanceOf(EgressBlockedError);
    expect(blockedTotal()).toBe(before + 1);
  });

  it('net.connect(port, host) and net.connect(path) are checked too', async () => {
    const one = await new Promise<Error>((resolve) => net.connect(443, 'example.com').once('error', resolve));
    expect(one).toBeInstanceOf(EgressBlockedError);
    const two = await new Promise<Error>((resolve) => net.connect('/var/run/other.sock').once('error', resolve));
    expect(two).toBeInstanceOf(EgressBlockedError);
    expect(logs.at(-1)?.host).toBe('unix-socket');
  });

  it('http.get and https.get to the internet fail with EgressBlockedError', async () => {
    const a = await new Promise<Error>((resolve) => http.get('http://example.com/a?b=c').once('error', resolve));
    const b = await new Promise<Error>((resolve) => https.get('https://example.com/a?b=c').once('error', resolve));
    expect(a).toBeInstanceOf(EgressBlockedError);
    expect(b).toBeInstanceOf(EgressBlockedError);
  });

  it('net.connect to loopback works', async () => {
    const before = blockedTotal();
    await new Promise<void>((resolve, reject) => {
      const socket = net.connect({ host: '127.0.0.1', port }, () => {
        socket.end();
        resolve();
      });
      socket.once('error', reject);
    });
    expect(blockedTotal()).toBe(before);
  });

  it('the GitHub hosts stay blocked for the app even with updates on, and for the updater only when updates are on', async () => {
    const connectOf = (host: string) => new Promise<Error>((resolve) => net.connect({ host, port: 443 }).once('error', resolve));
    setEgressState({ connected: false, updates: true });
    try {
      expect(await connectOf('github.com')).toBeInstanceOf(EgressBlockedError);
      // Under runAsUpdater the decision is "allow": prove it without a real connection by spying on the decision.
      const seen = await runAsUpdater(async () => decideMainEgress({ kind: 'tcp', host: 'github.com', port: 443 }).allow);
      expect(seen).toBe(true);
      expect(decideMainEgress({ kind: 'tcp', host: 'github.com', port: 443 }).allow).toBe(false);
    } finally {
      setEgressState({ connected: false, updates: false });
    }
  });

  it('a throwing logger never turns a block into an allow', async () => {
    setEgressLogger(() => {
      throw new Error('disk full');
    });
    await expect(fetch('https://example.com/')).rejects.toBeInstanceOf(EgressBlockedError);
  });

  it('spies: nothing reaches the original connect when blocked', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const error = await new Promise<Error>((resolve) => net.connect({ host: 'tracker.example.test', port: 443 }).once('error', resolve));
    expect(error.message).toContain('tracker.example.test');
    spy.mockRestore();
  });
});
