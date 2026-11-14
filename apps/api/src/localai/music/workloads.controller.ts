// `POST /api/localai/nodes/:nodeId/workloads/:name/activate` (E11, cloud-app.md section 3.9): switch what a node does.
// The node's workloads come from its last snapshot (the `workloads` block, agent.md section 3.8.34): a name the node
// did not report is refused (422 LAI-LLM-050) and one that does not fit its GPU too (422 LAI-LLM-051), before anything
// is queued. Then `localai.workload.activate` goes through the command API (`CommandsService.request`: permission,
// node state, dedup, `localai.command.requested`), and `localai.workload.requested` is audited. The answer is the
// command view (201 new, 200 the active duplicate), which a client polls at `GET /api/localai/commands/:id`.
import { Body, Controller, Headers, Inject, Logger, Param, Post, Res, UnprocessableEntityException } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { AuthCredential, Auth, CurrentUser, type AuthCredentialInfo, type RequestUser } from '@marinoscar/platform-api/identity';
import type { FastifyReply } from 'fastify';

import { PrismaService } from '../../prisma/prisma.service';
import { parseWith } from '../commands/command-api.schemas';
import { unknownLabNode } from '../commands/command-errors';
import { assertNodeTakesCommands } from '../commands/command-refusals';
import type { CommandView } from '../commands/command-view';
import { CLIENT_HEADER, sourceOf } from '../commands/commands.controller';
import { CommandsService } from '../commands/commands.service';
import { WorkloadName } from '../contracts/commands';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { AUDIT_WORKLOAD_REQUESTED } from './music-audit';
import { workloadsOf } from './music-state';
import { ActivateWorkloadBodySchema } from './music.views';

/** `422` for a workload the node cannot switch to, with the agent's own code. */
function workloadRefused(code: 'LAI-LLM-050' | 'LAI-LLM-051' | null, reason: string, message: string): UnprocessableEntityException {
  return new UnprocessableEntityException({ message, details: { ...(code ? { code } : {}), reason } });
}

@ApiTags('localai-workloads')
@Controller('localai')
export class WorkloadsController {
  private readonly logger = new Logger(WorkloadsController.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly commands: CommandsService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  @Post('nodes/:nodeId/workloads/:name/activate')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.WORKLOADS_WRITE.id] })
  @ApiOperation({
    summary:
      "Switch a node's workload (queues localai.workload.activate). Body { force?: boolean }. 201 new command, 200 the active duplicate; 400 bad name or body, 404 unknown node, 409 LAI-CLD-035, 422 LAI-CLD-034 (offline/survival), 422 LAI-LLM-050 (the node has no such workload), 422 LAI-LLM-051 (it does not fit)",
  })
  async activate(
    @Param('nodeId') nodeId: string,
    @Param('name') name: string,
    @Body() body: unknown,
    @CurrentUser() user: RequestUser,
    @AuthCredential() credential: AuthCredentialInfo | null,
    @Headers(CLIENT_HEADER) client: string | undefined,
    @Res({ passthrough: true }) reply: FastifyReply,
  ): Promise<CommandView> {
    const workload = parseWith(WorkloadName, name);
    const { force } = parseWith(ActivateWorkloadBodySchema, body);

    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId }, select: { id: true, nodeId: true, phase: true, mode: true, lastState: true } });
    if (!node) throw unknownLabNode();
    assertNodeTakesCommands(node);
    const workloads = workloadsOf(node.lastState);
    if (!workloads) {
      throw workloadRefused(null, 'workloads_not_reported', 'This computer has not reported its workloads yet. Update its agent, or press Refresh status.');
    }
    const item = workloads.items.find((candidate) => candidate.name === workload);
    if (!item) throw workloadRefused('LAI-LLM-050', 'unknown_workload', 'This computer has no workload with that name.');
    if (!item.fits) throw workloadRefused('LAI-LLM-051', 'workload_does_not_fit', "This workload does not fit this computer's graphics card.");

    const { command, created } = await this.commands.request(
      node.nodeId,
      'workload.activate',
      { name: workload, ...(force !== undefined ? { force } : {}) },
      { userId: user.id, permissions: user.permissions },
      sourceOf(credential, client),
    );
    if (created) {
      try {
        await this.audit.record({
          action: AUDIT_WORKLOAD_REQUESTED,
          actorUserId: user.id,
          targetType: 'localai_node',
          targetId: node.nodeId,
          meta: { nodeId: node.nodeId, name: workload, commandId: command.id },
        });
      } catch (error) {
        this.logger.error(`audit ${AUDIT_WORKLOAD_REQUESTED} failed for ${node.nodeId} (${error instanceof Error ? error.name : 'error'})`);
      }
    }
    void reply.status(created ? 201 : 200);
    return command;
  }
}
