// The Local AI feature module (docs/specs/cloud-app.md): the app's own code on top of the platform. Each slice of the
// cloud adds its module here as it lands (node link now; state, commands, catalog, gateway later).
import { Module } from '@nestjs/common';

import { NodeLinkModule } from './node-link/node-link.module';

@Module({ imports: [NodeLinkModule] })
export class LocalaiModule {}
