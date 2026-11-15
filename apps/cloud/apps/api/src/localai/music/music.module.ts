// The music feature (E11): `/api/localai/music/*` (requests, songs, the saved prompts and the writing helpers of #568),
// the workload switch (`/api/localai/nodes/:nodeId/workloads/...`), the claim hook that signs a request's URLs, the
// settled-command listener and the object store. It needs the storage slice for the deployment's bucket
// (`packages/shared/slices.json` keeps `storage` enabled) and the multipart plugin `main.ts` registers with that slice.
//
// The AI helpers ("Suggest a title", "Write lyrics now", Automatic lyrics) use the `ai` slice when it is mounted:
// `MUSIC_AI` is then `AiServiceMusicAi` (AiService.forUser, loaded with `require` so the AI package is imported only
// then) and the "Use AI for music" route (`ai_config:*`) is mounted; else `NoMusicAi` (AI off: the generated title, the
// lyrics choice disabled, empty Automatic lyrics make a song with no vocals). `MUSIC_AI_SWITCH` is the `localaiMusic`
// settings namespace either way (#570).
import { Module, type Provider, type Type } from '@nestjs/common';
import { StorageProvidersModule } from '@marinoscar/platform-api/storage';

import { isSliceEnabled } from '../../platform/slices/manifest';
import { LOCALAI_CLOCK, systemLocalaiClock } from '../commands/command-events';
import { CommandsModule } from '../commands/commands.module';
import { MUSIC_AI, MUSIC_AI_SWITCH, NoMusicAi } from './music-ai';
import { SettingsMusicAiSwitch } from './music-ai-switch';
import { MusicAssistService } from './music-assist.service';
import { MusicClaimHook } from './music-claim';
import { MusicPromptsController } from './music-prompts.controller';
import { MusicPromptsService } from './music-prompts.service';
import { MusicSettledListener } from './music-settled.listener';
import { MUSIC_OBJECT_STORE, StorageMusicObjectStore } from './music-store';
import { MusicController } from './music.controller';
import { MusicService } from './music.service';
import { WorkloadsController } from './workloads.controller';

function aiBinding(): { imports: Type<unknown>[]; provider: Provider; controllers: Type<unknown>[] } {
  if (!isSliceEnabled('ai')) return { imports: [], provider: { provide: MUSIC_AI, useClass: NoMusicAi }, controllers: [] };
  return {
    imports: [(require('../../platform/ai/ai.config') as typeof import('../../platform/ai/ai.config')).AiModule as unknown as Type<unknown>],
    provider: { provide: MUSIC_AI, useClass: (require('./music-ai.adapter') as typeof import('./music-ai.adapter')).AiServiceMusicAi },
    controllers: [(require('./music-ai-settings.controller') as typeof import('./music-ai-settings.controller')).MusicAiSettingsController],
  };
}

const ai = aiBinding();

@Module({
  imports: [CommandsModule, StorageProvidersModule, ...ai.imports],
  controllers: [MusicController, MusicPromptsController, WorkloadsController, ...ai.controllers],
  providers: [
    MusicService,
    MusicPromptsService,
    MusicAssistService,
    MusicSettledListener,
    MusicClaimHook,
    ai.provider,
    SettingsMusicAiSwitch,
    { provide: MUSIC_AI_SWITCH, useExisting: SettingsMusicAiSwitch },
    { provide: MUSIC_OBJECT_STORE, useClass: StorageMusicObjectStore },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  exports: [MusicService],
})
export class MusicModule {}
