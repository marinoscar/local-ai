// The storage slice, configured once. `StorageModule` and `ProfileImageModule`
// are the CONFIGURED dynamic modules of `@marinoscar/platform-api/storage`,
// built when this file is first required (only for an enabled slice); every
// importer gets the same object. No option decides the provider, bucket,
// region, endpoint or credential: those are runtime settings.
import {
  ProfileImageModule as PlatformProfileImageModule,
  StorageConfigModule,
  StorageModule as PlatformStorageModule,
} from '@marinoscar/platform-api/storage';

import { StorageHostModule } from './storage-host.module';

/** The objects API, its status route, the cleanup sweep and the processing job. */
export const StorageModule = PlatformStorageModule.forRoot({ imports: [StorageHostModule] });

/** The profile-image routes (`/api/user-settings/profile-image`, the public avatar route). */
export const ProfileImageModule = PlatformProfileImageModule.forRoot();

export { StorageConfigModule, StorageProvidersModule } from '@marinoscar/platform-api/storage';
