// The node-facing Local AI routes (docs/specs/cloud-app.md section 3.6). Every route sits under
// `/api/nodes/{id}/localai/` so EAB's `nod_` allowlist (exact prefix `/api/nodes/`) admits it unchanged, and every
// route runs the binding guard (`@NodeLinkAuth()`), so a handler only ever sees the verified node.
//
// Skeleton: `GET config` (CLOUD-05). `state` (CLOUD-06), `key` (CLOUD-07), `commands/*` (CLOUD-08) and `catalog`
// (CLOUD-10) are added to this controller or to sibling controllers carrying the same decorator.
import { Controller, Get } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import type { LocalaiNode } from '@prisma/client';

import type { LocalaiConfigResponse } from '../contracts/config.schema';
import { NodeLinkAuth } from './node-link-auth.decorator';
import { buildLocalaiConfig } from './node-link.config';
import { CurrentLocalaiNode } from './node-link.types';

@ApiTags('localai-node-link')
@Controller('nodes/:id/localai')
@NodeLinkAuth()
export class NodeLinkController {
  @Get('config')
  @ApiOperation({ summary: 'Handshake: the features, command plane and timing this cloud offers the node' })
  config(@CurrentLocalaiNode() node: LocalaiNode): LocalaiConfigResponse {
    return buildLocalaiConfig(node, new Date());
  }
}
