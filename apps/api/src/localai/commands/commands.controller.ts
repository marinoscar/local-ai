// The command routes of the cloud UI (docs/specs/cloud-app.md section 3.9): history, send, read one, cancel. Every
// route is `@Auth` and every body is Zod-validated. `lab:read` reads. Sending and cancelling need `lab:operate`, or
// `lab:admin` for the four admin types (`model.delete`, `mode.offline`, `key.rotate`, `agent.update`): the route
// admits a holder of either, and `CommandsService` applies the exact permission of the command's type from the
// policy table, so the table is the single place that says who may send what.
import { Body, Controller, Delete, Get, Headers, HttpCode, Param, Post, Query, Res } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { AuthCredential, Auth, CurrentUser, type AuthCredentialInfo, type RequestUser } from '@marinoscar/platform-api/identity';
import type { FastifyReply } from 'fastify';

import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { commandIdOf, ListCommandsQuerySchema, parseWith, RequestCommandBodySchema } from './command-api.schemas';
import type { CommandSource } from './command-enqueuer';
import type { CommandView } from './command-view';
import { CommandsQueryService, type CommandPage } from './commands-query.service';
import { CommandsService } from './commands.service';

/** The header the desktop app sets to identify itself; honoured only for a personal access token. */
export const CLIENT_HEADER = 'x-localai-client';

/**
 * The `source` stored with a command: a browser session (or device-flow token) is `ui`; a `pat_` caller is `api`, or
 * `desktop` when it says so with {@link CLIENT_HEADER}. A session can never claim `desktop`.
 */
export function sourceOf(credential: AuthCredentialInfo | null, client: string | undefined): CommandSource {
  if (credential?.kind !== 'pat') return 'ui';
  return client?.trim().toLowerCase() === 'desktop' ? 'desktop' : 'api';
}

const SEND = [LOCALAI_PERMISSIONS.LAB_OPERATE.id, LOCALAI_PERMISSIONS.LAB_ADMIN.id];

@ApiTags('localai-commands')
@Controller('localai')
export class CommandsController {
  constructor(
    private readonly commands: CommandsService,
    private readonly queries: CommandsQueryService,
  ) {}

  @Get('nodes/:nodeId/commands')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: "A node's command history, newest first (?status= to filter, ?page=)" })
  list(@Param('nodeId') nodeId: string, @Query() query: unknown): Promise<CommandPage> {
    return this.queries.list(nodeId, parseWith(ListCommandsQuerySchema, query));
  }

  @Post('nodes/:nodeId/commands')
  @Auth({ anyPermissions: SEND })
  @ApiOperation({
    summary: 'Send a command to a node. 201 for a new command, 200 with the active duplicate. 403 without the permission of the type, 404 unknown node, 409 retired/pending node, 422 node in Offline/Survival mode or model not on the node',
  })
  async request(
    @Param('nodeId') nodeId: string,
    @Body() body: unknown,
    @CurrentUser() user: RequestUser,
    @AuthCredential() credential: AuthCredentialInfo | null,
    @Headers(CLIENT_HEADER) client: string | undefined,
    @Res({ passthrough: true }) reply: FastifyReply,
  ): Promise<CommandView> {
    const parsed = parseWith(RequestCommandBodySchema, body);
    const { command, created } = await this.commands.request(
      nodeId,
      parsed.type,
      parsed.params,
      { userId: user.id, permissions: user.permissions },
      sourceOf(credential, client),
    );
    void reply.status(created ? 201 : 200);
    return command;
  }

  @Get('commands/:id')
  @Auth({ permissions: [LOCALAI_PERMISSIONS.LAB_READ.id] })
  @ApiOperation({ summary: 'One command with its result or error (the UI polls it every 2 s while it is active)' })
  get(@Param('id') id: string): Promise<CommandView> {
    return this.queries.get(id);
  }

  @Delete('commands/:id')
  @HttpCode(200)
  @Auth({ anyPermissions: SEND })
  @ApiOperation({ summary: 'Cancel a queued command (409 once a node has claimed it or it has finished)' })
  cancel(@Param('id') id: string, @CurrentUser() user: RequestUser): Promise<CommandView> {
    return this.commands.cancel(commandIdOf(id), { userId: user.id, permissions: user.permissions });
  }
}
