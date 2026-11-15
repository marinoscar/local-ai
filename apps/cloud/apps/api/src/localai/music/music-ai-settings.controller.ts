// "Use AI for music" (#570, docs/specs/cloud-app.md section 3.10): the deployment's switch, read and written by the AI
// administrators. The permissions are the AI slice's own (`ai_config:read`, `ai_config:write`): the people who add
// providers, keys and models also decide whether the music page may use them, and the admin card
// ("Use AI for music", `/admin/settings/ai/music`) declares the same `ai_config:read`.
//
// Mounted only while the `ai` slice is (`music.module.ts`), because only then are those permissions seeded. Like
// `/api/admin/ai/*`, it is NOT behind the AI kill switch: an administrator can prepare it while AI is off.
import { Body, Controller, Get, Put } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import { AI_PERMISSIONS } from '@marinoscar/platform-api/ai';
import { Auth, CurrentUser, type RequestUser } from '@marinoscar/platform-api/identity';
import { z } from 'zod';

import { parseWith } from '../commands/command-api.schemas';
import { SettingsMusicAiSwitch } from './music-ai-switch';
import type { LocalaiMusicSettings } from './music-ai.settings';

/** `PUT /api/localai/music/ai-settings`: the one switch. Strict: an unknown field is a 400. */
export const MusicAiSettingsBodySchema = z.object({ aiEnabled: z.boolean() }).strict();

@ApiTags('localai-music')
@Controller('localai/music/ai-settings')
export class MusicAiSettingsController {
  constructor(private readonly musicSwitch: SettingsMusicAiSwitch) {}

  @Get()
  @Auth({ permissions: [AI_PERMISSIONS.AI_CONFIG_READ.id] })
  @ApiOperation({ summary: 'Use AI for music: { aiEnabled } (default false). ai_config:read' })
  get(): Promise<LocalaiMusicSettings> {
    return this.musicSwitch.deploymentValue();
  }

  @Put()
  @Auth({ permissions: [AI_PERMISSIONS.AI_CONFIG_WRITE.id] })
  @ApiOperation({ summary: 'Turn Use AI for music on or off: { aiEnabled: boolean }. ai_config:write; audited as a system settings change' })
  put(@Body() body: unknown, @CurrentUser() user: RequestUser): Promise<LocalaiMusicSettings> {
    return this.musicSwitch.setDeploymentValue(parseWith(MusicAiSettingsBodySchema, body).aiEnabled, user.id);
  }
}
