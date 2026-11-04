// The credentials slice has no page of its own: the encrypted write-only secret
// field (`SecretField` of `@marinoscar/platform-web/credentials/ui`) is used by
// the pages of the slices that keep secrets (storage, email, notifications, AI).
// It is listed so the manifest, the API and the web agree on the same ids.
import type { WebSlice } from './slice';

export const credentialsWebSlice: WebSlice = { id: 'credentials' };
