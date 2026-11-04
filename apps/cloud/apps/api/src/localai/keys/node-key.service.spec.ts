import type { LocalaiNode } from '@prisma/client';

import { captureOutput, HEX64, testKey } from '../../../test/localai/support/capture-output';
import { fingerprintOf, type NodeKeyRequest } from '../contracts/key.schema';
import { LOCALAI_NODE_KEY_REPORTED_EVENT } from './node-key.events';
import { AUDIT_NODE_KEY_REPORTED, NODE_KEY_CACHE_TTL_MS, NodeKeyService, NodeKeyUnreadableError } from './node-key.service';

const PURPOSE = 'localai_node_llama_key';

/** An in-memory stand-in for the credential store: same address, plaintext kept here only for the assertions. */
class FakeCredentials {
  readonly rows = new Map<string, string>();
  reads = 0;
  failReads = false;
  async setSecret(purpose: string, name: string, secret: string) {
    this.rows.set(`${purpose}/${name}`, secret);
  }
  async getSecret(purpose: string, name: string) {
    this.reads += 1;
    // The real service throws an error whose message must never be trusted; make ours loud.
    if (this.failReads) throw new Error(`decrypt failed for ${this.rows.get(`${purpose}/${name}`)}`);
    return this.rows.get(`${purpose}/${name}`) ?? null;
  }
  async deleteSecret(purpose: string, name: string) {
    this.rows.delete(`${purpose}/${name}`);
  }
}

class FakeBus {
  readonly published: Array<{ channel: string; payload: unknown }> = [];
  readonly handlers = new Map<string, Array<(payload: unknown) => void>>();
  failPublish = false;
  async publish(channel: string, payload: unknown) {
    if (this.failPublish) throw new Error('bus down');
    this.published.push({ channel, payload });
    for (const handler of this.handlers.get(channel) ?? []) handler(payload);
  }
  subscribe(channel: string, handler: (payload: unknown) => void) {
    this.handlers.set(channel, [...(this.handlers.get(channel) ?? []), handler]);
    return () => this.handlers.set(channel, (this.handlers.get(channel) ?? []).filter((h) => h !== handler));
  }
}

function nodeRow(overrides: Partial<LocalaiNode> = {}): LocalaiNode {
  return { id: 'row-ai-n3', nodeId: 'ai-n3', keyHint: null, keyReportedAt: null, phase: 'enrolled', ...overrides } as LocalaiNode;
}

function body(key = testKey()): NodeKeyRequest {
  return { key, fingerprint: fingerprintOf(key), rotated_at: '2026-11-02T15:04:05Z' };
}

describe('NodeKeyService', () => {
  let credentials: FakeCredentials;
  let bus: FakeBus;
  let prisma: { localaiNode: { update: jest.Mock; updateMany: jest.Mock } };
  let audit: { record: jest.Mock };
  let service: NodeKeyService;
  let now: number;

  beforeEach(() => {
    credentials = new FakeCredentials();
    bus = new FakeBus();
    prisma = { localaiNode: { update: jest.fn().mockResolvedValue({}), updateMany: jest.fn().mockResolvedValue({ count: 1 }) } };
    audit = { record: jest.fn().mockResolvedValue(undefined) };
    service = new NodeKeyService(prisma as never, credentials as never, audit as never, bus as never);
    service.onModuleInit();
    now = Date.parse('2026-11-02T15:04:05Z');
    jest.spyOn(Date, 'now').mockImplementation(() => now);
  });

  afterEach(() => {
    service.onModuleDestroy();
    jest.restoreAllMocks();
  });

  describe('report', () => {
    it('stores the key under the purpose and the node id, and round-trips through getKey', async () => {
      const input = body();
      const result = await service.report(nodeRow(), input, 'user-1');
      expect(credentials.rows.get(`${PURPOSE}/ai-n3`)).toBe(input.key);
      expect(await service.getKey('ai-n3')).toBe(input.key);
      expect(result.keyHint).toBe(fingerprintOf(input.key));
      expect(Date.parse(result.storedAt)).toBe(now);
    });

    it('updates keyHint and keyReportedAt on the node row', async () => {
      const input = body();
      await service.report(nodeRow(), input, 'user-1');
      expect(prisma.localaiNode.update).toHaveBeenCalledWith({
        where: { id: 'row-ai-n3' },
        data: { keyHint: fingerprintOf(input.key), keyReportedAt: new Date(now) },
      });
    });

    it('audits localai.node.key_reported with the hint and the derived reason, and nothing else', async () => {
      const first = body();
      await service.report(nodeRow(), first, 'user-1');
      expect(audit.record).toHaveBeenLastCalledWith({
        action: AUDIT_NODE_KEY_REPORTED,
        actorUserId: 'user-1',
        targetType: 'localai_node',
        targetId: 'ai-n3',
        meta: { keyHint: fingerprintOf(first.key), reason: 'enroll' },
      });
      await service.report(nodeRow({ keyHint: fingerprintOf(first.key) }), first, 'user-1');
      expect(audit.record.mock.calls[1][0].meta.reason).toBe('refresh');
      const second = body();
      await service.report(nodeRow({ keyHint: fingerprintOf(first.key) }), second, 'user-1');
      expect(audit.record.mock.calls[2][0].meta).toEqual({ keyHint: fingerprintOf(second.key), reason: 'rotate' });
    });

    it('publishes localai.node.key_reported with the node id, hint and reason (no key)', async () => {
      const input = body();
      await service.report(nodeRow(), input, null);
      expect(bus.published).toEqual([
        { channel: LOCALAI_NODE_KEY_REPORTED_EVENT, payload: { nodeId: 'ai-n3', keyHint: fingerprintOf(input.key), reason: 'enroll' } },
      ]);
      expect(JSON.stringify(bus.published)).not.toContain(input.key);
    });

    it('is idempotent: the same key twice leaves one stored value', async () => {
      const input = body();
      await service.report(nodeRow(), input, 'u');
      await service.report(nodeRow({ keyHint: fingerprintOf(input.key) }), input, 'u');
      expect([...credentials.rows.values()]).toEqual([input.key]);
    });

    it('a rotation overwrites the stored key', async () => {
      const first = body();
      const second = body();
      await service.report(nodeRow(), first, 'u');
      await service.report(nodeRow({ keyHint: fingerprintOf(first.key) }), second, 'u');
      expect(await service.getKey('ai-n3')).toBe(second.key);
    });

    it('still answers when the audit or the event fails (the key is stored), logging error names only', async () => {
      const capture = captureOutput();
      try {
        audit.record.mockRejectedValue(new Error(`audit failed for ${'f'.repeat(64)}`));
        bus.failPublish = true;
        const input = body();
        const result = await service.report(nodeRow(), input, 'u');
        expect(result.keyHint).toBe(fingerprintOf(input.key));
        expect(credentials.rows.get(`${PURPOSE}/ai-n3`)).toBe(input.key);
        expect(capture.text()).not.toMatch(HEX64);
        expect(capture.text()).toContain('audit localai.node.key_reported failed for node_id=ai-n3: Error');
      } finally {
        capture.restore();
      }
    });

    it('does not touch the node row or announce anything when storing the secret fails', async () => {
      credentials.setSecret = jest.fn().mockRejectedValue(new Error('cipher unavailable'));
      await expect(service.report(nodeRow(), body(), 'u')).rejects.toThrow('cipher unavailable');
      expect(prisma.localaiNode.update).not.toHaveBeenCalled();
      expect(audit.record).not.toHaveBeenCalled();
      expect(bus.published).toHaveLength(0);
    });
  });

  describe('getKey cache', () => {
    it('returns null for a node with no key, and does not cache the absence', async () => {
      expect(await service.getKey('ai-n3')).toBeNull();
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'later');
      expect(await service.getKey('ai-n3')).toBe('later');
    });

    it('serves from memory for 60 seconds and reads the store again after that', async () => {
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'one');
      expect(await service.getKey('ai-n3')).toBe('one');
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'two');
      now += NODE_KEY_CACHE_TTL_MS - 1;
      expect(await service.getKey('ai-n3')).toBe('one');
      expect(credentials.reads).toBe(1);
      now += 1;
      expect(await service.getKey('ai-n3')).toBe('two');
      expect(credentials.reads).toBe(2);
    });

    it('caps the cache lifetime at 60 seconds', () => {
      expect(NODE_KEY_CACHE_TTL_MS).toBeLessThanOrEqual(60_000);
    });

    it('a report evicts the cached key at once', async () => {
      const first = body();
      await service.report(nodeRow(), first, 'u');
      expect(await service.getKey('ai-n3')).toBe(first.key);
      const second = body();
      await service.report(nodeRow({ keyHint: fingerprintOf(first.key) }), second, 'u');
      expect(await service.getKey('ai-n3')).toBe(second.key);
    });

    it('the key_reported event from another replica evicts the entry', async () => {
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'one');
      await service.getKey('ai-n3');
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'two');
      await bus.publish(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: 'ai-n3', keyHint: 'aaaa…bbbb', reason: 'rotate' });
      expect(await service.getKey('ai-n3')).toBe('two');
    });

    it('keeps one node\'s entry when another node\'s event arrives', async () => {
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'one');
      await service.getKey('ai-n3');
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'two');
      await bus.publish(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: 'ai-n4', keyHint: 'aaaa…bbbb', reason: 'rotate' });
      expect(await service.getKey('ai-n3')).toBe('one');
    });

    it('stops listening and empties the cache on shutdown', async () => {
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'one');
      await service.getKey('ai-n3');
      service.onModuleDestroy();
      expect(bus.handlers.get(LOCALAI_NODE_KEY_REPORTED_EVENT)).toEqual([]);
      credentials.rows.set(`${PURPOSE}/ai-n3`, 'two');
      expect(await service.getKey('ai-n3')).toBe('two');
    });
  });

  describe('getKey when the store cannot decrypt', () => {
    it('throws NodeKeyUnreadableError carrying the node id only, logs the error name, caches nothing', async () => {
      const capture = captureOutput();
      try {
        const secret = testKey();
        credentials.rows.set(`${PURPOSE}/ai-n3`, secret);
        credentials.failReads = true;
        const error = await service.getKey('ai-n3').catch((e: unknown) => e);
        expect(error).toBeInstanceOf(NodeKeyUnreadableError);
        expect((error as Error).message).toBe('The stored llama key of node ai-n3 cannot be read');
        expect((error as Error).message).not.toContain(secret);
        expect((error as { cause?: unknown }).cause).toBeUndefined();
        expect(capture.text()).not.toContain(secret);
        credentials.failReads = false;
        expect(await service.getKey('ai-n3')).toBe(secret);
      } finally {
        capture.restore();
      }
    });
  });

  describe('deleteKey', () => {
    it('removes the secret, clears keyHint and keyReportedAt, and drops the cache', async () => {
      const input = body();
      await service.report(nodeRow(), input, 'u');
      expect(await service.getKey('ai-n3')).toBe(input.key);
      await service.deleteKey('ai-n3');
      expect(credentials.rows.size).toBe(0);
      expect(await service.getKey('ai-n3')).toBeNull();
      expect(prisma.localaiNode.updateMany).toHaveBeenCalledWith({ where: { nodeId: 'ai-n3' }, data: { keyHint: null, keyReportedAt: null } });
    });

    it('is idempotent and publishes no key_reported event', async () => {
      await service.deleteKey('ai-n3');
      await service.deleteKey('ai-n3');
      expect(bus.published).toHaveLength(0);
    });
  });

  describe('tripwire: no sink carries key material', () => {
    it('a whole report, read and delete leaves no key in logs, audit, event or result', async () => {
      const capture = captureOutput();
      try {
        const input = body();
        const result = await service.report(nodeRow(), input, 'u');
        await service.getKey('ai-n3');
        await service.deleteKey('ai-n3');
        const everything = JSON.stringify({ result, audit: audit.record.mock.calls, events: bus.published, update: prisma.localaiNode.update.mock.calls });
        expect(everything).not.toContain(input.key);
        expect(everything).not.toMatch(HEX64);
        expect(capture.text()).not.toMatch(HEX64);
      } finally {
        capture.restore();
      }
    });
  });
});
