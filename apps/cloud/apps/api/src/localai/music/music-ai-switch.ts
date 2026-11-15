// "Use AI for music" read and written through the settings slice (#570). The read goes through `SettingsResolver`, so
// an organization's own "off" applies (the namespace's tighten-only org layer); the write is the deployment value,
// through `SystemSettingsService.patchSettings`, which validates, versions and audits it (`system_settings:patch`).
import { Injectable } from '@nestjs/common';
import { SettingsResolver, SystemSettingsService } from '@marinoscar/platform-api/settings';

import type { MusicAiSwitch } from './music-ai';
import { MUSIC_AI_SETTINGS_NAMESPACE, readLocalaiMusicSettings, type LocalaiMusicSettings } from './music-ai.settings';

@Injectable()
export class SettingsMusicAiSwitch implements MusicAiSwitch {
  constructor(
    private readonly resolver: SettingsResolver,
    private readonly system: SystemSettingsService,
  ) {}

  /** The effective switch for this organization (the deployment's, tightened by the organization's). */
  async isOn(orgId?: string | null): Promise<boolean> {
    const value = await this.resolver.resolveSystem<LocalaiMusicSettings>(MUSIC_AI_SETTINGS_NAMESPACE, orgId ? { orgId } : {});
    return readLocalaiMusicSettings(value).aiEnabled;
  }

  /** The deployment's own value (the administrator page shows and changes this one). */
  async deploymentValue(): Promise<LocalaiMusicSettings> {
    return readLocalaiMusicSettings(await this.system.getNamespace('localaiMusic'));
  }

  /** Sets the deployment's value and answers it after the write. */
  async setDeploymentValue(aiEnabled: boolean, actorUserId: string): Promise<LocalaiMusicSettings> {
    await this.system.patchSettings({ localaiMusic: { aiEnabled } } as never, actorUserId);
    return this.deploymentValue();
  }
}
