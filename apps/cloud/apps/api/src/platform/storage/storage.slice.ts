// The storage slice: the objects API (`/api/storage`), the admin storage
// configuration (`/api/admin/storage-config`), uploaded profile pictures and the
// object-key prefix registry. The provider, bucket, region and credential are
// runtime settings (`/admin/settings/storage`): no environment variable.
//
// The app's key prefixes (none yet) are declared in `./storage-prefixes.ts`; the
// profile-image adapters are the host ports the slice replaces.
import type { ApiSlice } from '../slices/slice';

export const storageSlice: ApiSlice = {
  id: 'storage',
  permissionSlices: ['storage'],
  contribute: () => {
    const { STORAGE_SLICE_KEY_PREFIXES } = require('@marinoscar/platform-api/storage') as typeof import('@marinoscar/platform-api/storage');
    const { APP_STORAGE_KEY_PREFIXES } = require('./storage-prefixes') as typeof import('./storage-prefixes');
    const { STORAGE_CREDENTIAL_PURPOSE_DEF } = require('@marinoscar/platform-api/storage') as typeof import('@marinoscar/platform-api/storage');
    return {
      // The platform's four, then the app's. Other slices add theirs after, in mount order.
      storagePrefixes: [...STORAGE_SLICE_KEY_PREFIXES, ...APP_STORAGE_KEY_PREFIXES],
      credentialPurposes: [STORAGE_CREDENTIAL_PURPOSE_DEF],
    };
  },
  modules: () => {
    const config = require('./storage.config') as typeof import('./storage.config');
    return [config.StorageModule, config.ProfileImageModule, config.StorageConfigModule];
  },
  hostPorts: () => {
    const { StorageProfileImages } = require('./storage-profile-images') as typeof import('./storage-profile-images');
    const { IDENTITY_PROFILE_IMAGES } = require('@marinoscar/platform-api/identity') as typeof import('@marinoscar/platform-api/identity');
    const { SETTINGS_PROFILE_IMAGES } = require('@marinoscar/platform-api/settings') as typeof import('@marinoscar/platform-api/settings');
    return [
      { provide: IDENTITY_PROFILE_IMAGES, useClass: StorageProfileImages },
      { provide: SETTINGS_PROFILE_IMAGES, useClass: StorageProfileImages },
    ];
  },
};
