// The Local AI feature module (docs/specs/cloud-app.md): the app's own code on top of the platform. Each slice of the
// cloud adds its module here as it lands (node link now; state, commands, catalog, gateway later).
import { Module } from '@nestjs/common';

import { CatalogModule } from './catalog/catalog.module';
import { CommandsModule } from './commands/commands.module';
import { LocalaiNodesModule } from './nodes/nodes.module';
import { NodeLinkModule } from './node-link/node-link.module';

@Module({ imports: [NodeLinkModule, LocalaiNodesModule, CatalogModule, CommandsModule] })
export class LocalaiModule {}
