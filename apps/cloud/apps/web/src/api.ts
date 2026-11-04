import { APP_SLUG } from '@app/shared';
import { PlatformHttpClient, createPlatformApiClient } from '@marinoscar/platform-web/core';

/** The one HTTP client: same-origin `/api`, the refresh cookie, one refresh at a time across tabs. */
export const api = new PlatformHttpClient({
  baseUrl: import.meta.env.VITE_API_BASE_URL || '/api',
  refreshLockName: `${APP_SLUG}-auth-refresh`,
});

/** The transport packaged pages use (`PlatformWebHost.api`): the platform's adapter over the same client. */
export const platformApi = createPlatformApiClient(api);
