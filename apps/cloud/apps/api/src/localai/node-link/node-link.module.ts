import { Module } from '@nestjs/common';

import { LocalaiNodeBindingGuard } from './binding.guard';
import { NodeLinkController } from './node-link.controller';
import { LOCALAI_NODE_NOTIFIER, NoopLocalaiNodeNotifier } from './node-link.notifier';

/**
 * The node-facing routes and their guard. `PrismaService` and the platform's `AUDIT_SINK` are global.
 * CLOUD-15 replaces the no-op notifier by providing `LOCALAI_NODE_NOTIFIER` here.
 */
@Module({
  controllers: [NodeLinkController],
  providers: [LocalaiNodeBindingGuard, { provide: LOCALAI_NODE_NOTIFIER, useClass: NoopLocalaiNodeNotifier }],
  exports: [LocalaiNodeBindingGuard, LOCALAI_NODE_NOTIFIER],
})
export class NodeLinkModule {}
