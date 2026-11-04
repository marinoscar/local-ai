// The gateway's way to get a node's llama-server key (docs/specs/gateway.md sections 4.3 and 6).
//
// A thin adapter over `NodeKeyService.getKey` (the cloud's only reader of `('localai_node_llama_key', <node-id>)`), which
// already caches the decrypted key for at most 60 s and drops the entry on `localai.node.key_reported`. This adds what the
// gateway needs on top: a TYPED error for an unreadable store, so every caller maps it to 503 LAI-GW-050 the same way.
//
// SECRET HANDLING. `get` returns PLAINTEXT. Use it for the upstream `Authorization` header and nowhere else: never put the
// result in a log line, a span, an error, a response or a usage row. The error carries the node id only.
import { Injectable } from '@nestjs/common';

import { NodeKeyService } from '../../keys/node-key.service';
import { GW_KEYS_UNREADABLE } from './routing.types';

/** The stored key exists but cannot be decrypted (or the store cannot be read): 503 LAI-GW-050. */
export class GatewayNodeKeyError extends Error {
  readonly code = GW_KEYS_UNREADABLE;
  readonly status = 503;
  constructor(readonly nodeId: string) {
    super(`The gateway cannot read the stored key of node ${nodeId}`);
    this.name = 'GatewayNodeKeyError';
  }
}

@Injectable()
export class NodeKeyProvider {
  constructor(private readonly keys: NodeKeyService) {}

  /**
   * The node's key, or null when the cloud holds none (a node without a key is not in the routing table, so null means
   * the key was deleted since the last rebuild: the caller treats the node as unavailable).
   *
   * @throws GatewayNodeKeyError when the store cannot be read or the key cannot be decrypted (LAI-GW-050).
   */
  async get(nodeId: string): Promise<string | null> {
    try {
      return await this.keys.getKey(nodeId);
    } catch {
      // `NodeKeyUnreadableError`, or anything else out of the credential store: the same fault for the caller. The cause is
      // dropped on purpose (its message is not ours to trust, and the service has already logged its name).
      throw new GatewayNodeKeyError(nodeId);
    }
  }
}
