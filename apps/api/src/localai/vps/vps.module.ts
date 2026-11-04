// The VPS stack-agent slice (docs/specs/cloud-app.md section 4.10): the two node-eligible job types with their secret
// broker, the Route53 key store, and the routes the wizard's "Publish automatically" button uses. Needs the `credentials`
// slice (`packages/shared/slices.json`). `PrismaService`, `JobsService`, `JobHandlerRegistry`, `NodeOffloadService`
// and `AUDIT_SINK` are global.
import { Module } from '@nestjs/common';
import { CredentialsModule } from '@marinoscar/platform-api/credentials';

import { Route53KeyService } from './route53-key.service';
import { VpsPublishHandler, VpsUnpublishHandler } from './vps-jobs.handlers';
import { VpsSecretBroker } from './vps-secret.broker';
import { VpsSettingsService } from './vps-settings';
import { VpsStateService } from './vps-state.service';
import { VpsController } from './vps.controller';
import { VpsService } from './vps.service';

@Module({
  imports: [CredentialsModule],
  controllers: [VpsController],
  providers: [Route53KeyService, VpsSettingsService, VpsSecretBroker, VpsStateService, VpsPublishHandler, VpsUnpublishHandler, VpsService],
})
export class VpsModule {}
