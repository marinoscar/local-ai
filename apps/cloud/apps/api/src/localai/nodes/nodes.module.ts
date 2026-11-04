import { Module } from '@nestjs/common';

import { CommandEnqueuerModule } from '../commands/command-enqueuer.module';
import { NodeKeysModule } from '../keys/node-keys.module';
import { ResendKeysService } from '../keys/resend-keys.service';
import { LabNodesModule } from './lab-nodes.module';
import { NodesAdminService } from './nodes-admin.service';
import { NodesController } from './nodes.controller';

/** The UI-facing node write routes (`/api/localai/nodes`). The reads are `LabNodesModule`. `NodeCredentialService` is global. */
@Module({
  imports: [CommandEnqueuerModule, LabNodesModule, NodeKeysModule],
  controllers: [NodesController],
  providers: [ResendKeysService, NodesAdminService],
  exports: [NodesAdminService],
})
export class LocalaiNodesModule {}
