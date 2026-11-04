// Playwright for Electron (desktop.md section 9). The specs arrive with DSK-03 and later; they run against
// tests/fixtures/fake-agent.ts on 127.0.0.1:7070 and, for the packaged app, against `scripts/packaged-app.mjs test-copy`.
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: 'tests/e2e',
  timeout: 60_000,
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: { trace: 'retain-on-failure' },
});
