// `POST /api/nodes/{id}/localai/tailnet` (docs/specs/cloud-app.md section 3.6): the agent reports the node's own Tailscale
// address and name once it has contacted the cloud, so the Add-node wizard never asks for them (issue #437).
import { BadRequestException, Body, Controller, HttpCode, Post } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { CurrentUser } from '@marinoscar/platform-api/identity';
import type { LocalaiNode } from '@prisma/client';

import { NodeTailnetRequestSchema, NodeTailnetResponseSchema, tailnetIssueFields, type NodeTailnetResponse } from '../contracts/tailnet.schema';
import { NodeLinkAuth } from './node-link-auth.decorator';
import { CurrentLocalaiNode } from './node-link.types';
import { NodeTailnetService } from './tailnet.service';

@ApiTags('localai-node-link')
@Controller('nodes/:id/localai')
@NodeLinkAuth()
export class NodeTailnetController {
  constructor(private readonly tailnet: NodeTailnetService) {}

  @Post('tailnet')
  @HttpCode(200)
  @ApiOperation({ summary: "The node reports its own Tailscale address and name (admin-only data, never echoed)" })
  async report(@CurrentLocalaiNode() node: LocalaiNode, @Body() body: unknown, @CurrentUser('id') actorUserId: string): Promise<NodeTailnetResponse> {
    const parsed = NodeTailnetRequestSchema.safeParse(body);
    if (!parsed.success) {
      // 400 with the names of the fields, never a value.
      throw new BadRequestException({ message: 'The tailnet report is not valid', details: { reason: 'invalid', fields: tailnetIssueFields(parsed.error) } });
    }
    return NodeTailnetResponseSchema.parse(await this.tailnet.report(node, parsed.data, actorUserId ?? null));
  }
}
