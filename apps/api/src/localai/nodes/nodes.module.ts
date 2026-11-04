import { Module } from '@nestjs/common';

import { CommandEnqueuerModule } from '../commands/command-enqueuer.module';
import { ResendKeysService } from '../keys/resend-keys.service';
import { NodesController } from './nodes.controller';

/** The UI-facing node routes (`/api/localai/nodes`). */
@Module({
  imports: [CommandEnqueuerModule],
  controllers: [NodesController],
  providers: [ResendKeysService],
})
export class LocalaiNodesModule {}
