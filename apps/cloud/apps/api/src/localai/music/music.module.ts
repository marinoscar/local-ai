// The music feature (E11): `/api/localai/music/*`, the workload switch (`/api/localai/nodes/:nodeId/workloads/...`),
// the claim hook that signs a request's URLs, the settled-command listener and the object store. It needs the
// storage slice for the deployment's bucket (`packages/shared/slices.json` keeps `storage` enabled) and the
// multipart plugin `main.ts` registers with that slice.
import { Module } from '@nestjs/common';
import { StorageProvidersModule } from '@marinoscar/platform-api/storage';

import { LOCALAI_CLOCK, systemLocalaiClock } from '../commands/command-events';
import { CommandsModule } from '../commands/commands.module';
import { MusicClaimHook } from './music-claim';
import { MusicSettledListener } from './music-settled.listener';
import { MUSIC_OBJECT_STORE, StorageMusicObjectStore } from './music-store';
import { MusicController } from './music.controller';
import { MusicService } from './music.service';
import { WorkloadsController } from './workloads.controller';

@Module({
  imports: [CommandsModule, StorageProvidersModule],
  controllers: [MusicController, WorkloadsController],
  providers: [
    MusicService,
    MusicSettledListener,
    MusicClaimHook,
    { provide: MUSIC_OBJECT_STORE, useClass: StorageMusicObjectStore },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  exports: [MusicService],
})
export class MusicModule {}
