// Gateway key management. `PrismaService` and `AUDIT_SINK` are global. The auth cache that revocation evicts from is
// the `/v1` authenticator's own (`GatewayAuthenticator.evict`, GW-03), bound to `GATEWAY_KEY_AUTH_CACHE` here.
import { Module } from '@nestjs/common';

import { LocalaiSettingsModule } from '../../settings.module';
import { GatewayAuthenticator } from '../auth';
import { GatewayCoreModule } from '../gateway-core.module';
import { GATEWAY_KEY_AUTH_CACHE } from '../key-auth-cache';
import { GATEWAY_KEY_DEFAULTS, LocalaiSettingsService } from '../../settings.service';
import { KeyService } from './key.service';
import { GATEWAY_KEY_STORE } from './key.store';
import { GATEWAY_KNOWN_MODELS, CatalogKnownModels } from './known-models';
import { KeysController } from './keys.controller';
import { PrismaKeyStore } from './prisma-key.store';

@Module({
  imports: [LocalaiSettingsModule, GatewayCoreModule],
  controllers: [KeysController],
  providers: [
    KeyService,
    PrismaKeyStore,
    CatalogKnownModels,
    { provide: GATEWAY_KEY_STORE, useExisting: PrismaKeyStore },
    { provide: GATEWAY_KNOWN_MODELS, useExisting: CatalogKnownModels },
    { provide: GATEWAY_KEY_DEFAULTS, useExisting: LocalaiSettingsService },
    // Revocation and scope changes take effect on the next /v1 request, not after the cache's 10 s.
    { provide: GATEWAY_KEY_AUTH_CACHE, useExisting: GatewayAuthenticator },
  ],
  exports: [KeyService],
})
export class KeysModule {}
