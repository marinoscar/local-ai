// The node side of the command plane (docs/specs/cloud-app.md sections 3.6 and 4.4): the core service, the claim
// long poll, the sweeper and the four node routes. `PrismaService`, the event bus, the jobs registry and `JobsService`
// are global. The routes sit behind the binding guard, which `NodeLinkModule` provides and exports.
import { Module } from '@nestjs/common';

import { NodeLinkCommandsController } from '../node-link/commands.controller';
import { NodeLinkModule } from '../node-link/node-link.module';
import { CommandClaimRepository, PrismaCommandClaimRepository } from './claim.sql';
import { ClaimService } from './claim.service';
import { LOCALAI_CLOCK, systemLocalaiClock } from './command-events';
import { CommandsService } from './commands.service';
import { CommandsSweepHandler } from './sweeper.job';
import { CommandsSweepTask } from './sweeper.task';

@Module({
  imports: [NodeLinkModule],
  controllers: [NodeLinkCommandsController],
  providers: [
    CommandsService,
    ClaimService,
    CommandsSweepHandler,
    CommandsSweepTask,
    { provide: CommandClaimRepository, useClass: PrismaCommandClaimRepository },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  // CLOUD-09 (the command API for the UI) enqueues through `CommandsService`.
  exports: [CommandsService],
})
export class CommandsModule {}
