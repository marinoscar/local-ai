// The app's object-storage key prefixes. Declare every prefix a writer of this
// app uses, or `npm run storage:purge` (and the factory reset) leaves its
// objects in the bucket. A prefix is lowercase segments with exactly one
// trailing `/`, and must not overlap another registered prefix.
import type { StorageKeyPrefixDef } from '@marinoscar/platform-api/storage';

/** Every prefix this app adds. Append here (build keys with `buildObjectKey`, never by hand). */
export const APP_STORAGE_KEY_PREFIXES: readonly StorageKeyPrefixDef[] = [];
