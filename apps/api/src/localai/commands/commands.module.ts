// The command plane (docs/specs/cloud-app.md sections 3.6, 3.9 and 4.4): the core service, the claim long poll, the
// sweeper, the four node routes and the UI command routes with their audit listener. `PrismaService`, the event bus,
// `AUDIT_SINK`, the jobs registry and `JobsService` are global. The node routes sit behind the binding guard, which
// `NodeLinkModule` provides and exports.
import { Module } from '@nestjs/common';

import { NodeLinkCommandsController } from '../node-link/commands.controller';
import { NodeLinkModule } from '../node-link/node-link.module';
import { CommandClaimRepository, PrismaCommandClaimRepository } from './claim.sql';
import { ClaimService } from './claim.service';
import { LOCALAI_CLOCK, systemLocalaiClock } from './command-events';
import { CommandsAuditListener } from './commands-audit.listener';
import { CommandsController } from './commands.controller';
import { CommandsQueryService } from './commands-query.service';
import { LOCALAI_COMMAND_NOTIFIER, NoopLocalaiCommandNotifier } from './command-notifier';
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
    // CLOUD-15 (notifications) replaces the no-op notifier by providing LOCALAI_COMMAND_NOTIFIER here.
    { provide: LOCALAI_COMMAND_NOTIFIER, useClass: NoopLocalaiCommandNotifier },
    ClaimService,
    CommandsSweepHandler,
    CommandsSweepTask,
    { provide: CommandClaimRepository, useClass: PrismaCommandClaimRepository },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  exports: [CommandsService, LOCALAI_COMMAND_NOTIFIER],
})
export class CommandsModule {}
