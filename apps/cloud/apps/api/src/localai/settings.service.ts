// Reads and writes the `localai` settings namespace (docs/specs/cloud-app.md section 3.10). The write goes through the
// platform's `SystemSettingsService.patchSettings`, which merges with the namespace's `merge`, validates the whole
// document, bumps the version and audits (`system_settings:patch`). The platform's own HTTP routes refuse this namespace
// (see `localai.settings.ts`), so this service is the only writer and `SettingsController` the only door to it.
import { Injectable } from '@nestjs/common';
import { SystemSettingsService } from '@marinoscar/platform-api/settings';

import { flatten, nest, type LocalaiSettings, type LocalaiSettingsPatch } from './localai.settings';

/** The settings the key-management code needs. */
export interface GatewayKeyDefaults {
  user: LocalaiSettings['gateway']['defaults']['user'];
  service: LocalaiSettings['gateway']['defaults']['service'];
  maxUserKeyDays: number;
}

/** Where `KeyService` reads the defaults from. A port so the unit tests need no settings store. */
export const GATEWAY_KEY_DEFAULTS = Symbol('GATEWAY_KEY_DEFAULTS');
export interface GatewayKeyDefaultsReader {
  read(): Promise<GatewayKeyDefaults>;
}

@Injectable()
export class LocalaiSettingsService implements GatewayKeyDefaultsReader {
  constructor(private readonly system: SystemSettingsService) {}

  /** The whole namespace, complete (stored values over defaults, key by key). */
  async readAll(): Promise<LocalaiSettings> {
    return (await this.system.getNamespace('localai')) as LocalaiSettings;
  }

  /** The flat map of dotted keys. */
  async readFlat(): Promise<Record<string, unknown>> {
    return flatten(await this.readAll());
  }

  /** The key defaults as of now: changing `gateway.defaults.user` changes the next key. */
  async read(): Promise<GatewayKeyDefaults> {
    const { gateway } = await this.readAll();
    return { user: gateway.defaults.user, service: gateway.defaults.service, maxUserKeyDays: gateway.maxUserKeyDays };
  }

  /** Sets the given dotted keys (already validated) and answers the flat map after the write. */
  async write(values: Readonly<Record<string, unknown>>, actorUserId: string): Promise<Record<string, unknown>> {
    const patch = nest(values) as LocalaiSettingsPatch;
    await this.system.patchSettings({ localai: patch } as never, actorUserId);
    return this.readFlat();
  }
}
