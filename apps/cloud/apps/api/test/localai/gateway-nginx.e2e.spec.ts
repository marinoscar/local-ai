// The stream proxy THROUGH the inner nginx (docs/specs/gateway.md section 3.1 and 9): the real `gateway.conf` location
// (`proxy_buffering off`, `/v1/` mapped onto `/api/gateway/v1/`) in front of the real gateway scope, with a fake llama-server
// behind it. A real `nginx` process is started on a loopback port from a generated config that includes the app's own
// `infra/nginx/app.d/locations/gateway.conf` (only the platform's `security-headers.conf` include is dropped: that file
// belongs to the container image). The spec is skipped, with the reason in its name, on a machine without an `nginx` binary.
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { AddressInfo } from 'node:net';

import { Logger } from '@nestjs/common';

import { sseBehaviour, sleep, type SseScript } from '../fakes/fake-llama-sse';
import { CHAT, streamLab, StreamClient, waitFor, type StreamLab } from './support/stream-lab';

const REPO = join(__dirname, '..', '..', '..', '..', '..', '..');
const GATEWAY_CONF = join(REPO, 'apps', 'cloud', 'infra', 'nginx', 'app.d', 'locations', 'gateway.conf');

const hasNginx = spawnSync('nginx', ['-v'], { encoding: 'utf8' }).status === 0;
const suite = hasNginx ? describe : describe.skip;

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address() as AddressInfo;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return port;
}

interface Nginx {
  origin: string;
  stop(): Promise<void>;
}

async function startNginx(apiPort: number): Promise<Nginx> {
  const dir = mkdtempSync(join(tmpdir(), 'lai-nginx-'));
  for (const name of ['body', 'proxy', 'fcgi', 'uwsgi', 'scgi']) mkdirSync(join(dir, name));
  const port = await freePort();
  const location = readFileSync(GATEWAY_CONF, 'utf8')
    .split('\n')
    .filter((line) => !line.includes('security-headers.conf'))
    .join('\n');
  const conf = join(dir, 'nginx.conf');
  writeFileSync(
    conf,
    `worker_processes 1;
pid ${dir}/nginx.pid;
error_log ${dir}/error.log warn;
daemon off;
events { worker_connections 128; }
http {
  access_log off;
  client_body_temp_path ${dir}/body;
  proxy_temp_path ${dir}/proxy;
  fastcgi_temp_path ${dir}/fcgi;
  uwsgi_temp_path ${dir}/uwsgi;
  scgi_temp_path ${dir}/scgi;
  upstream api_upstream { server 127.0.0.1:${apiPort}; keepalive 8; }
  server {
    listen 127.0.0.1:${port};
${location}
  }
}
`,
  );
  const test = spawnSync('nginx', ['-t', '-c', conf, '-p', dir], { encoding: 'utf8' });
  if (test.status !== 0) throw new Error(`nginx rejected the generated config: ${test.stderr}`);

  const child: ChildProcess = spawn('nginx', ['-c', conf, '-p', dir], { stdio: 'ignore' });
  const origin = `http://127.0.0.1:${port}`;
  let ready = false;
  for (let attempt = 0; attempt < 100 && !ready; attempt += 1) {
    try {
      ready = (await fetch(`${origin}/v1/`)).status === 200;
    } catch {
      // not listening yet
    }
    if (!ready) await sleep(50);
  }
  if (!ready) {
    child.kill('SIGKILL');
    throw new Error('nginx did not start');
  }
  return {
    origin,
    async stop() {
      const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()));
      child.kill('SIGTERM');
      await exited;
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

suite(`stream proxy through the inner nginx${hasNginx ? '' : ' (skipped: no nginx binary on this machine)'}`, () => {
  let lab: StreamLab;
  let nginx: Nginx;
  const script: SseScript = { tokens: ['Ho', 'la', ' mu', 'ndo', '!', ' ', 'Que', ' ta', 'l', '?'], intervalMs: 50 };

  beforeAll(async () => {
    jest.spyOn(Logger.prototype, 'debug').mockImplementation(() => undefined);
    jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
    lab = await streamLab([{ id: 'ai-n1', behaviour: sseBehaviour(script) }]);
    nginx = await startNginx(Number(new URL(lab.origin).port));
  });

  afterAll(async () => {
    await nginx?.stop();
    await lab?.cleanup();
    jest.restoreAllMocks();
  });

  it('delivers the events one by one through location /v1/, not all at the end', async () => {
    script.emitted = [];
    script.emittedAt = [];
    const client = new StreamClient(nginx.origin, '/v1/chat/completions', lab.gw.token, CHAT);
    await client.done;

    expect(client.status).toBe(200);
    expect(client.ended).toBe(true);
    expect(client.payloads.at(-1)).toBe('[DONE]');
    expect(client.events.length).toBeGreaterThanOrEqual(12);

    // Incremental: the first event is long before the last, and each is close behind the node's own write.
    const first = client.events[0]!.at;
    const last = client.events[client.events.length - 1]!.at;
    expect(last - first).toBeGreaterThan(300);
    let pointer = 0;
    for (const event of client.events) {
      while (pointer < script.emitted.length && script.emitted[pointer] !== event.text) pointer += 1;
      expect(event.at - script.emittedAt[pointer]!).toBeLessThan(100);
    }
    expect(client.headers['x-request-id']).toEqual(expect.any(String));
  });

  it('carries a client abort through nginx to the node', async () => {
    const before = lab.llamas['ai-n1']!.abortedAt.length;
    script.intervalMs = 20;
    script.endless = true;
    try {
      const client = new StreamClient(nginx.origin, '/v1/chat/completions', lab.gw.token, CHAT);
      expect(await waitFor(() => client.events.length >= 4)).toBe(true);
      const abortedAt = Date.now();
      client.abort();
      expect(await waitFor(() => lab.llamas['ai-n1']!.abortedAt.length === before + 1, 2000)).toBe(true);
      expect(lab.llamas['ai-n1']!.abortedAt[before]! - abortedAt).toBeLessThan(500);
    } finally {
      script.endless = false;
      script.intervalMs = 50;
    }
  });
});
