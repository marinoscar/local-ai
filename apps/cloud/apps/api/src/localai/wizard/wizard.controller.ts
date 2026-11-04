// The Add-node wizard routes (docs/specs/cloud-app.md sections 3.9 and 4.8). Every route needs `lab:admin`, the string
// the whole write side of the lab uses. Bodies are validated with Zod.
//
// `POST /api/localai/nodes` returns the node credential, once. Its response, and the checklist's (which carries the node's
// tailnet address), are `Cache-Control: no-store`: neither may sit in a browser or proxy cache.
import { Body, Controller, Get, Header, HttpCode, NotFoundException, Param, Post } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';

import { parseWith } from '../catalog/catalog.schemas';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { ChecklistService, type ChecklistDto } from './checklist.service';
import { isNodeId } from './node-id';
import { ChecklistActionBodySchema, CreateNodeBodySchema, isChecklistStep } from './wizard.schemas';
import { WizardService, type CreateNodeResponse } from './wizard.service';

const NO_STORE = 'no-store, max-age=0';

@ApiTags('localai-nodes')
@Controller('localai/nodes')
export class WizardController {
  constructor(
    private readonly wizard: WizardService,
    private readonly checklist: ChecklistService,
  ) {}

  @Get('next-id')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'The next free node id (ai-n<max+1> over every node ever added, retired ones included)' })
  async nextId(): Promise<{ nodeId: string | null }> {
    return { nodeId: await this.wizard.suggestNodeId() };
  }

  @Post()
  @HttpCode(201)
  @Header('Cache-Control', NO_STORE)
  @Header('Pragma', 'no-cache')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'Wizard step 1: create a pending node, mint and bind its nod_ credential. The token is in this response only.' })
  create(@Body() body: unknown, @CurrentUser() user: RequestUser): Promise<CreateNodeResponse> {
    return this.wizard.create(parseWith(CreateNodeBodySchema, body), user.id);
  }

  @Get(':nodeId/checklist')
  @Header('Cache-Control', NO_STORE)
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'The publish checklist with its commands and files rendered for this node (real tailnet address; lab:admin only)' })
  getChecklist(@Param('nodeId') nodeId: string): Promise<ChecklistDto> {
    return this.checklist.get(knownId(nodeId));
  }

  @Post(':nodeId/checklist/:step')
  @HttpCode(200)
  @Header('Cache-Control', NO_STORE)
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_ADMIN.id] })
  @ApiOperation({ summary: 'Mark a checklist step done or to do, or run its automatic check' })
  async act(@Param('nodeId') nodeId: string, @Param('step') step: string, @Body() body: unknown, @CurrentUser() user: RequestUser): Promise<ChecklistDto> {
    if (!isChecklistStep(step)) throw new NotFoundException({ message: 'No such checklist step', details: { reason: 'unknown_step' } });
    return this.checklist.act(knownId(nodeId), step, parseWith(ChecklistActionBodySchema, body), user.id);
  }
}

/** A node id outside the lab's shape cannot exist: answer 404 without a query. */
function knownId(nodeId: string): string {
  if (!isNodeId(nodeId)) throw new NotFoundException({ message: 'No Local AI node has this id', details: { reason: 'unknown_localai_node' } });
  return nodeId;
}
