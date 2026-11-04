import { buildServiceWorker, readEnabledSlices } from '../../vite.config';

// Web Push needs a worker that shows the pushed notification. It is bundled from
// src/sw.ts into one classic script (no imports: a worker cannot load React or
// share chunks with the app) and exists only while the notifications slice is on.
describe('the Web Push service worker', () => {
  it('bundles into one self-contained script that shows pushes and handles clicks', async () => {
    const code = await buildServiceWorker();
    expect(code).toContain('push');
    expect(code).toContain('notificationclick');
    expect(code).toContain('pushsubscriptionchange');
    expect(code).not.toMatch(/\bimport\s*[({"']/);
    expect(code).not.toContain('react');
  }, 30_000);

  it('is only built while the notifications slice is enabled', () => {
    expect(readEnabledSlices().includes('notifications')).toBe(true);
  });
});
