import { Module } from '@nestjs/common';

import { LabNodesController } from './lab-nodes.controller';
import { LabNodesService } from './lab-nodes.service';
import { LabSettingsService } from './lab-settings.service';

/** The read-only Lab overview routes (`GET /api/localai/nodes`, `GET /api/localai/nodes/:nodeId`). `PrismaService` and the settings are global. */
@Module({ controllers: [LabNodesController], providers: [LabNodesService, LabSettingsService], exports: [LabNodesService, LabSettingsService] })
export class LabNodesModule {}
