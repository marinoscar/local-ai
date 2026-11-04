// The email slice's options, FRAMEWORK-FREE on purpose: the registration step
// (which configures the render context at import time, before Nest composes
// anything) and `EmailModule.forRoot` read the SAME object.
//
// Nothing here is runtime email configuration (transport, relay, sender,
// secrets): that is `/admin/settings/email`. `APP_URL` and `SES_REGION` are
// deployment variables of `infra/compose/.env.example`.
import { APP_NAME } from '@app/shared';
import type { EmailModuleOptions } from '@marinoscar/platform-api/email';
import { classifyRateLimit } from '@marinoscar/platform-api/jobs';

export const EMAIL_MODULE_OPTIONS: EmailModuleOptions = {
  appName: APP_NAME,
  appUrl: () => process.env.APP_URL || 'http://localhost:3535',
  // The queue's own HTTP / SDK classifier, so a throttled SES send reads as a
  // rate limit exactly as a throttled job does.
  classifyRateLimit,
  // No default, on purpose: a wrong region fails as "identity not verified".
  sesRegionFallback: () => process.env.SES_REGION || undefined,
};
