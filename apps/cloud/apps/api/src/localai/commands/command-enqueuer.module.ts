import { Module } from '@nestjs/common';

import { LOCALAI_COMMAND_ENQUEUER, PrismaCommandEnqueuer } from './command-enqueuer';

/** Provides the {@link LOCALAI_COMMAND_ENQUEUER} port. CLOUD-08 rebinds it to `CommandsService`. */
@Module({
  providers: [{ provide: LOCALAI_COMMAND_ENQUEUER, useClass: PrismaCommandEnqueuer }],
  exports: [LOCALAI_COMMAND_ENQUEUER],
})
export class CommandEnqueuerModule {}
