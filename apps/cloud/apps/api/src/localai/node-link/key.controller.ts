// `POST /api/nodes/{id}/localai/key` (docs/specs/cloud-app.md section 3.6): the agent reports its llama-server API key.
//
// SECRET HANDLING. The body holds the key, so:
//  - the body is typed `unknown` and validated here with `parseNodeKeyRequest`, which maps a failure to path and code
//    only: no received value ever reaches an error message (the platform's global pipe is not given the body);
//  - the answer is built from stored values (`keyHint`, `storedAt`) and parsed through a strict schema, so the key
//    cannot be echoed even by a later edit; the route has no response `data` that could hold it;
//  - the app has no request-body logger and Fastify request logging is off; `key.controller.spec.ts` and the HTTP
//    tripwire capture every log sink while the flow runs and fail on any 64-hex string.
import { BadRequestException, Body, Controller, HttpCode, Post } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { CurrentUser } from '@marinoscar/platform-api/identity';
import type { LocalaiNode } from '@prisma/client';

import { NodeKeyResponseSchema, parseNodeKeyRequest, type NodeKeyResponse } from '../contracts/key.schema';
import { NodeKeyService } from '../keys/node-key.service';
import { NodeLinkAuth } from './node-link-auth.decorator';
import { CurrentLocalaiNode } from './node-link.types';

@ApiTags('localai-node-link')
@Controller('nodes/:id/localai')
@NodeLinkAuth()
export class NodeKeyController {
  constructor(private readonly keys: NodeKeyService) {}

  @Post('key')
  @HttpCode(200)
  @ApiOperation({ summary: 'The node reports its llama-server API key (stored encrypted, never shown)' })
  async report(@CurrentLocalaiNode() node: LocalaiNode, @Body() body: unknown, @CurrentUser('id') actorUserId: string): Promise<NodeKeyResponse> {
    const parsed = parseNodeKeyRequest(body);
    if (!parsed.ok) {
      // 400 with where and what, never the value.
      throw new BadRequestException({
        message: parsed.refusal.kind === 'fingerprint_mismatch' ? 'The fingerprint does not match the key' : 'The key report is not valid',
        details: { reason: parsed.refusal.kind, issues: parsed.refusal.issues },
      });
    }
    const stored = await this.keys.report(node, parsed.value, actorUserId ?? null);
    return NodeKeyResponseSchema.parse(stored);
  }
}
