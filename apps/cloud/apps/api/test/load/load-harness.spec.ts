// The gateway load-test harness (GW-12): the echo server does what the scenario relies on, and nothing in CI, in a
// deployment file or in the API's start-up path can run or enable it by accident.
import { spawn, type ChildProcess } from 'node:child_process';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const LOAD = __dirname;
const API = join(LOAD, '..', '..');
const CLOUD = join(API, '..', '..');
const REPO = join(CLOUD, '..', '..');
const SERVER = join(LOAD, 'fake-upstream.mjs');

interface Running {
  child: ChildProcess;
  base: string;
}
const running: ChildProcess[] = [];

/** Starts the echo server as its own process (it is an ES module; Jest here is CommonJS) on a free port. */
async function startServer(args: string[]): Promise<Running> {
  const child = spawn(process.execPath, [SERVER, '--port', '0', ...args], { stdio: ['ignore', 'pipe', 'pipe'] });
  running.push(child);
  const base = await new Promise<string>((resolve, reject) => {
    let out = '';
    const timer = setTimeout(() => reject(new Error(`the echo server did not start: ${out}`)), 10_000);
    child.stdout!.on('data', (part: Buffer) => {
      out += part.toString();
      const match = /listening on (http:\/\/127\.0\.0\.1:\d+)/.exec(out);
      if (match) {
        clearTimeout(timer);
        resolve(match[1]);
      }
    });
    child.once('exit', (code) => reject(new Error(`the echo server exited with ${code}: ${out}`)));
  });
  return { child, base };
}

afterAll(async () => {
  for (const child of running) child.kill('SIGTERM');
  await Promise.all(running.map((child) => new Promise((resolve) => (child.exitCode !== null ? resolve(null) : child.once('exit', resolve)))));
});

const AUTH = { authorization: 'Bearer placeholder-node-credential', 'content-type': 'application/json' };

async function readEvents(base: string, body: unknown = { stream: true, messages: [{ role: 'user', content: 'hi' }] }): Promise<{ events: string[]; status: number }> {
  const response = await fetch(`${base}/v1/chat/completions`, { method: 'POST', headers: AUTH, body: JSON.stringify(body) });
  const text = await response.text();
  const events = text.split('\n\n').filter((event) => event !== '');
  return { events: events.map((event) => event.replace(/^data: /, '')), status: response.status };
}

describe('the echo server (fake-upstream.mjs)', () => {
  it('streams a role chunk, exactly 300 content events, a finish chunk, a usage-only chunk and [DONE]', async () => {
    const { base } = await startServer(['--interval-ms', '1']);
    const { events, status } = await readEvents(base);
    expect(status).toBe(200);
    expect(events.at(-1)).toBe('[DONE]');
    const chunks = events.slice(0, -1).map((event) => JSON.parse(event) as { choices: Array<{ delta: { role?: string; content?: string }; finish_reason: string | null }>; usage?: Record<string, number>; x_sent_ms: number });

    const content = chunks.filter((chunk) => chunk.choices.length > 0 && typeof chunk.choices[0].delta.content === 'string' && chunk.choices[0].delta.content !== '');
    expect(content).toHaveLength(300);
    expect(chunks[0].choices[0].delta.role).toBe('assistant');
    expect(chunks.at(-2)?.choices[0].finish_reason).toBe('stop');

    const usage = chunks.at(-1)!;
    expect(usage.choices).toEqual([]);
    expect(usage.usage).toMatchObject({ completion_tokens: 300 });
    expect(chunks.filter((chunk) => chunk.choices.length === 0)).toHaveLength(1);
    expect(events).toHaveLength(1 + 300 + 1 + 1 + 1);
  });

  it('stamps every chunk with its send time, in epoch milliseconds, never decreasing', async () => {
    const { base } = await startServer(['--interval-ms', '2', '--tokens', '20']);
    const before = Date.now();
    const { events } = await readEvents(base);
    const after = Date.now();
    const stamps = events.slice(0, -1).map((event) => (JSON.parse(event) as { x_sent_ms: number }).x_sent_ms);
    expect(stamps).toHaveLength(1 + 20 + 1 + 1);
    for (const stamp of stamps) {
      expect(typeof stamp).toBe('number');
      expect(stamp).toBeGreaterThanOrEqual(before - 1);
      expect(stamp).toBeLessThanOrEqual(after + 1);
    }
    expect([...stamps].sort((a, b) => a - b)).toEqual(stamps);
  });

  it('spaces the content events by the interval (not faster), and ends the answer after the last one', async () => {
    const { base } = await startServer(['--interval-ms', '20', '--tokens', '10']);
    const started = Date.now();
    const { events } = await readEvents(base);
    const elapsed = Date.now() - started;
    expect(events).toHaveLength(1 + 10 + 1 + 1 + 1);
    expect(elapsed).toBeGreaterThanOrEqual(10 * 20 - 25);
    expect(elapsed).toBeLessThan(10 * 20 + 1500);
  });

  it('defaults to 300 tokens at 20 ms', async () => {
    expect(readFileSync(SERVER, 'utf8')).toMatch(/tokens: 300, intervalMs: 20/);
  });

  it('answers /health with 200, /stats with its counters, and refuses a call with no bearer', async () => {
    const { base } = await startServer(['--interval-ms', '1', '--tokens', '3']);
    expect((await fetch(`${base}/health`)).status).toBe(200);
    expect((await fetch(`${base}/health`)).headers.get('content-type')).toContain('json');

    const noAuth = await fetch(`${base}/v1/chat/completions`, { method: 'POST', body: '{"stream":true}' });
    expect(noAuth.status).toBe(401);
    await noAuth.text();
    await readEvents(base);
    const stats = (await (await fetch(`${base}/stats`)).json()) as Record<string, number>;
    expect(stats).toMatchObject({ active: 0, started: 1, completed: 1, aborted: 0, rejected: 1 });
  });

  it('counts a client that leaves mid-stream as aborted and stops sending', async () => {
    const { base } = await startServer(['--interval-ms', '5', '--tokens', '1000']);
    const controller = new AbortController();
    const response = await fetch(`${base}/v1/chat/completions`, { method: 'POST', headers: AUTH, body: JSON.stringify({ stream: true }), signal: controller.signal });
    const reader = response.body!.getReader();
    await reader.read();
    controller.abort();
    await new Promise((resolve) => setTimeout(resolve, 300));
    const stats = (await (await fetch(`${base}/stats`)).json()) as Record<string, number>;
    expect(stats).toMatchObject({ active: 0, started: 1, completed: 0, aborted: 1 });
  });

  it('answers a non-streaming request with one JSON completion and usage', async () => {
    const { base } = await startServer([]);
    const response = await fetch(`${base}/v1/chat/completions`, { method: 'POST', headers: AUTH, body: JSON.stringify({ messages: [{ role: 'user', content: 'x' }] }) });
    expect(response.status).toBe(200);
    const body = (await response.json()) as { usage: { completion_tokens: number } };
    expect(body.usage.completion_tokens).toBe(1);
  });
});

function listFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    if (name === 'node_modules' || name === 'dist' || name === '.git') return [];
    const path = join(dir, name);
    return statSync(path).isDirectory() ? listFiles(path) : [path];
  });
}

describe('nothing runs or enables the load test by accident', () => {
  it('no CI workflow runs k6, the echo server or the load script, or sets the flag', () => {
    const workflows = listFiles(join(REPO, '.github')).filter((file) => /\.ya?ml$/.test(file));
    expect(workflows.length).toBeGreaterThan(0);
    for (const file of workflows) {
      const text = readFileSync(file, 'utf8');
      expect({ file: relative(REPO, file), hit: /k6|load:gateway|fake-upstream|FAKE_UPSTREAM|gateway-load/.test(text) }).toEqual({ file: relative(REPO, file), hit: false });
    }
  });

  it('the flag is in no compose file, env example, Dockerfile or script that deploys the cloud', () => {
    const files = [...listFiles(join(CLOUD, 'infra')), join(API, 'Dockerfile'), ...listFiles(join(CLOUD, 'scripts')), join(CLOUD, 'install.sh'), join(REPO, 'installers')].filter((file) => {
      try {
        return statSync(file).isFile();
      } catch {
        return false;
      }
    });
    expect(files.length).toBeGreaterThan(5);
    for (const file of files) {
      expect({ file: relative(REPO, file), hit: readFileSync(file, 'utf8').includes('FAKE_UPSTREAM') }).toEqual({ file: relative(REPO, file), hit: false });
    }
  });

  it('only the load:gateway script mentions k6, and test, build and start do not run it', () => {
    const scripts = (JSON.parse(readFileSync(join(API, 'package.json'), 'utf8')) as { scripts: Record<string, string> }).scripts;
    expect(Object.keys(scripts).filter((name) => /k6|load/.test(scripts[name]))).toEqual(['load:gateway']);
    for (const name of ['test', 'test:db', 'build', 'start', 'start:dev', 'typecheck']) expect(scripts[name]).not.toMatch(/k6|load|fake/i);
    const cloud = (JSON.parse(readFileSync(join(CLOUD, 'package.json'), 'utf8')) as { scripts: Record<string, string> }).scripts;
    for (const command of Object.values(cloud)) expect(command).not.toMatch(/k6|load:gateway|fake-upstream/);
  });

  it('k6 is not an npm dependency', () => {
    for (const file of [join(API, 'package.json'), join(CLOUD, 'package.json')]) expect(readFileSync(file, 'utf8')).not.toMatch(/"k6"/);
  });

  it('main.ts refuses the flag in production before it builds the application', () => {
    const main = readFileSync(join(API, 'src', 'main.ts'), 'utf8');
    const bootstrap = main.slice(main.indexOf('async function bootstrap'));
    const assertAt = bootstrap.indexOf('assertFakeUpstreamAllowed(');
    expect(assertAt).toBeGreaterThan(-1);
    expect(assertAt).toBeLessThan(bootstrap.indexOf('createApp()'));
  });

  it('the k6 script reads the key from the environment and holds none', () => {
    const script = readFileSync(join(LOAD, 'gateway-load.k6.js'), 'utf8');
    expect(script).toMatch(/env\('LAI_KEY'/);
    expect(script).not.toMatch(/lai_[A-Za-z0-9]{8,}/);
    expect(script).toContain('gateway_added_ms');
    expect(script).toMatch(/'p\(95\)<=5'/);
  });

  it('the files of the harness carry no address but loopback and no key', () => {
    for (const name of ['gateway-load.k6.js', 'fake-upstream.mjs', 'sample-rss.sh', 'RESULTS-TEMPLATE.md']) {
      const text = readFileSync(join(LOAD, name), 'utf8');
      const addresses = text.match(/\b\d{1,3}(?:\.\d{1,3}){3}\b/g) ?? [];
      for (const address of addresses) expect(address.startsWith('127.') || address === '0.0.0.0').toBe(true);
      expect(text).not.toMatch(/\b(?:nod|pat|lai)_[A-Za-z0-9]{16,}/);
    }
  });
});
