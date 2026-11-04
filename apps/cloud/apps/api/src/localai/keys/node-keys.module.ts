import { Module } from '@nestjs/common';
import { CredentialsModule } from '@marinoscar/platform-api/credentials';

import { NodeKeyService } from './node-key.service';

/**
 * The node llama keys (docs/specs/cloud-app.md section 3.8). Exports `NodeKeyService` for the server-side consumers:
 * the gateway's upstream client (GW-04) and the add-node checklist's gateway test (CLOUD-14). Needs the `credentials`
 * slice (`packages/shared/slices.json`). `PrismaService`, `EVENT_BUS` and `AUDIT_SINK` are global.
 */
@Module({
  imports: [CredentialsModule],
  providers: [NodeKeyService],
  exports: [NodeKeyService],
})
export class NodeKeysModule {}
