import { Module } from '@nestjs/common';

import { JobsAlertReevaluator } from '../alerts/alert-reevaluator';
import { LabAudience } from '../lab-audience';
import { NodeKeysModule } from '../keys/node-keys.module';
import { LocalaiNodeBindingGuard } from './binding.guard';
import { NodeKeyController } from './key.controller';
import { NodeLinkController } from './node-link.controller';
import { NotificationsNodeNotifier } from './node-enrolled.notifier';
import { LOCALAI_NODE_NOTIFIER } from './node-link.notifier';
import { LOCALAI_ALERT_REEVALUATOR } from './state.hooks';
import { StateService } from './state.service';

/**
 * The node-facing routes and their guard. `PrismaService` and the platform's `AUDIT_SINK` are global.
 * `LOCALAI_NODE_NOTIFIER` is the notifications slice's `localai.node_enrolled` (CLOUD-15); `LOCALAI_ALERT_REEVALUATOR` queues
 * the one-node alert evaluation job (CLOUD-15).
 */
@Module({
  imports: [NodeKeysModule],
  controllers: [NodeLinkController, NodeKeyController],
  providers: [
    LocalaiNodeBindingGuard,
    LabAudience,
    { provide: LOCALAI_NODE_NOTIFIER, useClass: NotificationsNodeNotifier },
    { provide: LOCALAI_ALERT_REEVALUATOR, useClass: JobsAlertReevaluator },
    StateService,
  ],
  exports: [LocalaiNodeBindingGuard, LOCALAI_NODE_NOTIFIER, LOCALAI_ALERT_REEVALUATOR, StateService],
})
export class NodeLinkModule {}
