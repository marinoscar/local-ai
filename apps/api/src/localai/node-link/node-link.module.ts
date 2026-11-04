import { Module } from '@nestjs/common';

import { NodeKeysModule } from '../keys/node-keys.module';
import { LocalaiNodeBindingGuard } from './binding.guard';
import { NodeKeyController } from './key.controller';
import { NodeLinkController } from './node-link.controller';
import { LOCALAI_NODE_NOTIFIER, NoopLocalaiNodeNotifier } from './node-link.notifier';
import { LOCALAI_ALERT_REEVALUATOR, NoopLocalaiAlertReevaluator } from './state.hooks';
import { StateService } from './state.service';

/**
 * The node-facing routes and their guard. `PrismaService` and the platform's `AUDIT_SINK` are global.
 * CLOUD-15 replaces the no-op notifier by providing `LOCALAI_NODE_NOTIFIER` here.
 */
@Module({
  imports: [NodeKeysModule],
  controllers: [NodeLinkController, NodeKeyController],
  providers: [
    LocalaiNodeBindingGuard,
    { provide: LOCALAI_NODE_NOTIFIER, useClass: NoopLocalaiNodeNotifier },
    // CLOUD-15 (alerts) replaces the no-op re-evaluator by providing LOCALAI_ALERT_REEVALUATOR here.
    { provide: LOCALAI_ALERT_REEVALUATOR, useClass: NoopLocalaiAlertReevaluator },
    StateService,
  ],
  exports: [LocalaiNodeBindingGuard, LOCALAI_NODE_NOTIFIER, LOCALAI_ALERT_REEVALUATOR, StateService],
})
export class NodeLinkModule {}
