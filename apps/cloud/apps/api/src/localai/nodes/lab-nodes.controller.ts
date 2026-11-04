// The read-only UI routes of the Lab overview (docs/specs/cloud-app.md section 3.9). A controller of its own (the
// write routes live in `nodes.controller.ts`) so reads and writes can grow independently. Every route needs lab:read.
import { Controller, Get, Param, Query } from '@nestjs/common';
import { ApiOperation, ApiQuery, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser } from '@marinoscar/platform-api/identity';

import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import type { LabNodesResponse, NodeDetailDto } from './dto/node-card';
import { LabNodesService } from './lab-nodes.service';

@ApiTags('localai-nodes')
@Controller('localai/nodes')
export class LabNodesController {
  constructor(private readonly nodes: LabNodesService) {}

  @Get()
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: 'Node cards for the Lab overview: derived health, mode, models, ark colours, alert counts (never the tailnet IP)' })
  list(): Promise<LabNodesResponse> {
    return this.nodes.list();
  }

  @Get(':nodeId')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: 'One node: the card fields, the last snapshot and the tab data. ?reveal=tailnet (lab:admin) adds the tailnet IP and writes one audit row' })
  @ApiQuery({ name: 'reveal', required: false, enum: ['tailnet'], description: 'lab:admin only: include the stored tailnet IP' })
  detail(
    @Param('nodeId') nodeId: string,
    @Query('reveal') reveal: string | undefined,
    @CurrentUser('permissions') permissions: string[] | undefined,
    @CurrentUser('id') actorUserId: string | undefined,
  ): Promise<NodeDetailDto> {
    // The IP is shown only to someone who holds lab:admin AND asked for it; a viewer's `?reveal=tailnet` is ignored.
    const mayReveal = reveal === 'tailnet' && (permissions ?? []).includes(LOCALAI_PERMISSIONS.LAB_ADMIN.id);
    return this.nodes.detail(nodeId, mayReveal, actorUserId ?? null);
  }
}
