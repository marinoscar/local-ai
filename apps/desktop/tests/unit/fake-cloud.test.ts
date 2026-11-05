// The fake cloud (tests/fixtures/fake-cloud.ts) and the loopback seam that points `ai.marin.cr:443` at it: the same
// code the e2e specs use, driven here through the real HTTP transport of cloud-link.ts and the real device flow.
import { afterEach, describe, expect, it } from 'vitest';
import { createCloudHttp, createLoopbackDispatcher, pollDeviceToken, requestDeviceGrant, abortableSleep } from '../../src/main/cloud-link';
import { type FakeCloud, startFakeCloud } from '../fixtures/fake-cloud';

let cloud: FakeCloud | undefined;
const allow = () => ({ allow: true, label: 'ai.marin.cr' });
afterEach(async () => {
  await cloud?.close();
  cloud = undefined;
});

describe('fake cloud through the loopback seam', () => {
  it('runs the whole device flow over HTTP: grant, pending, slow_down, approve, PAT, me, revoke', async () => {
    cloud = await startFakeCloud({ interval: 1 });
    const http = createCloudHttp({ dispatcher: createLoopbackDispatcher(cloud.port, allow) });
    const granted = await requestDeviceGrant(http, { deviceName: 'ai-desktop on host-a', userAgent: 'ai-desktop/0.1.0' });
    if (!granted.ok) throw new Error('no grant');
    expect(cloud.clientInfos).toEqual([{ deviceName: 'ai-desktop on host-a', userAgent: 'ai-desktop/0.1.0', tokenType: 'pat' }]);
    expect(granted.grant.verificationUriComplete).toBe(`https://ai.marin.cr/activate?code=${granted.grant.userCode}`);

    cloud.slowDownNext();
    setTimeout(() => cloud!.approve(), 1500);
    const slowed: number[] = [];
    const outcome = await pollDeviceToken({
      http, deviceCode: granted.grant.deviceCode, intervalS: 1, expiresInS: 60, sleep: abortableSleep, now: Date.now,
      signal: new AbortController().signal, onSlowDown: (ms) => slowed.push(ms),
    });
    expect(slowed).toEqual([6000]);
    expect(outcome.kind).toBe('approved');
    if (outcome.kind !== 'approved') return;
    expect(outcome.credential.token).toBe(cloud.issued!.token);

    const me = await http('/api/auth/me', { method: 'GET', bearer: outcome.credential.token });
    expect(me.status).toBe(200);
    expect((me.body as { data: { displayName: string } }).data.displayName).toBe('Person A');
    expect((await http('/api/auth/me', { method: 'GET', bearer: 'pat_wrong' })).status).toBe(401);
    expect((await http(`/api/pat/${outcome.credential.tokenId}`, { method: 'DELETE', bearer: outcome.credential.token })).status).toBe(204);
    expect(cloud.revoked).toEqual([outcome.credential.tokenId]);
  }, 20_000);

  it('a held request is seen as aborted by the server when the client aborts', async () => {
    cloud = await startFakeCloud();
    const http = createCloudHttp({ dispatcher: createLoopbackDispatcher(cloud.port, allow) });
    const granted = await requestDeviceGrant(http, { deviceName: 'd', userAgent: 'u' });
    if (!granted.ok) throw new Error('no grant');
    cloud.approve();
    const outcome = await pollDeviceToken({ http, deviceCode: granted.grant.deviceCode, intervalS: 1, expiresInS: 30, sleep: abortableSleep, now: Date.now, signal: new AbortController().signal });
    if (outcome.kind !== 'approved') throw new Error('not approved');
    cloud.holdNodes = true;
    const controller = new AbortController();
    const held = http('/api/localai/nodes', { method: 'GET', bearer: outcome.credential.token, signal: controller.signal });
    const settled = held.then(() => 'resolved', () => 'rejected');
    while (cloud.openRequests() === 0) await new Promise((r) => setTimeout(r, 10));
    const at = Date.now();
    controller.abort();
    expect(await settled).toBe('rejected');
    while (cloud.abortedAt.length === 0) await new Promise((r) => setTimeout(r, 10));
    expect(cloud.abortedAt[0]! - at).toBeLessThan(1000);
    expect(cloud.openRequests()).toBe(0);
  }, 20_000);
});

describe('the seam follows the egress decision', () => {
  it('an open switch (the real decision, connected = true) lets the request through; undici passes an empty port for 443', async () => {
    const { setEgressState, resetEgressGuardForTests } = await import('../../src/main/egress-guard');
    cloud = await startFakeCloud();
    setEgressState({ connected: true, updates: false });
    try {
      const http = createCloudHttp({ dispatcher: createLoopbackDispatcher(cloud.port) });
      expect((await http('/api/auth/me', { method: 'GET' })).status).toBe(401);
    } finally {
      resetEgressGuardForTests();
    }
    expect(cloud.requests).toHaveLength(1);
  });

  it('a closed switch opens no socket at all (the loopback socket is not a way round the guard)', async () => {
    cloud = await startFakeCloud();
    // The real decision function with the default state: connected = false.
    const http = createCloudHttp({ dispatcher: createLoopbackDispatcher(cloud.port) });
    await expect(http('/api/auth/me', { method: 'GET', bearer: 'pat_x' })).rejects.toThrow();
    expect(cloud.connections()).toBe(0);
    expect(cloud.requests).toHaveLength(0);
  });
});
