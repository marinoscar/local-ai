// The node-facing Local AI routes (docs/specs/cloud-app.md section 3.6). Every route sits under
// `/api/nodes/{id}/localai/` so EAB's `nod_` allowlist (exact prefix `/api/nodes/`) admits it unchanged, and every
// route runs the binding guard (`@NodeLinkAuth()`), so a handler only ever sees the verified node.
//
// Skeleton: `GET config` (CLOUD-05). `state` (CLOUD-06), `key` (CLOUD-07), `commands/*` (CLOUD-08) and `catalog`
// (CLOUD-10) are added to this controller or to sibling controllers carrying the same decorator.
import { Body, Controller, Get, HttpCode, Post } from '@nestjs/common';
import { ApiBody, ApiOperation, ApiTags } from '@nestjs/swagger';
import { CurrentUser } from '@marinoscar/platform-api/identity';
import type { LocalaiNode } from '@prisma/client';

import type { LocalaiConfigResponse } from '../contracts/config.schema';
import type { LocalaiStateResponse } from '../contracts/state.schema';
import { NodeLinkAuth } from './node-link-auth.decorator';
import { buildLocalaiConfig } from './node-link.config';
import { CurrentLocalaiNode } from './node-link.types';
import { StateService } from './state.service';

@ApiTags('localai-node-link')
@Controller('nodes/:id/localai')
@NodeLinkAuth()
export class NodeLinkController {
  constructor(private readonly state: StateService) {}

  @Get('config')
  @ApiOperation({ summary: 'Handshake: the features, command plane and timing this cloud offers the node' })
  config(@CurrentLocalaiNode() node: LocalaiNode): LocalaiConfigResponse {
    return buildLocalaiConfig(node, new Date());
  }

  // The body is deliberately `unknown`: StateService scrubs the RAW body before Zod parses it (so a secret-looking
  // string is never echoed in an issue), and answers every refusal in EAB's error format. Body limit: 256 KiB, set
  // on the route by `registerNodeLinkBodyLimits` (node-link.body-limits.ts).
  @Post('state')
  @HttpCode(200)
  @ApiOperation({ summary: 'Push the node state snapshot (LocalaiStateSnapshotV1, at most 256 KiB)' })
  @ApiBody({ description: 'LocalaiStateSnapshotV1 (docs/specs/cloud-app.md section 3.6)', schema: { type: 'object' } })
  async ingestState(
    @CurrentLocalaiNode() node: LocalaiNode,
    @CurrentUser('id') actorUserId: string | undefined,
    @Body() body: unknown,
  ): Promise<LocalaiStateResponse> {
    return this.state.ingest(node, actorUserId ?? null, body);
  }
}
