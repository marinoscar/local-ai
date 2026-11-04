// =============================================================================
// Import-time registrations of the enabled slices
// =============================================================================
//
// The platform's registries are static and freeze once the module graph is
// built, so everything an enabled slice adds to them is registered HERE, once,
// before any `forRoot()`: `platform.ts` calls `registerSlices()` before it
// builds the first module. Never register from `onModuleInit`.
//
// The order is the order of the registries' dependencies: settings namespaces
// before `SettingsModule.forRoot()` composes the request bodies, credential
// purposes before the stores are used, storage prefixes before the purge reads
// them, then each slice's own `register()` (the notification manifest first,
// since other slices' events are merged into it).
// =============================================================================

import { registerCredentialPurpose } from '@marinoscar/platform-api/credentials';
import { registerStorageKeyPrefixes } from '@marinoscar/platform-api/storage';
import {
  registerSystemSettingsNamespaces,
  registerUserSettingsNamespaces,
  type SystemSettingsNamespace,
  type UserSettingsNamespace,
} from '@marinoscar/platform-api/settings';

import { contributions } from './contributions';
import { ENABLED, enabledSlices } from './manifest';

let done = false;
let settingsDone = false;

/**
 * Only the settings namespaces of the enabled slices. The seed imports this
 * (it composes the default `global` settings row from the registry and needs
 * no module graph).
 */
export function registerSliceSettings(): void {
  if (settingsDone) return;
  settingsDone = true;
  const { systemSettings, userSettings } = contributions();
  if (systemSettings.length > 0) registerSystemSettingsNamespaces(systemSettings as SystemSettingsNamespace[]);
  if (userSettings.length > 0) registerUserSettingsNamespaces(userSettings as UserSettingsNamespace[]);
}

/** Registers everything the enabled slices add to the static registries. Idempotent per process. */
export function registerSlices(): void {
  if (done) return;
  done = true;
  const merged = contributions();
  registerSliceSettings();
  for (const purpose of merged.credentialPurposes) registerCredentialPurpose(purpose);
  if (merged.storagePrefixes.length > 0) registerStorageKeyPrefixes(merged.storagePrefixes);
  for (const slice of ENABLED) slice.register?.(enabledSlices);
}
