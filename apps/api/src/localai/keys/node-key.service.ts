// NodeKeyService: the cloud's only code that stores, reads and deletes a node's llama-server API key
// (docs/specs/cloud-app.md section 3.8). The key is kept encrypted by EAB's deployment credential store under the
// address `('localai_node_llama_key', '<node-id>')`.
//
// SECRET HANDLING (spec section 6, security-model.md section 2).
//  - `report` is the only writer, `getKey` the only reader. `getKey` returns PLAINTEXT and is for server-side consumers
//    only (the gateway's upstream client, the checklist's gateway test): never call it from a controller.
//  - Nothing here logs, audits, publishes or throws a key. The audit row and the event carry `keyHint` (first4...last4)
//    and the reason. Log lines carry the node id and an error NAME, never an error message (a decryption failure's
//    message is not ours to trust).
//  - The decrypted key is cached in memory for at most {@link NODE_KEY_CACHE_TTL_MS} per node, as the spec orders
//    (the gateway calls a node on every request; a decryption per request is wasteful). A write, `deleteKey` and the
//    `localai.node.key_reported` event (so another replica's write too) drop the entry. A failed read caches nothing.
import { Inject, Injectable, Logger, type OnModuleDestroy, type OnModuleInit } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { CredentialsService } from '@marinoscar/platform-api/credentials';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import type { LocalaiNode } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { LOCALAI_NODE_LLAMA_KEY_PURPOSE } from '../../platform/credentials/credential-purposes';
import { fingerprintOf, type NodeKeyRequest, type NodeKeyResponse } from '../contracts/key.schema';
import {
  keyReportReason,
  LOCALAI_NODE_KEY_REPORTED_EVENT,
  type NodeKeyReportedEvent,
} from './node-key.events';

/** Audit action (permanent string, spec section 3.5). */
export const AUDIT_NODE_KEY_REPORTED = 'localai.node.key_reported';

/** How long a decrypted key may stay in memory (spec section 3.8). */
export const NODE_KEY_CACHE_TTL_MS = 60_000;

/** The stored key exists but cannot be decrypted (`SECRETS_ENCRYPTION_KEY` lost or changed). The gateway answers LAI-GW-050. */
export class NodeKeyUnreadableError extends Error {
  constructor(readonly nodeId: string) {
    // The message carries the node id only, never the cause's message.
    super(`The stored llama key of node ${nodeId} cannot be read`);
    this.name = 'NodeKeyUnreadableError';
  }
}

interface CacheEntry {
  key: string;
  expiresAt: number;
}

@Injectable()
export class NodeKeyService implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger(NodeKeyService.name);
  private readonly cache = new Map<string, CacheEntry>();
  private unsubscribe: (() => void) | null = null;

  constructor(
    private readonly prisma: PrismaService,
    private readonly credentials: CredentialsService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
  ) {}

  onModuleInit(): void {
    // Another replica stored a new key: our copy is stale. (Our own writes evict directly as well.)
    this.unsubscribe = this.bus.subscribe<NodeKeyReportedEvent>(LOCALAI_NODE_KEY_REPORTED_EVENT, (event) => {
      if (event && typeof event.nodeId === 'string') this.evict(event.nodeId);
    });
  }

  onModuleDestroy(): void {
    this.unsubscribe?.();
    this.unsubscribe = null;
    this.cache.clear();
  }

  /**
   * Stores the key a node reported (`POST /api/nodes/{id}/localai/key`). `node` is the row the binding guard verified;
   * `input` has passed `parseNodeKeyRequest` (so the fingerprint matches). Idempotent: the same key again re-encrypts
   * it, refreshes `keyReportedAt` and audits `refresh`.
   */
  async report(node: LocalaiNode, input: NodeKeyRequest, actorUserId: string | null): Promise<NodeKeyResponse> {
    const keyHint = fingerprintOf(input.key);
    const reason = keyReportReason(node.keyHint, keyHint);
    const storedAt = new Date(Date.now());

    await this.credentials.setSecret(LOCALAI_NODE_LLAMA_KEY_PURPOSE.purpose, node.nodeId, input.key);
    this.evict(node.nodeId);
    await this.prisma.localaiNode.update({ where: { id: node.id }, data: { keyHint, keyReportedAt: storedAt } });

    // After the write committed, outside any transaction. Neither can refuse a key that is already stored.
    try {
      await this.audit.record({
        action: AUDIT_NODE_KEY_REPORTED,
        actorUserId,
        targetType: 'localai_node',
        targetId: node.nodeId,
        meta: { keyHint, reason },
      });
    } catch (error) {
      this.logger.error(`audit ${AUDIT_NODE_KEY_REPORTED} failed for node_id=${node.nodeId}: ${errorName(error)}`);
    }
    try {
      await this.bus.publish<NodeKeyReportedEvent>(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: node.nodeId, keyHint, reason });
    } catch (error) {
      this.logger.error(`event ${LOCALAI_NODE_KEY_REPORTED_EVENT} failed for node_id=${node.nodeId}: ${errorName(error)}`);
    }

    return { keyHint, storedAt: storedAt.toISOString() };
  }

  /**
   * SERVER-SIDE ONLY. RETURNS PLAINTEXT. Never call it from a controller or put its result in a response, a log line,
   * an error or an audit row. The node's llama key, or null when the cloud holds none.
   *
   * @throws NodeKeyUnreadableError when a key is stored but cannot be decrypted.
   */
  async getKey(nodeId: string): Promise<string | null> {
    const now = Date.now();
    const cached = this.cache.get(nodeId);
    if (cached) {
      if (cached.expiresAt > now) return cached.key;
      this.cache.delete(nodeId);
    }
    let key: string | null;
    try {
      key = await this.credentials.getSecret(LOCALAI_NODE_LLAMA_KEY_PURPOSE.purpose, nodeId);
    } catch (error) {
      this.logger.error(`reading the llama key failed for node_id=${nodeId}: ${errorName(error)}`);
      throw new NodeKeyUnreadableError(nodeId);
    }
    // A missing key is not cached: a report lands within seconds and evicts, but never assume it did.
    if (key !== null) this.cache.set(nodeId, { key, expiresAt: now + NODE_KEY_CACHE_TTL_MS });
    return key;
  }

  /**
   * Deletes the stored key and clears `keyHint` and `keyReportedAt` (retiring a node, spec section 3.8). Idempotent.
   * The retire route (CLOUD-14) calls it; it deliberately publishes no `key_reported` event (nothing was reported).
   */
  async deleteKey(nodeId: string): Promise<void> {
    this.evict(nodeId);
    await this.credentials.deleteSecret(LOCALAI_NODE_LLAMA_KEY_PURPOSE.purpose, nodeId);
    await this.prisma.localaiNode.updateMany({ where: { nodeId }, data: { keyHint: null, keyReportedAt: null } });
  }

  /** Drops the cached key of one node. */
  evict(nodeId: string): void {
    this.cache.delete(nodeId);
  }
}

/** Error NAME only: never the error object, whose message could in principle embed data. */
function errorName(error: unknown): string {
  return error instanceof Error ? error.name : 'unknown error';
}
