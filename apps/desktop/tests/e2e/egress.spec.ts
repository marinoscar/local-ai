// Egress guard, offline state (docs/specs/desktop.md section 9, egress.spec.ts). Drives the real Electron app.
//
//   unpackaged:  npm run build && xvfb-run -a npx playwright test tests/e2e/egress.spec.ts
//   packaged:    node scripts/dist.mjs --linux --x64 && node scripts/packaged-app.mjs test-copy
//                LOCALAI_E2E_EXECUTABLE=release/test-copy/ai-desktop xvfb-run -a npx playwright test tests/e2e/egress.spec.ts
//
// It needs a display (xvfb on a server) and port 7070 free: the fake agent listens there, exactly like the real one.
// As root (a container) Chromium needs --no-sandbox; set LOCALAI_E2E_NO_SANDBOX=1 anywhere else it is needed.
import { type ElectronApplication, type Page, expect, test } from '@playwright/test';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';
import { type Launched, launch } from './helpers';

interface EgressLine {
  component: string;
  host: string;
  blocked_total: number;
  code: string;
  level: string;
  [key: string]: unknown;
}

let agent: FakeAgent;
let run: Launched;
let app: ElectronApplication;
let logLines: EgressLine[];
let stderr = '';

test.beforeAll(async () => {
  agent = await startFakeAgent({ port: 7070 });
  logLines = [];
  // No token file: the start-up sequence ends on the LAI-DSK-003 page, which is a shell page like any other.
  run = await launch({ token: null });
  app = run.app;
  app.process().stderr?.on('data', (chunk: Buffer) => {
    stderr += chunk.toString('utf8');
    for (const line of chunk.toString('utf8').split('\n')) {
      if (!line.includes('"component":"egress"')) continue;
      try {
        logLines.push(JSON.parse(line) as EgressLine);
      } catch {
        // not one of ours
      }
    }
  });
});

test.afterAll(async () => {
  await run?.stop();
  await agent?.close();
});

test('the shell window loads from localai://app, with the CSP, in whichever way the app is run', async () => {
  // The main window is a BaseWindow: the top bar and the error page are two views of it. Playwright lists every page.
  await expect.poll(() => app.windows().map((w) => w.url()).some((url) => url.startsWith('localai://app/error/'))).toBe(true);
  const page = app.windows().find((w) => w.url().startsWith('localai://app/error/'))!;
  await expect(page.locator('#root')).not.toBeEmpty();
  const headers = await page.evaluate(async () => {
    const response = await fetch('localai://app/topbar/');
    return {
      status: response.status,
      csp: response.headers.get('content-security-policy'),
      nosniff: response.headers.get('x-content-type-options'),
    };
  });
  expect(headers.status).toBe(200);
  expect(headers.nosniff).toBe('nosniff');
  expect(headers.csp).toContain("default-src 'none'");
  expect(headers.csp).toContain("script-src 'self'");
  expect(headers.csp).toContain("frame-ancestors 'none'");
  // The shell page itself can reach nothing but its own files.
  const refused = await page.evaluate(async () => {
    try {
      await fetch('http://127.0.0.1:7070/api/v1/health');
      return 'reached';
    } catch {
      return 'refused';
    }
  });
  expect(refused).toBe('refused'); // connect-src 'self' (CSP) stops it before the network
  // A traversal request through the protocol is refused.
  const traversal = await page.evaluate(async () => (await fetch('localai://app/%2e%2e/%2e%2e/etc/passwd')).status);
  expect([403, 404]).toContain(traversal);
});

test('an agent page that tries fetch, <img> and WebSocket to the internet is blocked three times, host only', async () => {
  const before = logLines.length;
  // A renderer in the agent partition, as the content view will be.
  const [page] = await Promise.all([
    app.waitForEvent('window') as Promise<Page>,
    app.evaluate(({ BrowserWindow }) => {
      const win = new BrowserWindow({
        show: false,
        webPreferences: { partition: 'persist:agent', sandbox: true, contextIsolation: true, nodeIntegration: false, spellcheck: false },
      });
      void win.loadURL('about:blank');
    }),
  ]);
  const failed: string[] = [];
  page.on('requestfailed', (request) => {
    if (request.url().startsWith('https://example.com') || request.url().startsWith('wss://example.com')) {
      failed.push(`${request.url().split('/')[0]} ${request.failure()?.errorText ?? ''}`);
    }
  });

  await page.goto(`${agent.url}/egress-probe`);
  await expect.poll(() => agent.probeResults.length, { timeout: 20_000 }).toBe(1);
  expect(agent.probeResults[0]).toEqual({ fetch: 'failed', img: 'failed', ws: 'failed' });

  const blocks = logLines.slice(before);
  expect(blocks).toHaveLength(3);
  expect(blocks.map((b) => b.blocked_total)).toEqual([before + 1, before + 2, before + 3]);
  for (const block of blocks) {
    expect(block).toMatchObject({ component: 'egress', host: 'example.com', code: 'LAI-DSK-012', level: 'error' });
  }
  // Nothing but the host reaches the log: no path or query of the attempted requests.
  expect(JSON.stringify(blocks)).not.toMatch(/probe/);
  // Chromium reports the cancelled requests as blocked by the client (webRequest cancel), not as network failures.
  // Playwright surfaces the fetch and the image here; it does not report a refused WebSocket handshake.
  expect(failed.length).toBeGreaterThanOrEqual(2);
  expect(failed.every((entry) => entry.includes('ERR_BLOCKED_BY_CLIENT'))).toBe(true);
  // The agent itself stayed reachable (the probe page and its report came from 127.0.0.1:7070).
  expect(agent.requests.map((r) => r.path)).toEqual(expect.arrayContaining(['/egress-probe', '/egress-result']));
});

test('main-process fetch to the cloud throws EgressBlockedError while offline, and loopback still works', async () => {
  const before = logLines.length;
  const outcome = await app.evaluate(async () => {
    try {
      await fetch('https://ai.marin.cr/api/auth/me');
      return { threw: false };
    } catch (error) {
      const e = error as { name?: string; code?: string; host?: string };
      return { threw: true, name: e.name, code: e.code, host: e.host };
    }
  });
  expect(outcome).toEqual({ threw: true, name: 'EgressBlockedError', code: 'LAI-DSK-012', host: 'ai.marin.cr' });
  await expect.poll(() => logLines.length).toBe(before + 1);
  expect(logLines.at(-1)).toMatchObject({ host: 'ai.marin.cr', component: 'egress' });

  const health = await app.evaluate(async () => (await fetch('http://127.0.0.1:7070/api/v1/health')).status);
  expect(health).toBe(200);
  expect(logLines.length).toBe(before + 1);
});

test('the app made no other attempt to leave the machine on its own', async () => {
  // Everything counted so far was provoked above: 3 by the probe page, 1 by the main-process fetch.
  expect(logLines.map((l) => l.host)).toEqual(['example.com', 'example.com', 'example.com', 'ai.marin.cr']);
  expect(stderr).not.toContain('EgressBlockedError: undefined');
});
