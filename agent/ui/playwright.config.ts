import { defineConfig } from '@playwright/test';

const port = Number(process.env.FAKE_AGENT_PORT || 7071);

// The Portal is designed for 1366x768 (survival-portal.md section 4.1). The tests run against a
// fake agent (e2e/fake-agent.mjs) that serves the built dist/: run `npm run build` first.
export default defineConfig({
  testDir: 'e2e',
  testMatch: '**/*.spec.ts',
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    viewport: { width: 1366, height: 768 },
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
  webServer: {
    command: 'node e2e/fake-agent.mjs',
    url: `http://127.0.0.1:${port}/__seen`,
    reuseExistingServer: !process.env.CI,
    env: { FAKE_AGENT_PORT: String(port) },
  },
});
