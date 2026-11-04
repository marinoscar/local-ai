import { Module } from '@nestjs/common';

import { NodeKeysModule } from '../keys/node-keys.module';
import { LocalaiNodeBindingGuard } from './binding.guard';
import { NodeKeyController } from './key.controller';
import { NodeLinkController } from './node-link.controller';
import { LOCALAI_NODE_NOTIFIER, NoopLocalaiNodeNotifier } from './node-link.notifier';

/**
 * The node-facing routes and their guard. `PrismaService` and the platform's `AUDIT_SINK` are global.
 * CLOUD-15 replaces the no-op notifier by providing `LOCALAI_NODE_NOTIFIER` here.
 */
@Module({
  imports: [NodeKeysModule],
  controllers: [NodeLinkController, NodeKeyController],
  providers: [LocalaiNodeBindingGuard, { provide: LOCALAI_NODE_NOTIFIER, useClass: NoopLocalaiNodeNotifier }],
  exports: [LocalaiNodeBindingGuard, LOCALAI_NODE_NOTIFIER],
})
export class NodeLinkModule {}
