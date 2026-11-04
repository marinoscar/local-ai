// The `storage` system settings namespace, registered before
// `SettingsModule.forRoot()` composes the request bodies (platform.ts imports
// this first). The user-data slice moves object bytes through the storage
// provider, which reads this namespace; nothing is uploaded until an
// administrator configures a bucket. Enable the storage slice for its routes
// and settings page (README, "Enable more of the platform").
import { registerSystemSettingsNamespaces, type SystemSettingsNamespace } from '@marinoscar/platform-api/settings';
import { STORAGE_SYSTEM_SETTINGS } from '@marinoscar/platform-api/storage';

registerSystemSettingsNamespaces([STORAGE_SYSTEM_SETTINGS as SystemSettingsNamespace]);
