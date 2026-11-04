// The gateway's request-path services (key authentication, the settings it reads), resolved by `registerGatewayRoutes`
// with `app.get(...)`. The key-management API and the routing table add their own modules next to this one.
import { Module } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { GatewayAuthenticator } from './auth';
import { PrismaGatewayKeyStore, PrismaOwnerAccessReader } from './auth.prisma';
import { GatewaySettingsReader } from './gateway-settings';

@Module({
  providers: [
    GatewaySettingsReader,
    {
      provide: GatewayAuthenticator,
      inject: [PrismaService],
      useFactory: (prisma: PrismaService) => new GatewayAuthenticator({ store: new PrismaGatewayKeyStore(prisma), owners: new PrismaOwnerAccessReader(prisma) }),
    },
  ],
  exports: [GatewayAuthenticator, GatewaySettingsReader],
})
export class GatewayCoreModule {}
