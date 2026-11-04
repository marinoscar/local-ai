// Gateway key management. `PrismaService` and `AUDIT_SINK` are global. The auth cache (`GATEWAY_KEY_AUTH_CACHE`) is
// bound by the `/v1` authentication (GW-03) from a global module; until it is, revocation has no cache to evict.
import { Module } from '@nestjs/common';

import { LocalaiSettingsModule } from '../../settings.module';
import { GATEWAY_KEY_DEFAULTS, LocalaiSettingsService } from '../../settings.service';
import { KeyService } from './key.service';
import { GATEWAY_KEY_STORE } from './key.store';
import { GATEWAY_KNOWN_MODELS, CatalogKnownModels } from './known-models';
import { KeysController } from './keys.controller';
import { PrismaKeyStore } from './prisma-key.store';

@Module({
  imports: [LocalaiSettingsModule],
  controllers: [KeysController],
  providers: [
    KeyService,
    PrismaKeyStore,
    CatalogKnownModels,
    { provide: GATEWAY_KEY_STORE, useExisting: PrismaKeyStore },
    { provide: GATEWAY_KNOWN_MODELS, useExisting: CatalogKnownModels },
    { provide: GATEWAY_KEY_DEFAULTS, useExisting: LocalaiSettingsService },
  ],
  exports: [KeyService],
})
export class KeysModule {}
