import { Module } from '@nestjs/common';

import { LOCALAI_COMMAND_ENQUEUER } from './command-enqueuer';
import { CommandsModule } from './commands.module';
import { CommandsService } from './commands.service';

/** Provides the {@link LOCALAI_COMMAND_ENQUEUER} port, bound to the command plane's `CommandsService`. */
@Module({
  imports: [CommandsModule],
  providers: [{ provide: LOCALAI_COMMAND_ENQUEUER, useExisting: CommandsService }],
  exports: [LOCALAI_COMMAND_ENQUEUER],
})
export class CommandEnqueuerModule {}
