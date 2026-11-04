// The UI-facing node routes under `/api/localai/nodes` (docs/specs/cloud-app.md section 3.9). CLOUD-07 added the
// "Ask all nodes to re-send keys" action; CLOUD-12 adds the three node admin writes of the detail page (edit, replace
// credential, retire). The list and detail reads are `lab-nodes.controller.ts`; create is CLOUD-14, which appends here.
// Every write is lab:admin and audited by the service after it commits.
import { Body, Controller, HttpCode, Param, Patch, Post } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser } from '@marinoscar/platform-api/identity';

import { parseWith } from '../commands/command-api.schemas';
import { ResendKeysService } from '../keys/resend-keys.service';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { PatchNodeBodySchema, RetireNodeBodySchema } from './dto/patch-node';
import type { NodeDetailDto } from './dto/node-card';
import { LabNodesService } from './lab-nodes.service';
import { NodesAdminService, type ReplacedCredential, type RetiredNode } from './nodes-admin.service';

const ADMIN = { permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] };

@ApiTags('localai-nodes')
@Controller('localai/nodes')
export class NodesController {
  constructor(
    private readonly resendKeys: ResendKeysService,
    private readonly admin: NodesAdminService,
    private readonly nodes: LabNodesService,
  ) {}

  @Post('resend-keys')
  @HttpCode(200)
  @Auth(ADMIN)
  @ApiOperation({ summary: 'Ask every enrolled node to re-send its llama key (queues status.refresh on each)' })
  async resendKeysToAll(@CurrentUser('id') actorUserId: string): Promise<{ commandIds: string[] }> {
    return { commandIds: await this.resendKeys.resendAll(actorUserId ?? null) };
  }

  @Patch(':nodeId')
  @Auth(ADMIN)
  @ApiOperation({ summary: 'Edit a node: displayName, publicHost, servePattern, tailnetMachine, tailnetIp, gatewayEnabled, gatewayPriority, catalogPin. Answers the node as the detail route does (never the tailnet IP)' })
  async patch(@Param('nodeId') nodeId: string, @Body() body: unknown, @CurrentUser('id') actorUserId: string): Promise<NodeDetailDto> {
    await this.admin.patch(nodeId, parseWith(PatchNodeBodySchema, body), actorUserId ?? null);
    return this.nodes.detail(nodeId, false);
  }

  @Post(':nodeId/credential')
  @HttpCode(201)
  @Auth(ADMIN)
  @ApiOperation({ summary: 'Replace the node credential: revoke the bound one and mint a new nod_ token, shown ONCE in this answer' })
  replaceCredential(@Param('nodeId') nodeId: string, @CurrentUser('id') actorUserId: string): Promise<ReplacedCredential> {
    return this.admin.replaceCredential(nodeId, actorUserId);
  }

  @Post(':nodeId/retire')
  @HttpCode(200)
  @Auth(ADMIN)
  @ApiOperation({ summary: 'Retire a node for good. Body { confirm: "<nodeId>" }; 400 when it does not match. Revokes the credential, deletes the key, cancels queued commands' })
  retire(@Param('nodeId') nodeId: string, @Body() body: unknown, @CurrentUser('id') actorUserId: string): Promise<RetiredNode> {
    const { confirm } = parseWith(RetireNodeBodySchema, body);
    return this.admin.retire(nodeId, confirm, actorUserId ?? null);
  }
}
