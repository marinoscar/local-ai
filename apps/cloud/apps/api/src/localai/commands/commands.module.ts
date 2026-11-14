// The command plane (docs/specs/cloud-app.md sections 3.6, 3.9 and 4.4): the core service, the claim long poll, the
// sweeper, the four node routes and the UI command routes with their audit listener. `PrismaService`, the event bus,
// `AUDIT_SINK`, the jobs registry and `JobsService` are global. The node routes sit behind the binding guard, which
// `NodeLinkModule` provides and exports.
import { Module } from '@nestjs/common';

import { NodeLinkCommandsController } from '../node-link/commands.controller';
import { NodeLinkModule } from '../node-link/node-link.module';
import { CommandClaimRepository, PrismaCommandClaimRepository } from './claim.sql';
import { ClaimService } from './claim.service';
import { ClaimParamsHooks } from './claim-params.hooks';
import { LOCALAI_CLOCK, systemLocalaiClock } from './command-events';
import { CommandsAuditListener } from './commands-audit.listener';
import { CommandsController } from './commands.controller';
import { CommandsQueryService } from './commands-query.service';
import { LOCALAI_COMMAND_NOTIFIER } from './command-notifier';
import { NotificationsCommandNotifier } from './command-failed.notifier';
import { CommandsService } from './commands.service';
import { CommandsSweepHandler } from './sweeper.job';
import { CommandsSweepTask } from './sweeper.task';

@Module({
  imports: [NodeLinkModule],
  controllers: [NodeLinkCommandsController, CommandsController],
  providers: [
    CommandsService,
    CommandsQueryService,
    CommandsAuditListener,
    // `localai.command_failed` to the requester, through the notifications slice (CLOUD-15).
    { provide: LOCALAI_COMMAND_NOTIFIER, useClass: NotificationsCommandNotifier },
    ClaimService,
    // Claim-time params of a type (music.generate mints its signed URLs there).
    ClaimParamsHooks,
    CommandsSweepHandler,
    CommandsSweepTask,
    { provide: CommandClaimRepository, useClass: PrismaCommandClaimRepository },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  exports: [CommandsService, LOCALAI_COMMAND_NOTIFIER, ClaimParamsHooks],
})
export class CommandsModule {}
