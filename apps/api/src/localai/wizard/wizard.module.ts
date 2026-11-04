import { Module } from '@nestjs/common';

import { NodeKeysModule } from '../keys/node-keys.module';
import { ChecklistService } from './checklist.service';
import { NETWORK_PROBE } from './network-probe.port';
import { NodeNetworkProbe } from './network-probe.node';
import { GithubReleaseChecksums, RELEASE_CHECKSUMS } from './release-checksums.port';
import { WizardController } from './wizard.controller';
import { WizardService } from './wizard.service';
import { WizardSettingsService } from './wizard-settings';

/**
 * The Add-node wizard (`POST /api/localai/nodes`, `GET /api/localai/nodes/next-id` and the publish checklist routes).
 * The network probe and the release-checksum reader are ports bound to the real implementations here; a test overrides
 * `NETWORK_PROBE` and `RELEASE_CHECKSUMS`. `PrismaService`, `AUDIT_SINK` and `NodeCredentialService` are global.
 */
@Module({
  imports: [NodeKeysModule],
  controllers: [WizardController],
  providers: [
    WizardService,
    ChecklistService,
    WizardSettingsService,
    { provide: NETWORK_PROBE, useClass: NodeNetworkProbe },
    { provide: RELEASE_CHECKSUMS, useClass: GithubReleaseChecksums },
  ],
})
export class WizardModule {}
