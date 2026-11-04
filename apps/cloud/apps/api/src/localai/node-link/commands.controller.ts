// The node-facing command routes (docs/specs/cloud-app.md section 3.6): `POST /api/nodes/{id}/localai/commands/claim`
// and `/{cmdId}/renew | result | failure`. Every route carries `@NodeLinkAuth()`, so a handler only ever sees the
// verified node, and every query below is scoped to THAT node's id (a command id of another node is a 409, never a
// 404, so the answer does not reveal whether it exists). Bodies are strict Zod schemas; a malformed body is a 400.
//
// `409` always means "the server moved on; drop the work". The wire shapes mirror EAB's claim/renew/result/failure.
import { Body, Controller, HttpCode, Param, Post, Res } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import type { LocalaiNode } from '@prisma/client';
import type { FastifyReply } from 'fastify';
import { z } from 'zod';

import {
  ClaimRequestSchema,
  FailureRequestSchema,
  RenewRequestSchema,
  ResultRequestSchema,
  type ClaimResponse,
  type RenewResponse,
  type SettleResponse,
} from '../contracts/command-wire';
import { ClaimService } from '../commands/claim.service';
import { invalidBody, leaseLost, unknownCommand } from '../commands/command-errors';
import { CommandsService } from '../commands/commands.service';
import { NodeLinkAuth } from './node-link-auth.decorator';
import { CurrentLocalaiNode } from './node-link.types';

const CommandId = z.string().uuid();

/** The command id from the path, or a 404 for something that is not a uuid (it never reaches SQL). */
function commandIdOf(raw: string): string {
  const parsed = CommandId.safeParse(raw);
  if (!parsed.success) throw unknownCommand();
  return parsed.data;
}

@ApiTags('localai-node-link')
@Controller('nodes/:id/localai/commands')
@NodeLinkAuth()
export class NodeLinkCommandsController {
  constructor(
    private readonly claims: ClaimService,
    private readonly commands: CommandsService,
  ) {}

  @Post('claim')
  @HttpCode(200)
  @ApiOperation({ summary: 'Long-poll for the next command queued for this node (an empty list is the normal answer)' })
  async claim(
    @CurrentLocalaiNode() node: LocalaiNode,
    @Body() body: unknown,
    @Res({ passthrough: true }) reply: FastifyReply,
  ): Promise<ClaimResponse> {
    const parsed = ClaimRequestSchema.safeParse(body);
    if (!parsed.success) throw invalidBody(parsed.error);

    // The poll ends when the client goes away before the answer is written. (`request.raw` 'close' fires when the
    // request body has been read, so the response's own 'close' is the one that means a disconnect.)
    const gone = new AbortController();
    const onClose = () => {
      if (!reply.raw.writableFinished) gone.abort();
    };
    reply.raw.once('close', onClose);
    try {
      return await this.claims.claim(node.id, parsed.data, gone.signal);
    } finally {
      reply.raw.removeListener('close', onClose);
    }
  }

  @Post(':cmdId/renew')
  @HttpCode(200)
  @ApiOperation({ summary: 'Extend the lease of a command this node holds (409: the server moved on, drop the work)' })
  async renew(@CurrentLocalaiNode() node: LocalaiNode, @Param('cmdId') cmdId: string, @Body() body: unknown): Promise<RenewResponse> {
    const id = commandIdOf(cmdId);
    const parsed = RenewRequestSchema.safeParse(body);
    if (!parsed.success) throw invalidBody(parsed.error);
    const leaseExpiresAt = await this.commands.renew(node.id, id, parsed.data);
    if (!leaseExpiresAt) throw leaseLost();
    return { leaseExpiresAt: leaseExpiresAt.toISOString() };
  }

  @Post(':cmdId/result')
  @HttpCode(200)
  @ApiOperation({ summary: 'Report the result of a command this node holds (400: wrong type or schema; 409: lease lost)' })
  async result(@CurrentLocalaiNode() node: LocalaiNode, @Param('cmdId') cmdId: string, @Body() body: unknown): Promise<SettleResponse> {
    const id = commandIdOf(cmdId);
    const parsed = ResultRequestSchema.safeParse(body);
    if (!parsed.success) throw invalidBody(parsed.error);
    return this.commands.complete(node.id, id, parsed.data);
  }

  @Post(':cmdId/failure')
  @HttpCode(200)
  @ApiOperation({ summary: 'Report that a command this node holds failed, as "<doctor code>: <message>" (409: lease lost)' })
  async failure(@CurrentLocalaiNode() node: LocalaiNode, @Param('cmdId') cmdId: string, @Body() body: unknown): Promise<SettleResponse> {
    const id = commandIdOf(cmdId);
    const parsed = FailureRequestSchema.safeParse(body);
    if (!parsed.success) throw invalidBody(parsed.error);
    return this.commands.fail(node.id, id, parsed.data);
  }
}
